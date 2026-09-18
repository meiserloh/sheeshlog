package sheeshlog_test

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meiserloh/sheeshlog"
	"github.com/meiserloh/sheeshlog/internal/profile"
)

var update = flag.Bool("update", false, "regenerate golden files")

// testEnv returns an Env pointing at a temporary config root, so tests never
// read or write the developer's real profiles.
//
// The profiles directory is created empty, which is what keeps these tests
// about the thing they are testing: an absent one is the first run, and the
// first run writes an example profile and says so, which every golden here
// would otherwise have to carry. firstRunEnv is the env for that case.
func testEnv(t *testing.T) sheeshlog.Env {
	t.Helper()
	env := firstRunEnv(t)
	if err := os.MkdirAll(filepath.Join(env.ConfigRoot, "sheesh", "profiles"), 0o755); err != nil {
		t.Fatal(err)
	}
	return env
}

// firstRunEnv is a config root with nothing in it at all: the fresh machine.
func firstRunEnv(t *testing.T) sheeshlog.Env {
	t.Helper()
	return sheeshlog.Env{
		ConfigRoot: t.TempDir(),
		Vars:       map[string]string{},
	}
}

// ttyEnv is testEnv with stdout looking like a terminal, which is what turns
// colour on under the default --color=auto.
func ttyEnv(t *testing.T) sheeshlog.Env {
	t.Helper()
	env := testEnv(t)
	env.StdoutIsTerminal = true
	return env
}

// writeProfile puts a profile file where a user's would be, so tests exercise
// the same on-disk path the tool resolves at runtime. The path is spelled out
// here rather than borrowed from the profile package: if that layout ever
// changes, a test should notice.
func writeProfile(t *testing.T, env sheeshlog.Env, name, body string) {
	t.Helper()
	dir := filepath.Join(env.ConfigRoot, "sheesh", "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// golden runs Run with the given argv and fixture on stdin, then diffs stdout,
// stderr and the exit code against a committed golden file. Regenerate with
// `go test -update`; never automatically, because an unreviewed golden update
// is a silently accepted layout change.
func golden(t *testing.T, name string, argv []string, fixture string, env sheeshlog.Env) {
	t.Helper()

	in, err := os.Open(filepath.Join("testdata", "fixtures", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = in.Close() }()

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run(argv, in, &stdout, &stderr, env)

	var got bytes.Buffer
	got.WriteString(stdout.String())
	if stderr.Len() > 0 {
		got.WriteString("--- stderr ---\n")
		got.WriteString(stderr.String())
	}
	if code != 0 {
		got.WriteString("--- exit ---\n")
		got.WriteString(string(rune('0'+code)) + "\n")
	}

	path := filepath.Join("testdata", "golden", name+".txt")
	if *update {
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden file (run `go test -update` to create it): %v", err)
	}
	if got.String() != string(want) {
		t.Errorf("output differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got.String(), want)
	}
}

// --level trace pins this to the layout, not the threshold: the fixture has a
// debug line carrying the nested-field case, and the default threshold would
// hide it.
func TestRenderBasicJSONLines(t *testing.T) {
	golden(t, "basic", []string{"--level", "trace"}, "basic.log", testEnv(t))
}

// A line longer than bufio's default 64 KB must render, not error: embedded
// payloads and stack traces are exactly the interesting lines.
func TestLongLineRenders(t *testing.T) {
	long := strings.Repeat("x", 100_000)
	in := strings.NewReader(`{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"` + long + `"}` + "\n")

	var stdout, stderr bytes.Buffer
	if code := sheeshlog.Run(nil, in, &stdout, &stderr, testEnv(t)); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), long) {
		t.Errorf("long message was not rendered in full (got %d bytes)", stdout.Len())
	}
}

// Output is flushed per line, so a `-f` tail appears as it happens rather than
// arriving in block-buffered clumps.
func TestOutputIsFlushedPerLine(t *testing.T) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	go func() {
		_ = sheeshlog.Run(nil, stdinR, stdoutW, io.Discard, testEnv(t))
		_ = stdoutW.Close()
	}()

	if _, err := io.WriteString(stdinW, `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"first"}`+"\n"); err != nil {
		t.Fatal(err)
	}

	got := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdoutR).ReadString('\n')
		got <- line
	}()

	select {
	case line := <-got:
		if !strings.Contains(line, "first") {
			t.Errorf("first line = %q", line)
		}
	case <-time.After(2 * time.Second):
		t.Error("no output before stdin closed: writer is buffering whole blocks")
	}
	_ = stdinW.Close()
}

// The default threshold is info, so the debug chatter is gone but the
// unrankable lines — no level, an unrecognised one, and the panic that never
// parsed — are all still there.
func TestLevelDefaultThreshold(t *testing.T) {
	golden(t, "levels-default", nil, "levels.log", testEnv(t))
}

func TestLevelThresholds(t *testing.T) {
	for _, l := range []string{"trace", "debug", "info", "warn", "warning", "error"} {
		t.Run(l, func(t *testing.T) {
			golden(t, "levels-"+l, []string{"--level", l}, "levels.log", testEnv(t))
		})
	}
}

// LEVEL carries the muscle memory from the oplog script, so it has to work
// unaided — and lose to an explicit flag.
func TestLevelFromEnv(t *testing.T) {
	env := testEnv(t)
	env.Vars["LEVEL"] = "debug"
	golden(t, "levels-env-debug", nil, "levels.log", env)
}

// The flag has to win even when it is the *less* verbose of the two, or the
// rule is indistinguishable from "the stricter threshold wins".
func TestLevelFlagBeatsEnv(t *testing.T) {
	env := testEnv(t)
	env.Vars["LEVEL"] = "debug"
	golden(t, "levels-flag-beats-env", []string{"--level", "info"}, "levels.log", env)
}

// An unusable threshold is a mistake worth stopping for: rendering at some
// guessed level would quietly hide lines the user asked to see.
func TestLevelUnusableValue(t *testing.T) {
	golden(t, "levels-bad-flag", []string{"--level", "loud"}, "levels.log", testEnv(t))
}

// An unknown flag is reported once, by sheesh, rather than twice with the flag
// package's own usage dump in between.
func TestUnknownFlag(t *testing.T) {
	golden(t, "unknown-flag", []string{"--nope"}, "levels.log", testEnv(t))
}

// -h is a request, not a mistake: usage on stdout, exit 0.
func TestHelpFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"-h"}, strings.NewReader(""), &stdout, &stderr, testEnv(t))
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stderr.Len() > 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}

	for _, want := range []string{"-level", "-color", "NO_COLOR", "-profile-for-pod", "ignoring the remembered profile", "-version"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("usage does not mention %s:\n%s", want, stdout.String())
		}
	}
}

func TestVersionFlagWithEmptyConfig(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--version"}, strings.NewReader(""), &stdout, &stderr, firstRunEnv(t))
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	// The literal, not sheeshlog.Version: a test that reads the constant it is
	// checking would follow a wrong bump instead of catching it.
	if want := "sheesh 1.0.0\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	// Not even the first-run example-profile notice: the output of --version is
	// the whole of what the run says.
	if stderr.Len() > 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestLevelUnusableEnvValue(t *testing.T) {
	env := testEnv(t)
	env.Vars["LEVEL"] = "loud"
	golden(t, "levels-bad-env", nil, "levels.log", env)
}

// kubectl logs --prefix output has to render as if the prefix were absent, and
// a panic — including the stack frame that happens to contain a brace — has to
// survive byte for byte, interleaved in stream order.
func TestPrefixedAndUnparseableLines(t *testing.T) {
	golden(t, "prefixed", nil, "prefixed.log", testEnv(t))
}

// Verbatim passthrough is byte for byte, and the line terminator is a byte: a
// last line that arrived unterminated must not grow a newline it never had.
func TestUnparseableFinalLineKeepsItsBytes(t *testing.T) {
	const raw = "panic: runtime error: invalid memory address"

	var stdout, stderr bytes.Buffer
	if code := sheeshlog.Run(nil, strings.NewReader(raw), &stdout, &stderr, testEnv(t)); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != raw {
		t.Errorf("stdout = %q, want %q byte for byte", stdout.String(), raw)
	}
}

// The reserved name is claimed only by a prefix that was really there, so a
// payload carrying its own _prefix key still surfaces as an extra: a field
// nobody anticipated must never disappear silently.
func TestPayloadPrefixKeySurvivesAsExtra(t *testing.T) {
	in := strings.NewReader(`{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi","_prefix":"FAKE"}` + "\n")

	var stdout, stderr bytes.Buffer
	if code := sheeshlog.Run(nil, in, &stdout, &stderr, testEnv(t)); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if want := "05:03:12 I -  hi  _prefix=FAKE\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
}

// Colour is decided once per run, so every configuration that means "colour"
// has to produce the same bytes as every other. Sharing one golden file across
// the four is the assertion.
func TestColourOnConfigurations(t *testing.T) {
	t.Run("auto on a terminal", func(t *testing.T) {
		golden(t, "colour-on", []string{"--level", "trace"}, "levels.log", ttyEnv(t))
	})
	t.Run("always off a terminal", func(t *testing.T) {
		golden(t, "colour-on", []string{"--level", "trace", "--color=always"}, "levels.log", testEnv(t))
	})
	// The decided precedence, documented in --help: an explicit --color beats
	// the environment, exactly as --level beats LEVEL.
	t.Run("always beats NO_COLOR", func(t *testing.T) {
		env := ttyEnv(t)
		env.Vars["NO_COLOR"] = "1"
		golden(t, "colour-on", []string{"--level", "trace", "--color=always"}, "levels.log", env)
	})
	// NO_COLOR counts when present and non-empty; an empty value is not a
	// request for plain output.
	t.Run("empty NO_COLOR is not a request", func(t *testing.T) {
		env := ttyEnv(t)
		env.Vars["NO_COLOR"] = ""
		golden(t, "colour-on", []string{"--level", "trace"}, "levels.log", env)
	})
}

// The mirror image: every configuration meaning "no colour" produces clean,
// greppable bytes, so `sheesh < file | grep backup-123` and a redirect both
// behave.
func TestColourOffConfigurations(t *testing.T) {
	t.Run("auto off a terminal", func(t *testing.T) {
		golden(t, "colour-off", []string{"--level", "trace"}, "levels.log", testEnv(t))
	})
	t.Run("never on a terminal", func(t *testing.T) {
		golden(t, "colour-off", []string{"--level", "trace", "--color=never"}, "levels.log", ttyEnv(t))
	})
	t.Run("NO_COLOR on a terminal", func(t *testing.T) {
		env := ttyEnv(t)
		env.Vars["NO_COLOR"] = "1"
		golden(t, "colour-off", []string{"--level", "trace"}, "levels.log", env)
	})
}

// An unusable --color value is a mistake worth stopping for, the same as an
// unusable --level.
func TestColourUnusableValue(t *testing.T) {
	golden(t, "colour-bad-flag", []string{"--color=loud"}, "levels.log", testEnv(t))
}

// Plain output must carry no escape bytes at all, not merely look right in a
// golden file a human skimmed.
func TestColourOffEmitsNoEscapeBytes(t *testing.T) {
	in := strings.NewReader(`{"ts":"2026-08-21T05:03:12Z","level":"error","msg":"boom","k":"v"}` + "\n")

	var stdout, stderr bytes.Buffer
	if code := sheeshlog.Run([]string{"--color=never"}, in, &stdout, &stderr, testEnv(t)); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if strings.ContainsRune(stdout.String(), '\x1b') {
		t.Errorf("plain output contains an escape byte: %q", stdout.String())
	}
}

// levels.log has neither extras nor a subject, so the colour goldens above pin
// only the level ramp. This pins the other two painted slots: the cyan subject
// and the whole run of extras dimmed as one.
func TestColourOnExtrasAndSubject(t *testing.T) {
	golden(t, "colour-basic", []string{"--level", "trace"}, "basic.log", ttyEnv(t))
}

// A shouted flag value is accepted the way a shouted level is.
func TestColourValueIsCaseInsensitive(t *testing.T) {
	golden(t, "colour-off", []string{"--level", "trace", "--color=NEVER"}, "levels.log", ttyEnv(t))
}

// Two profiles, one fixture: the layout is identical and only the content
// moves, which is ADR-0001 made visible. The subject candidates are listed in
// opposite orders so that "first present wins" is what distinguishes them.
func TestTwoProfilesSameLayout(t *testing.T) {
	const operator = `
time: ts
level: level
message: msg
subject:
  - name
  - Backup.name
`
	const nested = `
time: ts
level: level
message: detail
subject:
  - Backup.name
  - name
`
	t.Run("operator", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "operator", operator)
		golden(t, "profiles-operator", []string{"--level", "trace", "--profile", "operator"}, "profiles.log", env)
	})
	t.Run("nested", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "nested", nested)
		golden(t, "profiles-nested", []string{"--level", "trace", "--profile", "nested"}, "profiles.log", env)
	})
}

// A typo in a script must fail loudly rather than render with some other
// profile. The message names what was looked for, so the fix is obvious.
// Asserted rather than pinned by a golden: the message carries the temporary
// config path, which differs on every run.
func TestUnknownProfileNamesIt(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "nope"}, strings.NewReader(""), &stdout, &stderr, testEnv(t))

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout.Len() > 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `"nope"`) {
		t.Errorf("stderr does not name the profile: %q", stderr.String())
	}
}

// A malformed profile names the file and the problem, so it can be fixed
// without guessing which of several profiles broke.
func TestMalformedProfileNamesFileAndProblem(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "broken", "time: ts\nsubject: [unclosed\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "broken"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "broken.yaml") {
		t.Errorf("stderr does not name the file: %q", stderr.String())
	}
}

// An unknown key is a typo that would otherwise do nothing at all, which is the
// silent failure this tool exists to stop.
func TestUnknownProfileKeyIsRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "typo", "time: ts\nmesage: msg\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "typo"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "mesage") {
		t.Errorf("stderr does not name the offending key: %q", stderr.String())
	}
}

// The file name is the profile name, so a name carrying a separator is not a
// profile of the user's. Joining it onto the profiles directory would read a
// file from anywhere on disk.
func TestProfileNameCannotEscapeTheProfilesDirectory(t *testing.T) {
	env := testEnv(t)
	outside := filepath.Join(env.ConfigRoot, "outside.yaml")
	if err := os.WriteFile(outside, []byte("time: ts\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"../outside", "../../etc/passwd", "..", "sub/other"} {
		var stdout, stderr bytes.Buffer
		code := sheeshlog.Run([]string{"--profile", name}, strings.NewReader(""), &stdout, &stderr, env)
		if code == 0 {
			t.Errorf("--profile %q exited 0, want a refusal", name)
		}
	}
}

// An unset shell variable arrives as an empty name. Rendering with the built-in
// profile would look like success, so it fails instead; omitting the flag
// entirely is the only way to ask for the built-in one.
func TestEmptyProfileNameIsRefused(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", ""}, strings.NewReader(""), &stdout, &stderr, testEnv(t))

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "profile") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

// The example profile is the one the tool ships and writes on a first run,
// not a copy of it kept for the tests. An example nobody runs rots quietly;
// this makes a broken one fail the suite.
func TestExampleProfileRenders(t *testing.T) {
	env := firstRunEnv(t)
	if _, err := profile.WriteExample(env.ConfigRoot); err != nil {
		t.Fatal(err)
	}
	golden(t, "operator", []string{"--level", "trace", "--profile", "operator"}, "operator.log", env)
}

// A deny-list, not an allow-list: naming a field suppresses it, and a field
// nobody thought to name still arrives as an extra. The same fixture rendered
// with and without the list is the whole claim.
func TestContextFieldsAreADenyList(t *testing.T) {
	const base = `
time: ts
level: level
message: msg
subject:
  - name
`
	t.Run("without a context list", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "plain", base)
		golden(t, "context-none", []string{"--level", "trace", "--profile", "plain"}, "profiles.log", env)
	})
	// Backup is suppressed; detail is not named and therefore survives.
	t.Run("with a context list", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "denied", base+"context:\n  - Backup\n")
		golden(t, "context-denied", []string{"--level", "trace", "--profile", "denied"}, "profiles.log", env)
	})
}

// The stream prefix is addressable wherever a path is, so a profile can put the
// pod a line came from in the subject slot when tailing several at once.
func TestPrefixAsSubjectCandidate(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "bypod", "time: ts\nlevel: level\nmessage: msg\nsubject:\n  - _prefix\n")
	golden(t, "prefix-as-subject", []string{"--level", "trace", "--profile", "bypod"}, "prefixed.log", env)
}

// _prefix is usable in the context list too, and it is not a formality. A real
// stream prefix is already kept out of the extras, but a payload carrying its
// own _prefix key is deliberately left visible by the parser, and naming it
// here is what suppresses that.
func TestPrefixAsContextField(t *testing.T) {
	const line = `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi","_prefix":"FAKE"}` + "\n"
	const base = "time: ts\nlevel: level\nmessage: msg\n"

	run := func(t *testing.T, body string) string {
		t.Helper()
		env := testEnv(t)
		writeProfile(t, env, "p", body)

		var stdout, stderr bytes.Buffer
		if code := sheeshlog.Run([]string{"--profile", "p"}, strings.NewReader(line), &stdout, &stderr, env); code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
		}
		return stdout.String()
	}

	if got, want := run(t, base), "05:03:12 I -  hi  _prefix=FAKE\n"; got != want {
		t.Errorf("unlisted: stdout = %q, want %q", got, want)
	}
	if got, want := run(t, base+"context:\n  - _prefix\n"), "05:03:12 I -  hi\n"; got != want {
		t.Errorf("listed as a context field: stdout = %q, want %q", got, want)
	}
}

// A dotted context field would match no top-level key and suppress nothing,
// which looks exactly like a profile that works.
func TestDottedContextFieldIsRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "dotted", "time: ts\ncontext:\n  - Backup.name\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "dotted"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "Backup.name") {
		t.Errorf("stderr does not name the offending field: %q", stderr.String())
	}
}

// The claim of a noise rule is that a fresh tail opens with the events you care
// about rather than startup chatter. The same fixture rendered with and without
// the rules is that claim: the rules are the only difference between the two
// golden files.
//
// The two rules are the ones baked into the original oplog script, written
// verbatim, so this also pins that both remain expressible in the one rule
// shape.
func TestNoiseRulesDropWholeLines(t *testing.T) {
	const base = `
time: ts
level: level
message: msg
subject:
  - name
`
	const noise = base + `
noise:
  - {field: msg, matches: "^(Starting|Serving|Stopping|Shutdown|starting server|Wait completed)"}
  - {field: logger, matches: "^controller-runtime"}
`
	// Without the rules every line survives, including the two the fixture's
	// last three lines have no field for: a rule naming a field a line does not
	// carry must not match, and must not error either.
	t.Run("without noise rules", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "quiet", base)
		golden(t, "noise-none", []string{"--level", "trace", "--profile", "quiet"}, "noise.log", env)
	})
	// The dropped lines are info, warn and error, so this also pins that a rule
	// applies regardless of level: an error-level "Shutdown signal received" is
	// still startup chatter.
	t.Run("with noise rules", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "quiet", noise)
		golden(t, "noise-applied", []string{"--level", "trace", "--profile", "quiet"}, "noise.log", env)
	})
}

// A broken regex is broken whether or not a matching line ever arrives, so it
// fails before the first line is read. The message names the file and which
// rule in it, because that is what the user has to go and edit. Asserted rather
// than pinned by a golden: the message carries the temporary config path.
func TestInvalidNoiseRegexNamesFileAndRule(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "broken", "time: ts\nnoise:\n  - {field: msg, matches: \"^(unclosed\"}\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "broken"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	for _, want := range []string{"broken.yaml", "noise rule 1", "msg", "^(unclosed"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q: %q", want, stderr.String())
		}
	}
}

// Half a rule is refused. A rule with no field would resolve to nothing on
// every line and discard nothing; a rule with no regex is the worse half of the
// same mistake, because the empty regex matches everything and would leave a
// live tail silently blank. Both look exactly like a rule that works.
func TestHalfWrittenNoiseRuleIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"without a field": "time: ts\nnoise:\n  - {matches: \"^Starting\"}\n",
		"without a regex": "time: ts\nnoise:\n  - {field: msg}\n",
	} {
		t.Run(name, func(t *testing.T) {
			env := testEnv(t)
			writeProfile(t, env, "half", body)

			var stdout, stderr bytes.Buffer
			code := sheeshlog.Run([]string{"--profile", "half"}, strings.NewReader(""), &stdout, &stderr, env)

			if code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if !strings.Contains(stderr.String(), "noise rule 1") {
				t.Errorf("stderr does not name the offending rule: %q", stderr.String())
			}
		})
	}
}

// One golden per timestamp format, each rendered by the built-in profile with
// no configuration at all: the claim of detection is that a common style needs
// none. Every fixture ends with a value that cannot be read as a time, so each
// golden also shows the slot left empty and the rest of the line rendered.
func TestTimestampFormatsRenderWithoutConfiguration(t *testing.T) {
	for _, format := range []string{"rfc3339", "epoch", "epochmillis"} {
		t.Run(format, func(t *testing.T) {
			golden(t, "time-"+format, []string{"--level", "trace"}, "time-"+format+".log", testEnv(t))
		})
	}
}

// Detection splits seconds from milliseconds by magnitude, so a style whose
// values fall the wrong side of that boundary needs a way to say what it meant.
// The same fixture read both ways is the whole claim: nothing about the values
// changes, only what the profile says they are.
func TestProfileTimeFormatBeatsDetection(t *testing.T) {
	const base = `
time: ts
level: level
message: msg
`
	t.Run("detected", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "t", base)
		golden(t, "time-detected", []string{"--level", "trace", "--profile", "t"}, "time-ambiguous.log", env)
	})
	t.Run("stated", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "t", base+"timeformat: epochmillis\n")
		golden(t, "time-stated", []string{"--level", "trace", "--profile", "t"}, "time-ambiguous.log", env)
	})
	// A stated format is matched the way a level name or a colour mode is, so
	// the whole CLI answers the same way to shouting.
	t.Run("stated in capitals", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "t", base+"timeformat: EpochMillis\n")
		golden(t, "time-stated", []string{"--level", "trace", "--profile", "t"}, "time-ambiguous.log", env)
	})
}

// Stating how to read a value the profile never names does nothing at all, and
// reads like a profile that has thought about its timestamps.
func TestTimeFormatWithoutATimeFieldIsRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "orphan", "level: level\ntimeformat: epochmillis\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "orphan"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "orphan.yaml") || !strings.Contains(stderr.String(), "no time field") {
		t.Errorf("stderr does not say which file states a format nothing reads: %q", stderr.String())
	}
}

// The format is declared, not sniffed, and omitting it means the format the
// tool was built for: a profile written before the key existed reads its stream
// exactly as it did.
func TestFormatDefaultsToJSONAndIsStatable(t *testing.T) {
	env := testEnv(t)
	const line = `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi"}` + "\n"
	const base = "time: ts\nlevel: level\nmessage: msg\n"

	run := func(t *testing.T, body string) string {
		t.Helper()
		writeProfile(t, env, "f", body)
		var stdout, stderr bytes.Buffer
		if code := sheeshlog.Run([]string{"--profile", "f"}, strings.NewReader(line), &stdout, &stderr, env); code != 0 {
			t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
		}
		return stdout.String()
	}

	want := "05:03:12 I -  hi\n"
	if got := run(t, base); got != want {
		t.Errorf("with no format key: %q, want %q", got, want)
	}
	if got := run(t, base+"format: json\n"); got != want {
		t.Errorf("with format: json: %q, want %q", got, want)
	}
	// Matched case-insensitively, the way every other name in a profile is.
	if got := run(t, base+"format: JSON\n"); got != want {
		t.Errorf("with format: JSON: %q, want %q", got, want)
	}
}

// A format sheesh does not read would pass every line of the stream through
// verbatim, which looks exactly like a stream sheesh cannot help with. So it is
// refused by name at load, listing what was expected, as an unknown time format
// already is.
func TestUnknownFormatIsRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "typo", "time: ts\nformat: jsonl\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "typo"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	for _, want := range []string{"typo.yaml", "jsonl", "json"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q: %q", want, stderr.String())
		}
	}
}

// A profile stating a format has a reason to, so a typo in the name is refused
// by name rather than quietly falling back to detection — which would look like
// a working profile right up until a timestamp was ambiguous.
func TestUnknownTimeFormatIsRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "typo", "time: ts\ntimeformat: rfc339\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "typo"}, strings.NewReader(""), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	for _, want := range []string{"typo.yaml", "rfc339", "epochmillis"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not mention %q: %q", want, stderr.String())
		}
	}
}

// writeCurrent puts remembered state where the tool will look for it. The path
// is spelled out rather than borrowed from the state package for the same
// reason writeProfile spells its own out: it is a file a user may edit, so
// relocating it should break a test.
func writeCurrent(t *testing.T, env sheeshlog.Env, body string) {
	t.Helper()
	dir := filepath.Join(env.ConfigRoot, "sheesh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "current"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// readCurrent returns the remembered state verbatim, or "" when there is none.
func readCurrent(t *testing.T, env sheeshlog.Env) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(env.ConfigRoot, "sheesh", "current"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

// run is the short form for the tests that assert on the pieces rather than
// diff a whole rendering.
func run(t *testing.T, argv []string, stdin string, env sheeshlog.Env) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run(argv, strings.NewReader(stdin), &stdout, &stderr, env)
	return code, stdout.String(), stderr.String()
}

const minimalProfile = "time: ts\nlevel: level\nmessage: msg\n"

// `use` is the scriptable half of the picker: it records and returns, with no
// prompt to hang a dotfile or a CI step.
func TestUseRecordsTheProfileWithoutPrompting(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", minimalProfile)

	golden(t, "use", []string{"use", "operator"}, "basic.log", env)

	if got := readCurrent(t, env); got != "operator" {
		t.Errorf("remembered profile = %q, want %q", got, "operator")
	}
}

// The point of remembering: the day-to-day pipe carries no flags and renders
// exactly as the explicit --profile did. Asserted against the flag rather than
// a golden of its own, because "the same" is the whole claim.
func TestPipeRendersWithTheRememberedProfile(t *testing.T) {
	// The shipped example, written the way a first run writes it, so this
	// asserts on the profile a user actually has rather than on a fixture.
	env := firstRunEnv(t)
	if _, err := profile.WriteExample(env.ConfigRoot); err != nil {
		t.Fatal(err)
	}

	fixture, err := os.ReadFile(filepath.Join("testdata", "fixtures", "operator.log"))
	if err != nil {
		t.Fatal(err)
	}

	_, flagged, _ := run(t, []string{"--level", "trace", "--profile", "operator"}, string(fixture), env)
	if _, _, stderr := run(t, []string{"use", "operator"}, "", env); stderr != "" {
		t.Fatalf("use wrote to stderr: %q", stderr)
	}
	code, remembered, stderr := run(t, []string{"--level", "trace"}, string(fixture), env)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if remembered != flagged {
		t.Errorf("remembered profile rendered differently from --profile\n--- remembered ---\n%s\n--- --profile ---\n%s", remembered, flagged)
	}
	if flagged == "" {
		t.Fatal("neither run rendered anything, so the comparison proves nothing")
	}
}

// A one-off inspection must not become the new default: --profile renders this
// run and leaves what is remembered exactly as it was.
func TestProfileFlagLeavesTheRememberedProfileUnchanged(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "remembered", minimalProfile)
	writeProfile(t, env, "oneshot", "time: ts\nlevel: level\nmessage: detail\n")
	writeCurrent(t, env, "remembered\n")

	const line = `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"from msg","detail":"from detail"}` + "\n"
	code, stdout, stderr := run(t, []string{"--profile", "oneshot"}, line, env)

	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "from detail") {
		t.Errorf("the run did not use --profile: %q", stdout)
	}
	if got := readCurrent(t, env); got != "remembered" {
		t.Errorf("remembered profile = %q, want it untouched at %q", got, "remembered")
	}
}

// `list` is the non-interactive half of the picker, so it prints to stdout and
// never prompts. The current one is marked, because that is the thing a human
// running list most wants to know.
func TestListPrintsProfileNames(t *testing.T) {
	newEnv := func(t *testing.T) sheeshlog.Env {
		env := testEnv(t)
		for _, name := range []string{"operator", "backup", "gateway"} {
			writeProfile(t, env, name, minimalProfile)
		}
		return env
	}

	t.Run("with a current profile", func(t *testing.T) {
		env := newEnv(t)
		writeCurrent(t, env, "operator\n")
		golden(t, "list", []string{"list"}, "basic.log", env)
	})

	t.Run("with nothing remembered", func(t *testing.T) {
		golden(t, "list-no-current", []string{"list"}, "basic.log", newEnv(t))
	})
}

// No profiles yet is not a failure: an empty list is the honest answer, and a
// script looping over it does nothing rather than breaking.
func TestListWithNoProfilesPrintsNothing(t *testing.T) {
	code, stdout, stderr := run(t, []string{"list"}, "", testEnv(t))
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

// A typo in `use` must fail loudly: recording it would move the failure to
// every later pipe, far from the mistake.
func TestUseUnknownProfileFails(t *testing.T) {
	env := testEnv(t)
	code, stdout, stderr := run(t, []string{"use", "nope"}, "", env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, `"nope"`) {
		t.Errorf("stderr does not name the profile: %q", stderr)
	}
	if got := readCurrent(t, env); got != "" {
		t.Errorf("remembered profile = %q, want nothing recorded", got)
	}
}

// A profile that cannot be read is not one to remember either: the error
// belongs at `use`, not at the next pipe.
func TestUseMalformedProfileIsRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "broken", "time: ts\nmesage: msg\n")

	code, _, stderr := run(t, []string{"use", "broken"}, "", env)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "broken.yaml") {
		t.Errorf("stderr does not name the file: %q", stderr)
	}
	if got := readCurrent(t, env); got != "" {
		t.Errorf("remembered profile = %q, want nothing recorded", got)
	}
}

// Both halves of "exactly one name" are mistakes worth stopping for: no name is
// an unset shell variable, and two is an unquoted one.
func TestUseWantsExactlyOneName(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", minimalProfile)

	for _, argv := range [][]string{{"use"}, {"use", "operator", "backup"}} {
		code, _, stderr := run(t, argv, "", env)
		if code == 0 {
			t.Errorf("%v exited 0, want a refusal", argv)
		}
		if !strings.Contains(stderr, "use") {
			t.Errorf("%v: stderr = %q, want it to name the command", argv, stderr)
		}
	}
	if got := readCurrent(t, env); got != "" {
		t.Errorf("remembered profile = %q, want nothing recorded", got)
	}
}

// A mistyped subcommand must not be read as anything else. Rendering instead
// would silently swallow the argument.
func TestUnknownSubcommandFails(t *testing.T) {
	code, stdout, stderr := run(t, []string{"uses", "operator"}, "", testEnv(t))
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "uses") {
		t.Errorf("stderr does not name the command: %q", stderr)
	}
}

// A bare profile name is deliberately not a command: it would collide with the
// subcommand names, so `sheesh backup` has to fail rather than guess.
func TestBarePositionalProfileNameIsNotAccepted(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "backup", minimalProfile)

	code, _, stderr := run(t, []string{"backup"}, "", env)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "backup") {
		t.Errorf("stderr = %q", stderr)
	}
}

// Flags are parsed before the subcommand, so one written after it is a mistake
// worth naming rather than a profile called "--level".
func TestFlagsAfterASubcommandAreRefused(t *testing.T) {
	code, _, stderr := run(t, []string{"list", "--level", "debug"}, "", testEnv(t))
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "--level") {
		t.Errorf("stderr does not name the argument: %q", stderr)
	}
}

// Remembered state is the tool's own file, so a broken one is the tool's
// problem and not the user's: a live tail keeps rendering with the built-in
// profile rather than stopping. Pinned to the no-state golden, because
// "degrades to the default" means byte-identical to having no state at all.
// State that is corrupt names no profile at all, so there is nothing to report
// and the fallback is silent.
func TestCorruptRememberedStateDegradesToTheDefault(t *testing.T) {
	env := testEnv(t)
	writeCurrent(t, env, "not a profile name/../\n")
	golden(t, "basic", []string{"--level", "trace"}, "basic.log", env)
}

// A remembered profile that cannot be read at all — deleted since, or a
// directory, or behind a permission the tool does not have — degrades the same
// way, but names itself on stderr first. Silence here would leave a chmod
// accident or a deleted profile looking like a working setup, and the note is
// written once rather than per line.
func TestRememberedProfileThatCannotBeReadDegradesWithANote(t *testing.T) {
	const line = `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi","name":"backup-1"}` + "\n"

	// The reference: the same input with nothing remembered at all. The claim
	// is that stdout is identical, not merely non-empty.
	_, want, _ := run(t, nil, line, testEnv(t))

	t.Run("deleted since it was remembered", func(t *testing.T) {
		env := testEnv(t)
		writeCurrent(t, env, "ghost\n")
		assertDegraded(t, env, line, want, "ghost")
	})

	// A directory where the profile file should be is the portable stand-in for
	// every OS-level read failure: no chmod, and root cannot ignore it.
	t.Run("unreadable", func(t *testing.T) {
		env := testEnv(t)
		dir := filepath.Join(env.ConfigRoot, "sheesh", "profiles", "blocked.yaml")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeCurrent(t, env, "blocked\n")
		assertDegraded(t, env, line, want, "blocked")
	})
}

// assertDegraded requires a clean render of want plus a stderr note naming the
// profile and saying what it fell back to.
func assertDegraded(t *testing.T, env sheeshlog.Env, stdin, want, name string) {
	t.Helper()
	code, stdout, stderr := run(t, nil, stdin, env)

	if code != 0 {
		t.Errorf("exit code = %d, want 0: an unusable remembered profile must not stop the stream", code)
	}
	if stdout != want {
		t.Errorf("stdout = %q, want the built-in profile's rendering %q", stdout, want)
	}
	if !strings.Contains(stderr, name) {
		t.Errorf("stderr does not name the profile it could not read: %q", stderr)
	}
	if !strings.Contains(stderr, "built-in") {
		t.Errorf("stderr does not say what it fell back to: %q", stderr)
	}
}

// A one-shot --profile keeps failing loudly whatever the reason: it was asked
// for by name, so falling back would render the wrong thing under the name the
// user typed.
func TestUnreadableProfileFlagStillFailsLoudly(t *testing.T) {
	env := testEnv(t)
	dir := filepath.Join(env.ConfigRoot, "sheesh", "profiles", "blocked.yaml")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, []string{"--profile", "blocked"}, "", env)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "blocked") {
		t.Errorf("stderr does not name the profile: %q", stderr)
	}
}

// Only the files Load could actually read are profiles. Offering a name that
// cannot be used is worse than not listing it: the user picks it and the pick
// fails.
func TestListSkipsEntriesThatAreNotProfileFiles(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "real", minimalProfile)

	dir := filepath.Join(env.ConfigRoot, "sheesh", "profiles")
	if err := os.MkdirAll(filepath.Join(dir, "adirectory.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink is lstat'd by ReadDir, so a link to a directory looks like a
	// regular file unless it is followed.
	if err := os.Symlink(filepath.Join(dir, "adirectory.yaml"), filepath.Join(dir, "linked.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"README.md", "notes.txt", "real.yaml.bak"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	code, stdout, stderr := run(t, []string{"list"}, "", env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if stdout != "real\n" {
		t.Errorf("stdout = %q, want only the one real profile", stdout)
	}
}

// A flag a subcommand cannot honour is the silent no-op this codebase refuses
// everywhere else: `--profile other use operator` would look like it had done
// something with `other`.
func TestFlagsASubcommandCannotHonourAreRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", minimalProfile)

	for _, tc := range []struct {
		argv []string
		flag string
	}{
		{[]string{"--profile", "other", "use", "operator"}, "-profile"},
		{[]string{"--level", "debug", "list"}, "-level"},
		{[]string{"--color", "always", "list"}, "-color"},
	} {
		code, _, stderr := run(t, tc.argv, "", env)
		if code == 0 {
			t.Errorf("%v exited 0, want a refusal", tc.argv)
		}
		if !strings.Contains(stderr, tc.flag) {
			t.Errorf("%v: stderr does not name the flag: %q", tc.argv, stderr)
		}
	}
	if got := readCurrent(t, env); got != "" {
		t.Errorf("remembered profile = %q, want nothing recorded", got)
	}
}

// The render flags are validated for the run that renders, not for the ones
// that do not: a stale LEVEL exported in a shell profile must not stop the very
// commands you would run to work out where you stand.
func TestSubcommandsRunDespiteAnUnusableRenderFlag(t *testing.T) {
	env := testEnv(t)
	env.Vars["LEVEL"] = "verbose"
	writeProfile(t, env, "operator", minimalProfile)

	code, stdout, stderr := run(t, []string{"list"}, "", env)
	if code != 0 {
		t.Errorf("exit code = %d, stderr = %q", code, stderr)
	}
	if stdout != "operator\n" {
		t.Errorf("stdout = %q, want the profile list", stdout)
	}

	// And the same bad value still stops the run that would have rendered with
	// it, rather than being quietly forgiven everywhere.
	if code, _, _ := run(t, nil, "", env); code == 0 {
		t.Error("rendering with an unusable LEVEL exited 0, want a refusal")
	}
}

// A remembered profile that exists but is broken is a file the user has to go
// and fix, so it fails loudly naming the file — the same treatment --profile
// gives it. Degrading here would hide a typo behind a rendering that looks
// almost right.
func TestMalformedRememberedProfileFailsLoudly(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "broken", "time: ts\nmesage: msg\n")
	writeCurrent(t, env, "broken\n")

	code, stdout, stderr := run(t, []string{"--level", "trace"}, `{"ts":"2026-08-21T05:03:12Z","msg":"hi"}`+"\n", env)
	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "broken.yaml") {
		t.Errorf("stderr does not name the file: %q", stderr)
	}
}

// Help is the only place the commands are documented, so a new command that
// nobody can discover is only half added.
func TestHelpDescribesTheCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := sheeshlog.Run([]string{"-h"}, strings.NewReader(""), &stdout, &stderr, testEnv(t)); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"use", "list"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help does not mention %q:\n%s", want, stdout.String())
		}
	}
}

// The picker is the invocation the tool exists for, so its whole output is
// pinned rather than sampled: the numbering, the marker on the current
// profile and the prompt are the interface. It is asserted inline rather than
// against a golden file because the keystroke driving it has to be readable
// next to the output it produced.
func TestPickerListsProfilesAndRecordsThePick(t *testing.T) {
	env := testEnv(t)
	env.StdinIsTerminal = true
	writeProfile(t, env, "backup", minimalProfile)
	writeProfile(t, env, "operator", minimalProfile)
	writeCurrent(t, env, "operator\n")

	code, stdout, stderr := run(t, nil, "1\n", env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	// The list and the prompt are what the user is asked, so they stay visible
	// even when stdout is redirected; the confirmation is what happened, and
	// goes where `use` puts the same sentence.
	if want := "1) backup\n2) operator (current)\nprofile [1-2]: "; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if want := "now using profile \"backup\"\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if got := readCurrent(t, env); got != "backup" {
		t.Errorf("remembered profile = %q, want %q", got, "backup")
	}
}

// The dispatch the whole ticket turns on: the same bare invocation renders
// when stdin is not a terminal, and must not write a prompt into the stream a
// pipe is reading.
func TestBarePipeRendersAndNeverPrompts(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", minimalProfile)
	writeCurrent(t, env, "operator\n")

	code, stdout, stderr := run(t, nil, `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi"}`+"\n", env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "profile [") || strings.Contains(stderr, "profile [") {
		t.Errorf("a pipe was prompted:\nstdout %q\nstderr %q", stdout, stderr)
	}
	if !strings.Contains(stdout, "hi") {
		t.Errorf("stdout = %q, want the rendered line", stdout)
	}
}

// A mistyped selection at an interactive prompt costs a keystroke, not a
// re-run. The complaint goes to stderr and the prompt comes back.
func TestPickerRepromptsOnAnUnusableSelection(t *testing.T) {
	for _, tc := range []struct{ name, typed string }{
		{"out of range", "9\n"},
		{"zero", "0\n"},
		{"negative", "-1\n"},
		{"not a number", "operator\n"},
		{"empty line", "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A config root per subtest: a shared one would still hold the
			// previous subtest's pick, and every assertion that the second
			// pick was recorded would pass without recording anything.
			env := testEnv(t)
			env.StdinIsTerminal = true
			writeProfile(t, env, "backup", minimalProfile)
			writeProfile(t, env, "operator", minimalProfile)

			code, _, stderr := run(t, nil, tc.typed+"2\n", env)
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %q", code, stderr)
			}
			if strings.Count(stderr, "profile [1-2]: ") != 2 {
				t.Errorf("stderr does not show a second prompt: %q", stderr)
			}
			if !strings.Contains(stderr, "is not one of") {
				t.Errorf("stderr does not complain about the selection: %q", stderr)
			}
			if got := readCurrent(t, env); got != "operator" {
				t.Errorf("remembered profile = %q, want the second pick %q", got, "operator")
			}
		})
	}
}

// Cancelling is the gesture that has to be safe: whatever was remembered is
// still remembered, and the exit code says the pick did not happen.
func TestPickerCancelledLeavesTheRememberedProfileAlone(t *testing.T) {
	env := testEnv(t)
	env.StdinIsTerminal = true
	writeProfile(t, env, "backup", minimalProfile)
	writeProfile(t, env, "operator", minimalProfile)
	writeCurrent(t, env, "operator\n")

	for _, tc := range []struct{ name, typed string }{
		{"nothing typed", ""},
		{"an unusable selection and then nothing", "9\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := run(t, nil, tc.typed, env)
			if code == 0 {
				t.Error("exit code = 0, want non-zero: nothing was picked")
			}
			if got := readCurrent(t, env); got != "operator" {
				t.Errorf("remembered profile = %q, want it unchanged at %q", got, "operator")
			}
		})
	}
}

// Nothing to pick from is not a prompt with no options: it names the directory
// a profile would go in, and fails, so a script that reaches the picker by
// accident does not look like it succeeded.
func TestPickerWithNoProfilesNamesTheDirectory(t *testing.T) {
	// testEnv's profiles directory exists and is empty, which is the state a
	// user reaches by deleting the example: the first run is over, so nothing
	// is written back, and there is genuinely nothing to pick.
	env := testEnv(t)
	env.StdinIsTerminal = true

	code, stdout, stderr := run(t, nil, "1\n", env)
	if code == 0 {
		t.Error("exit code = 0, want non-zero")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, filepath.Join(env.ConfigRoot, "sheesh", "profiles")) {
		t.Errorf("stderr does not name the profiles directory: %q", stderr)
	}
	if got := readCurrent(t, env); got != "" {
		t.Errorf("remembered profile = %q, want nothing recorded", got)
	}
}

// The render flags are refused here for the same reason the subcommands refuse
// them: --level on a picker would look like it had done something.
func TestPickerRefusesTheRenderFlags(t *testing.T) {
	env := testEnv(t)
	env.StdinIsTerminal = true
	writeProfile(t, env, "operator", minimalProfile)

	code, _, stderr := run(t, []string{"--level", "debug"}, "1\n", env)
	if code == 0 {
		t.Error("exit code = 0, want a refusal")
	}
	if !strings.Contains(stderr, "-level") {
		t.Errorf("stderr does not name the flag: %q", stderr)
	}
	if got := readCurrent(t, env); got != "" {
		t.Errorf("remembered profile = %q, want nothing recorded", got)
	}
}

// The one invocation the picker must not swallow: --help is how help is asked
// for, and it is asked for at a terminal, which is exactly where the picker
// lives.
func TestHelpAtATerminalIsHelpNotThePicker(t *testing.T) {
	env := testEnv(t)
	env.StdinIsTerminal = true
	writeProfile(t, env, "operator", minimalProfile)

	code, stdout, stderr := run(t, []string{"-h"}, "", env)
	if code != 0 {
		t.Errorf("exit code = %d, stderr = %q", code, stderr)
	}
	if strings.Contains(stdout, "profile [") {
		t.Errorf("stdout is a picker, want the usage text: %q", stdout)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("stdout does not look like usage: %q", stdout)
	}
}

// A profile file can be listed and still be unreadable: List offers everything
// shaped like a profile, and a YAML typo only surfaces when it is parsed. The
// pick has to fail here, where the user can see which profile is broken, rather
// than on every pipe from now on.
func TestPickerRefusesAProfileThatCannotBeLoaded(t *testing.T) {
	env := testEnv(t)
	env.StdinIsTerminal = true
	writeProfile(t, env, "broken", "time: ts\nmesage: msg\n")
	writeProfile(t, env, "operator", minimalProfile)

	code, _, stderr := run(t, nil, "1\n2\n", env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stderr, "broken.yaml") {
		t.Errorf("stderr does not name the broken file: %q", stderr)
	}
	if got := readCurrent(t, env); got != "operator" {
		t.Errorf("remembered profile = %q, want the second, working pick %q", got, "operator")
	}
}

// The first minute on a new machine: nothing configured, and a pipe still
// renders. Pinned against the committed golden for the same input, so the claim
// is that an empty config renders exactly as a configured one does with the
// built-in profile, not merely that something came out.
//
// The note is asserted rather than pinned because it carries the temporary
// directory, which no golden file could hold. It goes to stderr, so a first
// run's stdout is still only the log.
func TestFirstRunRendersAndWritesTheExample(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "golden", "basic.txt"))
	if err != nil {
		t.Fatal(err)
	}

	env := firstRunEnv(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "fixtures", "basic.log"))
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, []string{"--level", "trace"}, string(fixture), env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if stdout != string(want) {
		t.Errorf("stdout differs from testdata/golden/basic.txt\n--- got ---\n%s\n--- want ---\n%s", stdout, want)
	}
	if !strings.Contains(stderr, filepath.Join(env.ConfigRoot, "sheesh", "profiles", "operator.yaml")) {
		t.Errorf("stderr does not name the example it wrote: %q", stderr)
	}
}

// With nothing configured there is nothing to name a profile, so the built-in
// one has to carry the run on its own. Asserted against the same input rendered
// with an equivalent profile file, because "the same" is the whole claim.
func TestNoProfilesRendersViaTheBuiltInProfile(t *testing.T) {
	const line = `{"ts":"2026-08-21T05:03:12Z","level":"warn","msg":"hi","name":"backup-1"}` + "\n"

	env := testEnv(t)
	writeProfile(t, env, "generic", "time: ts\nlevel: level\nmessage: msg\n")
	_, want, _ := run(t, []string{"--profile", "generic"}, line, env)

	code, got, stderr := run(t, nil, line, testEnv(t))
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if got != want {
		t.Errorf("stdout = %q, want the built-in profile's rendering %q", got, want)
	}
}

// The example has to be a profile, not a document about profiles: it is written
// where profiles live, so it shows up in the list and can be picked.
func TestTheExampleIsAProfileTheToolCanUse(t *testing.T) {
	env := firstRunEnv(t)

	code, stdout, stderr := run(t, []string{"list"}, "", env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if stdout != "operator\n" {
		t.Errorf("stdout = %q, want the example profile listed", stdout)
	}
	if !strings.Contains(stderr, "operator.yaml") {
		t.Errorf("stderr does not name the file it wrote: %q", stderr)
	}

	// And it loads: `use` refuses a profile it cannot read, so this failing
	// would mean the tool had written itself a file it cannot use.
	if code, _, stderr := run(t, []string{"use", "operator"}, "", env); code != 0 {
		t.Errorf("use operator exited %d: %q", code, stderr)
	}
}

// The user's own file is never at risk. The example is written once, on the run
// that finds no profiles directory at all; after that the directory is theirs.
func TestTheExampleIsWrittenOnceAndNeverOverwrites(t *testing.T) {
	env := firstRunEnv(t)
	path := filepath.Join(env.ConfigRoot, "sheesh", "profiles", "operator.yaml")

	if code, _, stderr := run(t, []string{"list"}, "", env); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}

	t.Run("an edited example is left alone", func(t *testing.T) {
		const mine = "time: when\nlevel: severity\nmessage: text\n"
		if err := os.WriteFile(path, []byte(mine), 0o644); err != nil {
			t.Fatal(err)
		}

		code, _, stderr := run(t, []string{"list"}, "", env)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if strings.Contains(stderr, "operator.yaml") {
			t.Errorf("a later run announced writing the example again: %q", stderr)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != mine {
			t.Errorf("the example was overwritten:\n%s", b)
		}
	})

	// Deleting it is a decision, not damage to repair: a file that comes back
	// on the next run cannot be got rid of.
	t.Run("a deleted example stays deleted", func(t *testing.T) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := run(t, []string{"list"}, "", env)
		if code != 0 {
			t.Fatalf("exit code = %d, stderr = %q", code, stderr)
		}
		if stdout != "" {
			t.Errorf("stdout = %q, want nothing: the example was deleted", stdout)
		}
	})
}

// Writing the example is a courtesy, so it must never be the reason a pipe
// fails. A config root that cannot be created is the portable stand-in for
// every reason the write might not work.
func TestAnUnwritableConfigRootStillRenders(t *testing.T) {
	env := firstRunEnv(t)
	// A regular file where the config root should be: nothing can be created
	// underneath it, and root cannot ignore that either.
	blocked := filepath.Join(env.ConfigRoot, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	env.ConfigRoot = blocked

	const line = `{"ts":"2026-08-21T05:03:12Z","level":"warn","msg":"hi"}` + "\n"
	code, stdout, _ := run(t, nil, line, env)
	if code != 0 {
		t.Errorf("exit code = %d, want 0: the example is a courtesy, not a requirement", code)
	}
	if !strings.Contains(stdout, "hi") {
		t.Errorf("stdout = %q, want the rendered line", stdout)
	}
}

// A machine with neither HOME nor XDG_CONFIG_HOME leaves the config root
// empty, which main.go allows on purpose. The profiles path is then relative,
// so writing the example would put it in whatever directory the user is
// standing in — very likely the repository they are tailing logs from.
func TestAnEmptyConfigRootWritesNothingWhereTheUserIsStanding(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	env := sheeshlog.Env{Vars: map[string]string{}}
	const line = `{"ts":"2026-08-21T05:03:12Z","level":"warn","msg":"hi"}` + "\n"

	code, stdout, stderr := run(t, nil, line, env)
	if code != 0 {
		t.Errorf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "hi") {
		t.Errorf("stdout = %q, want the rendered line", stdout)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the working directory was written to: %v", entries)
	}
}

// check is what makes a hand-written profile debuggable, so the report is
// pinned whole. The same sample is read by a healthy profile and by two broken
// ones, because the report is only useful if the difference between them is
// visible at a glance.
func TestCheckReportsWhatAProfileDidToASample(t *testing.T) {
	t.Run("a healthy profile", func(t *testing.T) {
		env := firstRunEnv(t)
		if _, err := profile.WriteExample(env.ConfigRoot); err != nil {
			t.Fatal(err)
		}
		golden(t, "check-healthy", []string{"check", "operator"}, "operator.log", env)
	})

	// A path that names no field leaves its slot empty on every line. The
	// report says so as a zero next to the path, which is the whole point:
	// rendering it would just show a gap.
	t.Run("a wrong path", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "typo", "time: ts\nlevel: level\nmessage: message\nsubject:\n  - Backup.title\n")
		golden(t, "check-wrong-path", []string{"check", "typo"}, "operator.log", env)
	})

	// An over-broad rule swallows the lines you wanted. The count next to the
	// rule is what shows it, and the summary says nothing came out.
	t.Run("an over-broad noise rule", func(t *testing.T) {
		env := testEnv(t)
		writeProfile(t, env, "greedy", "time: ts\nlevel: level\nmessage: msg\nnoise:\n  - {field: msg, matches: \"a\"}\n")
		golden(t, "check-greedy", []string{"check", "greedy"}, "operator.log", env)
	})
}

// The threshold is part of what a render would do, so check reports against the
// one asked for and names it, rather than always answering for info.
func TestCheckHonoursTheLevelThreshold(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "plain", minimalProfile)

	fixture, err := os.ReadFile(filepath.Join("testdata", "fixtures", "operator.log"))
	if err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := run(t, []string{"--level", "debug", "check", "plain"}, string(fixture), env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr)
	}
	if !strings.Contains(stdout, "threshold debug") {
		t.Errorf("the report does not name the threshold it counted against:\n%s", stdout)
	}
	// The debug line the default threshold would have dropped is now kept.
	if !strings.Contains(stdout, "0 below debug") {
		t.Errorf("the report still counts against info:\n%s", stdout)
	}
}

// check reads a sample and reports on it; it must never render it, or the
// report would arrive mixed into the log it is about.
func TestCheckRendersNothing(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "plain", minimalProfile)

	_, stdout, _ := run(t, []string{"check", "plain"}, `{"ts":"2026-08-21T05:03:12Z","level":"warn","msg":"a distinctive message"}`+"\n", env)
	if strings.Contains(stdout, "a distinctive message") {
		t.Errorf("check rendered the sample:\n%s", stdout)
	}
}

// The flags check does not honour are refused as they are everywhere else, and
// an unknown profile fails the same way `use` makes it fail.
func TestCheckRefusesWhatItCannotDo(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "plain", minimalProfile)

	for _, tc := range []struct {
		name, want string
		argv       []string
	}{
		{"a flag it cannot honour", "-color", []string{"--color", "always", "check", "plain"}},
		{"an unknown profile", "ghost", []string{"check", "ghost"}},
		{"no profile named", "one profile name", []string{"check"}},
		{"two profiles named", "one profile name", []string{"check", "plain", "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tc.argv, "", env)
			if code == 0 {
				t.Errorf("exit code = 0, want a refusal")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tc.want)
			}
		})
	}
}

// A line that is not JSON is written out verbatim by the renderer, so a report
// that counted it as dropped would say the opposite of what happens. The claim
// is checked against the renderer itself rather than against a number somebody
// typed into a test.
func TestCheckCountsPassedThroughLinesAsOutput(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "plain", minimalProfile)
	const sample = "not json at all\nalso not json\n"

	_, rendered, _ := run(t, []string{"--profile", "plain"}, sample, env)
	if rendered != sample {
		t.Fatalf("the renderer did not pass these through verbatim: %q", rendered)
	}

	_, report, _ := run(t, []string{"check", "plain"}, sample, env)
	if !strings.Contains(report, "2 passed through") {
		t.Errorf("the report does not count the passed-through lines:\n%s", report)
	}
	if strings.Contains(report, "every line was dropped\n") {
		t.Errorf("the report claims lines were dropped that the renderer prints:\n%s", report)
	}
}

// A correct path with the wrong timeformat leaves exactly the same empty slot
// as a misspelled path, and they are fixed on different lines of the profile.
func TestCheckTellsAWrongTimeFormatFromAWrongPath(t *testing.T) {
	env := testEnv(t)
	// The value is a time, but not one epoch milliseconds can be read from.
	writeProfile(t, env, "misformatted", "time: ts\nlevel: level\nmessage: msg\ntimeformat: epochmillis\n")
	writeProfile(t, env, "mispathed", "time: when\nlevel: level\nmessage: msg\n")
	const sample = `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi"}` + "\n"

	_, misformatted, _ := run(t, []string{"check", "misformatted"}, sample, env)
	if !strings.Contains(misformatted, "not read as a time") {
		t.Errorf("the report does not point at the timeformat:\n%s", misformatted)
	}

	_, mispathed, _ := run(t, []string{"check", "mispathed"}, sample, env)
	if strings.Contains(mispathed, "not read as a time") {
		t.Errorf("a path that found nothing is reported as a format problem:\n%s", mispathed)
	}
	if !strings.Contains(mispathed, "empty") {
		t.Errorf("the report does not say the slot was empty:\n%s", mispathed)
	}
}

// Nobody types a log sample at a prompt, so a terminal on stdin is a forgotten
// redirect. Sitting there reading it looks like a hang.
func TestCheckAtATerminalSaysItWantsASample(t *testing.T) {
	env := testEnv(t)
	env.StdinIsTerminal = true
	writeProfile(t, env, "plain", minimalProfile)

	code, stdout, stderr := run(t, []string{"check", "plain"}, "", env)
	if code == 0 {
		t.Error("exit code = 0, want a refusal")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "stdin") {
		t.Errorf("stderr does not say where the sample goes: %q", stderr)
	}
}

// A misspelled command is a misspelled command, whatever flags came with it.
// Told that -level does not apply to `chekc`, a user goes and looks at the flag.
func TestAnUnknownCommandIsReportedAsOneEvenWithFlags(t *testing.T) {
	code, _, stderr := run(t, []string{"--level", "debug", "chekc", "plain"}, "", testEnv(t))
	if code == 0 {
		t.Error("exit code = 0, want a refusal")
	}
	if !strings.Contains(stderr, "unknown command") {
		t.Errorf("stderr blames something other than the command name: %q", stderr)
	}
}

// A sample that fails to read part way is not a sample that ended: reporting it
// as complete would send the user off to fix a profile that is fine.
func TestCheckReportsAFailedRead(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "plain", minimalProfile)

	var stdout, stderr bytes.Buffer
	stdin := io.MultiReader(
		strings.NewReader(`{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"hi"}`+"\n"),
		&failingReader{},
	)
	code := sheeshlog.Run([]string{"check", "plain"}, stdin, &stdout, &stderr, env)

	if code == 0 {
		t.Error("exit code = 0, want non-zero")
	}
	if stdout.Len() > 0 {
		t.Errorf("stdout = %q, want no report over a sample that did not finish", stdout.String())
	}
	if !strings.Contains(stderr.String(), "reading the sample") {
		t.Errorf("stderr = %q, want it to name the failure", stderr.String())
	}
}

// failingReader is a read error that is not EOF.
type failingReader struct{}

func (*failingReader) Read([]byte) (int, error) { return 0, errors.New("disk on fire") }

// The flag package stops parsing at the first word that is not a flag, so a
// flag written after the subcommand arrives as a positional argument. Counting
// it as one sends the user looking at their profiles rather than at where they
// put the flag.
func TestFlagsWrittenAfterTheSubcommandSayTheOrderIsWrong(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", minimalProfile)

	for _, tc := range []struct {
		name string
		argv []string
		want string
	}{
		{"before the profile name", []string{"check", "-level", "debug", "operator"}, "sheesh -level debug check operator"},
		{"after the profile name", []string{"check", "operator", "-level", "debug"}, "sheesh -level debug check operator"},
		{"written with an equals", []string{"check", "operator", "--level=debug"}, "sheesh --level=debug check operator"},
		{"on a command that takes none", []string{"list", "--color", "always"}, "sheesh --color always list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := run(t, tc.argv, "", env)
			if code == 0 {
				t.Error("exit code = 0, want a refusal")
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			// The whole command, not a description of the correction: the fix
			// is an order, and an order is easier to see than to read about.
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want it to suggest %q", stderr, tc.want)
			}
		})
	}

	// A word that is no global flag has no order that would make it work, so
	// there is nothing to suggest and it is named as the unknown flag it is.
	t.Run("a flag that does not exist", func(t *testing.T) {
		code, _, stderr := run(t, []string{"use", "--force", "operator"}, "", env)
		if code == 0 {
			t.Error("exit code = 0, want a refusal")
		}
		if !strings.Contains(stderr, `unknown flag "--force"`) {
			t.Errorf("stderr = %q, want it to name the unknown flag", stderr)
		}
		if strings.Contains(stderr, "try: sheesh") {
			t.Errorf("stderr suggests a command that would not work either: %q", stderr)
		}
	})

	// And the ordinary invocations are untouched: a profile name is not a flag
	// however it is spelled.
	t.Run("a correct invocation still works", func(t *testing.T) {
		if code, _, stderr := run(t, []string{"use", "operator"}, "", env); code != 0 {
			t.Errorf("exit code = %d, stderr = %q", code, stderr)
		}
		if code, _, stderr := run(t, []string{"--level", "debug", "check", "operator"}, "", env); code != 0 {
			t.Errorf("exit code = %d, stderr = %q", code, stderr)
		}
	})
}

// Unsupported formats from real logs (anonymized) are a regression oracle, not a hope: every
// line of every family it holds — positional, syslog-like, access log, Java,
// startup prose, multiline continuation — must come out of Run exactly as it
// went in, under a profile that does not fit it at all.
//
// The zap-console and klog lines in the fixture are the reason this ticket
// exists. Both merely *end* in a JSON object, and a parser that took everything
// before the first brace as a stream prefix rendered them as their JSON context
// alone, throwing away the real timestamp, level and message.
//
// The lines are reduced from the local corpus with the addresses and the CAS
// key material replaced by stand-ins of the same shape; what is being asserted
// is the parser's behaviour on the *shape* of each family.
//
// Several of them carry `key=value` fragments in their tails — Traefik's
// `providerName=kubernetescrd`, Postfix's `commands=0/0`, klog's
// `annotationsAllowList={}` — and none of them is logfmt. Under a JSON profile
// nothing here may be tempted into reading one: the format is declared, so the
// question is never asked.
func TestUnsupportedFormatsPassThroughByteForByte(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", "time: ts\nlevel: level\nmessage: msg\n")

	path := filepath.Join("testdata", "fixtures", "passthrough.log")
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	// --level trace, so that nothing here can be explained away as filtering:
	// a passed-through line is never ranked, and this pins that too.
	code := sheeshlog.Run([]string{"--level", "trace", "--profile", "operator"}, bytes.NewReader(want), &stdout, &stderr, env)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != string(want) {
		t.Errorf("output is not %s byte for byte\n--- got ---\n%s\n--- want ---\n%s", path, stdout.String(), want)
	}
}

// The prefix belongs to the viewing tool and not to the log style, so one
// profile has to serve all of them: the same payload arrives bare from a
// single-container read, one token from k9s on a multi-container pod, and two
// from stern. Declaring the prefix instead would mean one profile per tool.
func TestSameProfileAcrossViewingTools(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", "time: ts\nlevel: level\nmessage: msg\n")

	const payload = `{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"reconciling","name":"backup-1"}`
	rendered := func(t *testing.T, line string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := sheeshlog.Run([]string{"--profile", "operator"}, strings.NewReader(line+"\n"), &stdout, &stderr, env); code != 0 {
			t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
		}
		return stdout.String()
	}

	bare := rendered(t, payload)
	if bare != "05:03:12 I -  reconciling  name=backup-1\n" {
		t.Fatalf("unprefixed rendered as %q", bare)
	}
	for _, line := range []string{
		"manager " + payload,
		"my-backup-operator-controller-manager-7c45d48bb4-cgkp2 manager " + payload,
		"[pod/my-backup-operator-7c45d48bb4-cgkp2/manager] " + payload,
	} {
		if got := rendered(t, line); got != bare {
			t.Errorf("prefixed line rendered as %q, want the unprefixed %q", got, bare)
		}
	}
}

// The committed stern sample, end to end: two tokens ahead of the payload, over
// a whole file rather than one hand-written line. Ten lines reduced from the
// corpus, covering both container names in it, a `logger` line, the nested
// Backup, Restore and BackupSchedule subjects, an extra key holding a space, a
// boolean extra, and the one line that is not a record at all.
func TestSternPrefixedJSON(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", `time: ts
level: level
message: msg
subject:
  - name
  - Backup.name
  - Restore.name
  - BackupSchedule.name
context:
  - logger
  - controller
  - controllerGroup
  - controllerKind
  - reconcileID
  - namespace
  - Backup
  - Restore
  - BackupSchedule
`)
	// --level trace, because the sample is mostly debug lines and this test is
	// about the prefix rather than the threshold.
	golden(t, "stern-prefixed-json", []string{"--level", "trace", "--profile", "operator"}, "stern-prefixed-json.log", env)
}

// logfmtProfile is the reading recipe the two goldens below share: the
// `time`/`level`/`msg` convention, which is what velero, both Prometheus
// workloads and most controller-runtime operators write. It is one string so that the render and the report are demonstrably
// about the same profile.
const logfmtProfile = `format: logfmt
time: time
level: level
message: msg
subject:
  - name
  - _prefix
context:
  - controller
  - controllerGroup
  - controllerKind
  - reconcileID
  - namespace
  - logSource
  - source
  - App
`

// A profile stating `format: logfmt` renders in the same fixed layout a JSON
// one does: the format decides how a payload is read and never how the output looks.
//
// The fixture is reduced from the corpus and covers what distinguishes one
// reading from another end to end: the one-token `velero` and `prometheus`
// stream prefixes, a quoted and an unquoted time value, a lower- and an
// upper-case level, a hyphenated key, `=` inside a quoted value, and JSON
// carried as a field's string value. Its first two lines are the prose a velero
// tail opens with, which never opens with an assignment and so passes through
// verbatim; its last line is the one synthetic entry, a record whose quoted
// value is unterminated, which fails whole rather than half-read.
func TestLogfmtProfileRendersInTheFixedLayout(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "logfmt", logfmtProfile)
	golden(t, "logfmt", []string{"--level", "trace", "--profile", "logfmt"}, "logfmt.log", env)
}

// check reports over a logfmt sample as it does over a JSON one: the same
// sections, the same counts, the passed-through lines accounted for.
func TestLogfmtCheckReports(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "logfmt", logfmtProfile)
	golden(t, "check-logfmt", []string{"--level", "trace", "check", "logfmt"}, "logfmt.log", env)
}

// The corpus uses three time/level/message conventions and no more, so a
// profile names the one its stream uses rather than being given a fallback
// list. Each is one real line from the stream that writes it.
func TestLogfmtTimeFieldConventions(t *testing.T) {
	for _, tc := range []struct{ field, line, want string }{
		{"time", `velero time="2026-09-02T11:00:44Z" level=info msg="Validating BackupStorageLocation"`, "11:00:44 I velero  Validating BackupStorageLocation\n"},
		{"ts", `ts=2026-09-02T05:18:22.255969236Z level=warn msg="memberlist fast-join finished"`, "05:18:22.255969236 W -  memberlist fast-join finished\n"},
		{"t", `grafana t=2026-09-02T05:18:40.00299328Z level=warn msg="calling resource store as the service"`, "05:18:40.00299328 W grafana  calling resource store as the service\n"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			env := testEnv(t)
			writeProfile(t, env, "s", "format: logfmt\ntime: "+tc.field+"\nlevel: level\nmessage: msg\nsubject:\n  - _prefix\n")

			var stdout, stderr bytes.Buffer
			if code := sheeshlog.Run([]string{"--profile", "s"}, strings.NewReader(tc.line+"\n"), &stdout, &stderr, env); code != 0 {
				t.Fatalf("exit code = %d, stderr: %s", code, stderr.String())
			}
			if stdout.String() != tc.want {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.want)
			}
		})
	}
}

func TestProfileForPodResolvesThroughTheCandidates(t *testing.T) {
	const operator = `
time: ts
level: level
message: msg
subject:
  - name
  - Backup.name
`
	pods := map[string]string{
		"Deployment":  "operator-7c45d48bb4-cgkp2",
		"DaemonSet":   "operator-wq7zz",
		"StatefulSet": "operator-0",
		"named pod":   "operator",
	}
	for what, pod := range pods {
		t.Run(what, func(t *testing.T) {
			env := testEnv(t)
			writeProfile(t, env, "operator", operator)
			// The same golden --profile operator pins, because resolving a
			// profile is not a different way of rendering with one.
			golden(t, "profiles-operator", []string{"--level", "trace", "--profile-for-pod", pod}, "profiles.log", env)
		})
	}
}

// The candidates run most specific first, so a profile for the workload beats
// one that happens to be named after a shorter prefix of it.
func TestProfileForPodPrefersTheLongerCandidate(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "svc-worker", "time: ts\nlevel: level\nmessage: msg\n")
	writeProfile(t, env, "svc", "time: ts\nlevel: level\nmessage: detail\n")

	var stdout, stderr bytes.Buffer
	in := strings.NewReader(`{"ts":"2026-08-21T05:03:12Z","level":"info","msg":"from the workload profile","detail":"from the prefix profile"}` + "\n")
	code := sheeshlog.Run([]string{"--profile-for-pod", "svc-worker-6d9f-abcde"}, in, &stdout, &stderr, env)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "from the workload profile") {
		t.Errorf("stdout = %q, want the svc-worker profile's message slot", stdout.String())
	}
}

func TestProfileForPodWithNoMatchPassesTheStreamThrough(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "remembered", "time: ts\nlevel: level\nmessage: msg\n")
	if code := sheeshlog.Run([]string{"use", "remembered"}, strings.NewReader(""), io.Discard, io.Discard, env); code != 0 {
		t.Fatalf("setting up the remembered profile: exit code = %d", code)
	}

	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "basic.log"))
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile-for-pod", "unheard-of-7c45d48bb4-cgkp2"}, bytes.NewReader(raw), &stdout, &stderr, env)

	if code != 0 {
		t.Errorf("exit code = %d, want 0: no profile for a pod is not a failure", code)
	}
	if stdout.String() != string(raw) {
		t.Errorf("stdout is not the input byte for byte\n--- got ---\n%s\n--- want ---\n%s", stdout.String(), raw)
	}

	// One line, on stderr, naming every candidate: stdout stays pure log, and
	// a profile installed under a slightly different name is a spelling to
	// compare rather than a mystery.
	notes := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(notes) != 1 {
		t.Errorf("stderr = %q, want exactly one line", stderr.String())
	}
	for _, want := range []string{"unheard-of-7c45d48bb4-cgkp2", "unheard-of-7c45d48bb4", "unheard-of"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not name the candidate %q: %q", want, stderr.String())
		}
	}
}

// The default threshold would drop the debug lines of a rendered stream. A
// passed-through stream has no levels to rank, so nothing is dropped: this
// pins that --level does not quietly apply to bytes nobody read.
func TestProfileForPodPassthroughIgnoresTheLevelThreshold(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "fixtures", "levels.log"))
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--level", "error", "--profile-for-pod", "nothing-here"}, bytes.NewReader(raw), &stdout, &stderr, testEnv(t))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stdout.String() != string(raw) {
		t.Errorf("stdout is not the input byte for byte\n--- got ---\n%s\n--- want ---\n%s", stdout.String(), raw)
	}
}

// Resolving a profile for one pod is a fact about this run only. A plugin fires
// on every pod a user opens, so remembering any of them would leave the next
// bare pipe rendering with whatever was looked at last.
func TestProfileForPodDoesNotChangeWhatIsRemembered(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "remembered", "time: ts\nlevel: level\nmessage: msg\n")
	writeProfile(t, env, "operator", "time: ts\nlevel: level\nmessage: msg\n")
	if code := sheeshlog.Run([]string{"use", "remembered"}, strings.NewReader(""), io.Discard, io.Discard, env); code != 0 {
		t.Fatalf("setting up the remembered profile: exit code = %d", code)
	}

	for _, pod := range []string{"operator-7c45d48bb4-cgkp2", "no-profile-for-this-one"} {
		sheeshlog.Run([]string{"--profile-for-pod", pod}, strings.NewReader(""), io.Discard, io.Discard, env)

		var stdout bytes.Buffer
		if code := sheeshlog.Run([]string{"list"}, strings.NewReader(""), &stdout, io.Discard, env); code != 0 {
			t.Fatalf("list: exit code = %d", code)
		}
		if !strings.Contains(stdout.String(), "remembered (current)") {
			t.Errorf("after --profile-for-pod %q the current profile moved:\n%s", pod, stdout.String())
		}
	}
}

// Both flags say what to render with, so one of them winning silently would
// mean a plugin that adds --profile-for-pod to a command already carrying
// --profile renders with a recipe nobody chose.
func TestProfileAndProfileForPodTogetherAreRefused(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "operator", "time: ts\nlevel: level\nmessage: msg\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile", "operator", "--profile-for-pod", "operator-0"}, strings.NewReader("{}\n"), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout.Len() > 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--profile-for-pod") {
		t.Errorf("stderr does not name the conflict: %q", stderr.String())
	}
}

func TestEmptyPodNameIsRefused(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile-for-pod", ""}, strings.NewReader("{}\n"), &stdout, &stderr, testEnv(t))

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout.Len() > 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "--profile-for-pod") {
		t.Errorf("stderr does not name the flag: %q", stderr.String())
	}
}

// A candidate whose file is there and broken is a mistake to go and fix. It is
// not stepped over, because doing so would read the stream with a shorter
// candidate's recipe while the user believes the one they wrote is in use.
func TestProfileForPodStopsAtABrokenCandidate(t *testing.T) {
	env := testEnv(t)
	writeProfile(t, env, "svc-worker", "time: ts\nsubject: [unclosed\n")
	writeProfile(t, env, "svc", "time: ts\nlevel: level\nmessage: msg\n")

	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile-for-pod", "svc-worker-6d9f-abcde"}, strings.NewReader("{}\n"), &stdout, &stderr, env)

	if code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if stdout.Len() > 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "svc-worker") {
		t.Errorf("stderr does not name the broken profile: %q", stderr.String())
	}
}

// A pod name that could never be a profile name resolves to nothing rather
// than to an error: the name arrives from whatever invoked sheesh, and
// refusing it would hide the log instead of merely not rendering it.
func TestUnusablePodNamePassesTheStreamThrough(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := sheeshlog.Run([]string{"--profile-for-pod", "../etc/passwd"}, strings.NewReader("hello\n"), &stdout, &stderr, testEnv(t))

	if code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr %q)", code, stderr.String())
	}
	if stdout.String() != "hello\n" {
		t.Errorf("stdout = %q, want the input", stdout.String())
	}
}
