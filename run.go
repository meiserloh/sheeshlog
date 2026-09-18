// Package sheeshlog renders structured log lines into a compact, human-readable
// form. Run is the single seam the whole CLI hangs off: main() is a shim, and
// the tests come through the same door a user does.
package sheeshlog

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/meiserloh/sheeshlog/internal/level"
	"github.com/meiserloh/sheeshlog/internal/logline"
	"github.com/meiserloh/sheeshlog/internal/profile"
	"github.com/meiserloh/sheeshlog/internal/render"
	"github.com/meiserloh/sheeshlog/internal/state"
)

// Version is the released version this build is, bumped by hand at tag time.
const Version = "1.0.0"

// Env carries what the process would otherwise reach for globally, so that
// nothing below this seam touches os directly and tests never read or write the
// developer's real config.
type Env struct {
	// ConfigRoot is the directory profiles and remembered state live under.
	ConfigRoot string
	// Vars holds only the environment variables the tool honours.
	Vars map[string]string
	// StdinIsTerminal decides picker-versus-render dispatch.
	StdinIsTerminal bool
	// StdoutIsTerminal decides colour.
	StdoutIsTerminal bool
}

// genericProfile is the built-in fallback: the field names most structured
// loggers use, no subject candidates, no noise rules. It keeps piping useful
// before any profile file exists.
var genericProfile = profile.Profile{
	Time:       "ts",
	Level:      "level",
	Message:    "msg",
	Format:     logline.FormatJSON,
	TimeFormat: logline.TimeAuto,
}

// Run executes the CLI and returns the process exit code.
//
// The order here is deliberate: the flags are parsed, then a subcommand runs,
// and only then are the render flags checked for usable values. A stale LEVEL
// exported in a shell profile would otherwise block the very commands a user
// would run to work out where they stand.
func Run(argv []string, stdin io.Reader, stdout, stderr io.Writer, env Env) int {
	fs, f := newFlagSet(io.Discard)
	if err := fs.Parse(argv); err != nil {
		// Help is a request, not a failure: it goes to stdout and exits clean.
		if errors.Is(err, flag.ErrHelp) {
			usage, _ := newFlagSet(stdout)
			usage.Usage()
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
		return 2
	}

	// Answered before the config directory is touched, so that --version is the
	// one question a broken or unwritable config cannot get in the way of.
	if *f.version {
		_, _ = fmt.Fprintf(stdout, "sheesh %s\n", Version)
		return 0
	}

	// Before anything reads or lists a profile, so that the first run of any
	// invocation — a pipe, `list`, the picker — is one that already has an
	// example to show. It is a courtesy and never a reason to fail: a config
	// directory that cannot be written to still renders.
	if path, err := profile.WriteExample(env.ConfigRoot); err == nil && path != "" {
		_, _ = fmt.Fprintf(stderr, "sheesh: wrote an example profile to\n  %s\n", path)
	}

	if code, handled := dispatch(fs, f, stdin, stdout, stderr, env); handled {
		return code
	}

	if env.StdinIsTerminal {
		err := refuseFlags(fs, "picker")
		if err == nil {
			err = pick(stdin, stdout, stderr, env)
		}

		if errors.Is(err, errCancelled) {
			return 1
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
			return 2
		}
		return 0
	}

	opts, err := renderOptions(fs, f, env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
		return 2
	}

	src, err := resolveProfile(opts, env)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
		return 2
	}
	// Written once, before the stream, rather than per line: the user has to
	// know the tool is not rendering with the profile they chose, but they are
	// about to read log output and this must not be part of it.
	if src.note != "" {
		_, _ = fmt.Fprintf(stderr, "sheesh: %s\n", src.note)
	}
	if src.passthrough {
		return copyStream(stdin, stdout, stderr)
	}
	return renderStream(stdin, stdout, stderr, src.profile, opts)
}

// dispatch runs the subcommand, if there is one, reporting whether it handled
// the invocation. A subcommand neither reads stdin nor renders anything, so it
// returns before the stream is touched.
//
// An unrecognised word is refused rather than read as a profile name: a bare
// positional profile name would collide with the subcommand names, so
// `sheesh backup` has to fail rather than guess which of the two it meant.
func dispatch(fs *flag.FlagSet, f flags, stdin io.Reader, stdout, stderr io.Writer, env Env) (int, bool) {
	args := fs.Args()
	if len(args) == 0 {
		return 0, false
	}
	cmd := args[0]

	allowed, known := allowedFlags[cmd]
	if !known {
		_, _ = fmt.Fprintf(stderr, "sheesh: unknown command %q (want use, list or check; a profile is chosen with use or --profile)\n", cmd)
		return 2, true
	}

	// Flags go before the subcommand
	if err := flagsAfterTheCommand(fs, cmd, args[1:]); err != nil {
		_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
		return 2, true
	}

	// check is the one subcommand --level applies to: it reports what a render
	// would do, and a render honours --level. The others render nothing, so a
	// threshold would describe nothing.
	err := refuseFlags(fs, cmd, allowed...)
	if err == nil {
		switch cmd {
		case "use":
			err = use(args[1:], stdout, env)
		case "list":
			err = list(args[1:], stdout, env)
		case "check":
			err = check(args[1:], fs, f, stdin, stdout, env)
		}
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
		return 2, true
	}
	return 0, true
}

// flagsAfterTheCommand reports a flag written after the subcommand, showing the
// whole command the user probably meant rather than describing the correction.
func flagsAfterTheCommand(fs *flag.FlagSet, cmd string, args []string) error {
	var moved, kept []string
	for i := 0; i < len(args); i++ {
		name, isFlag := flagName(args[i])
		if !isFlag {
			kept = append(kept, args[i])
			continue
		}
		if fs.Lookup(name) == nil {
			// Not one of the global flags at all, so there is no order that
			// would make it work and nothing useful to suggest.
			return fmt.Errorf("%s: unknown flag %q", cmd, args[i])
		}
		moved = append(moved, args[i])
		// Every global flag takes a value, so the word after one belongs to it
		// unless it was already written with an = or is itself a flag.
		if !strings.Contains(args[i], "=") && i+1 < len(args) {
			if _, next := flagName(args[i+1]); !next {
				i++
				moved = append(moved, args[i])
			}
		}
	}
	if len(moved) == 0 {
		return nil
	}
	rewritten := append(append(moved, cmd), kept...)
	return fmt.Errorf("%s: the global flags go before the subcommand\n  try: sheesh %s", cmd, strings.Join(rewritten, " "))
}

func flagName(arg string) (string, bool) {
	if len(arg) < 2 || arg[0] != '-' || arg == "--" {
		return "", false
	}
	name := strings.TrimLeft(arg, "-")
	name, _, _ = strings.Cut(name, "=")
	return name, name != ""
}

func refuseFlags(fs *flag.FlagSet, what string, allowed ...string) error {
	var ignored []string
	for _, name := range setFlags(fs) {
		if !slices.Contains(allowed, name) {
			ignored = append(ignored, name)
		}
	}
	if len(ignored) > 0 {
		return fmt.Errorf("%s: -%s does not apply here (the global flags are about rendering)", what, strings.Join(ignored, ", -"))
	}
	return nil
}

// allowedFlags is the subcommands, each mapped to the global flags it honours
var allowedFlags = map[string][]string{
	"use":   nil,
	"list":  nil,
	"check": {"level"},
}

// setFlags returns the names of the flags actually written on the command line,
// in declaration order, so that a message about them reads the same every run.
func setFlags(fs *flag.FlagSet) []string {
	var names []string
	fs.Visit(func(fl *flag.Flag) { names = append(names, fl.Name) })
	return names
}

// use records the named profile as the current one. The profile is loaded
// first, so that a name that cannot be read is refused here rather than
// remembered and failing on every pipe from now on — the mistake and the error
// stay in the same place.
func use(args []string, stdout io.Writer, env Env) error {
	if len(args) != 1 {
		return fmt.Errorf("use: want exactly one profile name (got %d)", len(args))
	}
	name := args[0]
	if _, err := profile.Load(env.ConfigRoot, name); err != nil {
		return err
	}
	return remember(name, stdout, env)
}

func remember(name string, stdout io.Writer, env Env) error {
	if err := state.SetCurrent(env.ConfigRoot, name); err != nil {
		return fmt.Errorf("recording profile %q: %w", name, err)
	}
	_, _ = fmt.Fprintf(stdout, "now using profile %q\n", name)
	return nil
}

// list prints the profile names, one per line, marking the current one. It goes
// to stdout and never prompts, so it is usable from a script; the marker is a
// suffix so that the names stay at the start of the line for whatever is
// reading them.
func list(args []string, stdout io.Writer, env Env) error {
	if len(args) != 0 {
		return fmt.Errorf("list: unexpected argument %q (global flags go before the subcommand)", args[0])
	}
	names, err := profile.List(env.ConfigRoot)
	if err != nil {
		return err
	}
	current := state.Current(env.ConfigRoot)
	for _, name := range names {
		if name == current {
			_, _ = fmt.Fprintf(stdout, "%s (current)\n", name)
			continue
		}
		_, _ = fmt.Fprintln(stdout, name)
	}
	return nil
}

// A source is the render source - either a profile or passthrough stream without a profile.
type source struct {
	profile     profile.Profile
	passthrough bool
	// note is said once on stderr before the stream starts.
	note string
}

// rendering is a run that reads with a profile.
func rendering(p profile.Profile, note string) source {
	return source{profile: p, note: note}
}

// passingThrough is a run that reads with no profile at all, which always has
// something to say about why.
func passingThrough(note string) source {
	return source{passthrough: true, note: note}
}

// resolveProfile picks what the run renders from: the profile installed for
// --profile-for-pod, else --profile, else the remembered profile, else the
// built-in generic one. A pod with no profile installed hands the stream over unread.
func resolveProfile(opts options, env Env) (source, error) {
	if opts.pod != "" {
		p, err := profile.ForPod(env.ConfigRoot, opts.pod)

		var none *profile.NoProfileForPodError
		if errors.As(err, &none) {
			return passingThrough(fmt.Sprintf("%v; passing the stream through unread", none)), nil
		}
		return rendering(p, ""), err
	}
	if opts.profile != "" {
		p, err := profile.Load(env.ConfigRoot, opts.profile)
		return rendering(p, ""), err
	}
	// Corrupt state names no profile at all, so it is indistinguishable from no
	// state and there is nothing to report.
	remembered := state.Current(env.ConfigRoot)
	if remembered == "" {
		return rendering(genericProfile, ""), nil
	}
	p, err := profile.Load(env.ConfigRoot, remembered)

	var notFound *profile.NotFoundError
	var unreadable *profile.UnreadableError
	if errors.As(err, &notFound) || errors.As(err, &unreadable) {
		return rendering(genericProfile, fmt.Sprintf("%v; rendering with the built-in profile", err)), nil
	}
	return rendering(p, ""), err
}

// options are the settings a single invocation runs under, resolved from flags
// and the environment before any input is read.
type options struct {
	// minLevel is the rank below which a rankable line is dropped.
	minLevel int
	// colour is decided once per run: a stream must not change its mind about
	// escape codes halfway down.
	colour bool
	// profile is the name given by --profile; empty means the remembered one.
	profile string
	// pod is the name given by --profile-for-pod, which resolves a profile of
	// its own and so leaves the remembered one out of the run entirely.
	pod string
	// levelName is minLevel as the user's own word for it, for `check` to
	// report the threshold it counted against.
	levelName string
}

// flags are the global flags' parsed destinations, bundled so that adding one
// does not widen newFlagSet's signature for every caller.
type flags struct {
	level      *string
	colour     *string
	profile    *string
	profileFor *string
	version    *bool
}

// newFlagSet declares the global flags in one place, so the usage text shown to
// a user and the parser that rejects their typo can never drift apart.
func newFlagSet(out io.Writer) (*flag.FlagSet, flags) {
	fs := flag.NewFlagSet("sheesh", flag.ContinueOnError)
	fs.SetOutput(out)
	// Written out rather than left to the flag package's default, because the
	// commands are otherwise undiscoverable: a command nobody can find is only
	// half added.
	fs.Usage = func() {
		_, _ = fmt.Fprint(out, `sheesh renders structured log lines into a compact, readable form.

Usage:
  <log source> | sheesh [flags]      render with the current profile
  <pod's log> | sheesh --profile-for-pod <pod>
                                     render by pod name, ignoring the current
                                     profile; for a log viewer such as k9s
  sheesh                             pick the current profile from a list
  sheesh use <name>                  make a profile the current one
  sheesh list                        print the profile names
  sheesh check <name> < sample.log   report what a profile does to a sample

The flags below are about rendering. use and list accept none; check accepts
-level, because it reports what a render at that threshold would do.

Flags:
`)
		fs.PrintDefaults()
	}
	// The empty default distinguishes "not given" from "given as info", so the
	// flag can beat LEVEL without the env var beating an explicit --level info.
	return fs, flags{
		level:      fs.String("level", "", "minimum level to render (trace, debug, info, warn, error)"),
		colour:     fs.String("color", "auto", "when to colour output: auto, always, never (an explicit value beats NO_COLOR)"),
		profile:    fs.String("profile", "", "profile to render this run with, leaving the current one unchanged"),
		version:    fs.Bool("version", false, "print the version and exit"),
		profileFor: fs.String("profile-for-pod", "", "resolve the profile from this pod name, ignoring the remembered profile; with no profile for the pod the stream is passed through unread"),
	}
}

// renderOptions resolves the settings the rendering run needs, rejecting any
// value it cannot use before a single line is read. It runs after the
// subcommands, so an unusable value stops only the run it would have applied to.
func renderOptions(fs *flag.FlagSet, f flags, env Env) (options, error) {
	// An explicitly empty --profile is a typo, usually an unset shell variable,
	// not a request for the built-in profile: only omitting the flag means that.
	set := setFlags(fs)
	if slices.Contains(set, "profile") && *f.profile == "" {
		return options{}, errors.New("--profile: empty profile name")
	}

	if slices.Contains(set, "profile-for-pod") && *f.profileFor == "" {
		return options{}, errors.New("--profile-for-pod: empty pod name")
	}

	if *f.profile != "" && *f.profileFor != "" {
		return options{}, errors.New("--profile and --profile-for-pod both say what to render with; use one")
	}

	colour, err := colourEnabled(*f.colour, env)
	if err != nil {
		return options{}, err
	}

	name, source := *f.level, "--level"
	if name == "" {
		name, source = env.Vars["LEVEL"], "LEVEL"
	}
	if name == "" {
		name, source = level.Default, "default"
	}
	rank, ok := level.Rank(name)
	if !ok {
		return options{}, fmt.Errorf("%s: unknown level %q (want trace, debug, info, warn or error)", source, name)
	}
	return options{minLevel: rank, colour: colour, profile: *f.profile, pod: *f.profileFor, levelName: strings.ToLower(name)}, nil
}

// colourEnabled resolves the one colour question of the run. An explicit
// --color wins outright, the same rule --level follows against LEVEL, so one
// sentence covers the whole CLI: a flag beats the environment.
func colourEnabled(mode string, env Env) (bool, error) {
	// Matched case-insensitively, the way a level name is, so that --color and
	// --level do not disagree about whether shouting is allowed.
	switch strings.ToLower(mode) {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		// NO_COLOR counts when present and non-empty, per no-color.org: an
		// empty value is not a request for plain output.
		if v, ok := env.Vars["NO_COLOR"]; ok && v != "" {
			return false, nil
		}
		// Off a terminal, escape codes would land in whatever grep or the
		// redirect is holding.
		return env.StdoutIsTerminal, nil
	default:
		return false, fmt.Errorf("--color: unknown value %q (want auto, always or never)", mode)
	}
}

// renderStream reads lines without a length cap and flushes after each one, so
// a live `-f` tail appears as it happens rather than in block-buffered clumps.
func renderStream(stdin io.Reader, stdout, stderr io.Writer, p profile.Profile, opts options) int {
	in := bufio.NewReader(stdin)
	out := bufio.NewWriter(stdout)

	for {
		raw, err := in.ReadString('\n')
		if raw != "" {
			if line, ok := renderLine(raw, p, opts); ok {
				_, _ = out.WriteString(line)
				_ = out.Flush()
			}
		}
		if err != nil {
			// Only EOF is a clean end of stream. A real read error must not
			// exit success with silently truncated output.
			if err == io.EOF {
				return 0
			}
			_, _ = fmt.Fprintf(stderr, "sheesh: reading input: %v\n", err)
			return 1
		}
	}
}

// copyStream passes the input through byte for byte
func copyStream(stdin io.Reader, stdout, stderr io.Writer) int {
	if _, err := io.Copy(stdout, stdin); err != nil {
		_, _ = fmt.Fprintf(stderr, "sheesh: reading input: %v\n", err)
		return 1
	}
	return 0
}

// renderLine renders one raw line, reporting false when the line is filtered
// out. A line that does not parse is passed through verbatim and never
// filtered: a panic or a stack trace has no level to rank and must never be
// swallowed.
func renderLine(raw string, p profile.Profile, opts options) (string, bool) {
	l, prefix, ok := logline.Parse(raw, p.Format)
	if !ok {
		return raw, true
	}
	// A stream prefix is addressable but not data: a profile may name it in a
	// slot or a noise rule, yet a prefixed line has to render as if the prefix
	// were absent. It is installed once here so that both readings see it.
	prefixed := prefix != ""
	if prefixed {
		l[logline.PrefixField] = prefix
	}
	// Noise is judged before the level threshold, because a rule discards a
	// line whatever its level: startup chatter logged as a warning is still
	// startup chatter.
	if _, ok := noiseRule(l, p); ok {
		return "", false
	}
	f, _ := fields(l, prefixed, p)
	// The line keeps the level it was written with in the rendered initial;
	// only its rank falls back, so an unrecognised level stays visible without
	// the tool pretending it said something else.
	if level.RankOrDefault(f.Level) < opts.minLevel {
		return "", false
	}
	return render.Line(f, opts.colour) + "\n", true
}

// noiseRule reports which of the profile's rules discards this line. A rule
// naming a field the line does not have simply does not match: profiles are
// written for a stream, and not every line of a stream carries every field.
func noiseRule(l logline.Line, p profile.Profile) (int, bool) {
	for i, r := range p.Noise {
		v, ok := logline.Get(l, r.Field)
		if !ok {
			continue
		}
		// Matched against the same rendering the extras would show, so that a
		// rule can name a field holding an object without having to know that
		// it does.
		if r.Regexp.MatchString(logline.String(v)) {
			return i, true
		}
	}
	return 0, false
}

// fields resolves a parsed line against a profile into the layout's slots,
// leaving everything unclaimed as extras. The prefixed flag says whether the
// reserved prefix field was filled by a stream prefix: claiming only that case
// leaves a payload's own _prefix key showing up as an extra rather than
// disappearing.
func fields(l logline.Line, prefixed bool, p profile.Profile) (render.Fields, trace) {
	claimed := map[string]bool{}
	if prefixed {
		claimed[logline.PrefixField] = true
	}
	claim := func(path string) { claimed[strings.SplitN(path, ".", 2)[0]] = true }
	get := func(path string) (string, bool) {
		v, ok := logline.Get(l, path)
		if !ok {
			return "", false
		}
		claim(path)
		return logline.String(v), true
	}

	var tr trace
	f := render.Fields{Subject: "-", Level: level.Default}
	// The time value is read unrendered, because detection has to see a JSON
	// number as a number; stringifying first would leave it guessing at a value
	// it had just flattened. The field is claimed only if it could be read: a
	// value that is not a time is still information, and claiming it regardless
	// would drop it from the line altogether — an empty slot and no extra
	// either, which is the one outcome that loses what the line said.
	if v, ok := logline.Get(l, p.Time); ok {
		if t := logline.TimeOfDay(v, p.TimeFormat); t != "" {
			f.Time = t
			claim(p.Time)
			tr.time = p.Time
		} else {
			tr.timeUnread = true
		}
	}
	if v, ok := get(p.Level); ok {
		f.Level = v
		tr.level = p.Level
	}
	if v, ok := get(p.Message); ok {
		f.Message = v
		tr.message = p.Message
	}
	// The first candidate that is present wins, so a Backup, a Restore and a
	// BackupSchedule line all show the thing they are about.
	for _, path := range p.Subjects {
		if v, ok := get(path); ok {
			f.Subject = v
			tr.subject = path
			break
		}
	}

	// A context field repeats on nearly every line, so it carries nothing that
	// distinguishes this event from the last one. Anything not named here and
	// not already in a slot still surfaces as an extra.
	suppressed := make(map[string]bool, len(p.ContextFields))
	for _, name := range p.ContextFields {
		suppressed[name] = true
	}

	keys := make([]string, 0, len(l))
	for k := range l {
		if !claimed[k] && !suppressed[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		f.Extras = append(f.Extras, render.Extra{Key: k, Value: logline.String(l[k])})
	}
	return f, tr
}

// A trace is what fields() did with one line: the path that filled each slot,
// or the empty string where nothing did
type trace struct {
	time, level, message, subject string
	// timeUnread says the time path found a value that could not be read
	timeUnread bool
}
