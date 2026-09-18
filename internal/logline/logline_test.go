package logline_test

import (
	"testing"

	"github.com/meiserloh/sheeshlog/internal/logline"
)

// The tokens ahead of the payload are the stream prefix, and it is returned
// rather than discarded: tailing several pods is only legible if a line can say
// which pod it came from.
func TestParseSplitsStreamPrefix(t *testing.T) {
	l, prefix, ok := logline.Parse(`[pod/operator-abc/manager] {"msg":"hello"}`+"\n", logline.FormatJSON)
	if !ok {
		t.Fatal("prefixed line did not parse")
	}
	if prefix != "[pod/operator-abc/manager]" {
		t.Errorf("prefix = %q", prefix)
	}
	if v, ok := logline.Get(l, "msg"); !ok || logline.String(v) != "hello" {
		t.Errorf("msg = %v (present: %v), want the payload parsed as if unprefixed", v, ok)
	}
}

// Parse never writes the reserved name itself, so a payload that happens to
// carry a _prefix key arrives untouched and the caller decides what it means.
func TestParseLeavesReservedFieldToTheCaller(t *testing.T) {
	l, prefix, ok := logline.Parse(`{"msg":"hello"}`, logline.FormatJSON)
	if !ok {
		t.Fatal("unprefixed line did not parse")
	}
	if prefix != "" {
		t.Errorf("prefix = %q, want empty", prefix)
	}
	if v, ok := logline.Get(l, logline.PrefixField); ok {
		t.Errorf("%s = %v, want absent", logline.PrefixField, v)
	}

	l, prefix, ok = logline.Parse(`[pod/x/c] {"msg":"hello","_prefix":"FAKE"}`, logline.FormatJSON)
	if !ok {
		t.Fatal("line did not parse")
	}
	if prefix != "[pod/x/c]" {
		t.Errorf("prefix = %q", prefix)
	}
	if v, _ := logline.Get(l, logline.PrefixField); logline.String(v) != "FAKE" {
		t.Errorf("%s = %v, want the payload's own value left as written", logline.PrefixField, v)
	}
}

func TestParseRejectsNonJSON(t *testing.T) {
	for _, s := range []string{
		"",
		"\n",
		"not json at all",
		"panic: runtime error: invalid memory address",
		// A brace alone is not a payload: the split must not turn a stack frame
		// into a half-parsed line.
		`main.reconcile(0xc000123456, map[string]string{"a":"b"})`,
		`[1,2,3]`,
	} {
		if _, _, ok := logline.Parse(s, logline.FormatJSON); ok {
			t.Errorf("Parse(%q) parsed, want rejected so the line passes through verbatim", s)
		}
	}
}

// Dotted-path lookup is one of the two places the spec asks for unit tests,
// because an unfilled slot is a rendering outcome rather than a failure and the
// ways a path can come up empty are easy to get subtly wrong.
func TestGetDottedPaths(t *testing.T) {
	l, _, ok := logline.Parse(`{"msg":"hi","Backup":{"name":"backup-1","meta":{"ns":"production"}},"count":3,"nil":null}`, logline.FormatJSON)
	if !ok {
		t.Fatal("fixture did not parse")
	}

	for _, tc := range []struct {
		path string
		want string
	}{
		{"msg", "hi"},
		{"Backup.name", "backup-1"},
		{"Backup.meta.ns", "production"},
		{"count", "3"},
		{"nil", "null"},
	} {
		v, ok := logline.Get(l, tc.path)
		if !ok {
			t.Errorf("Get(%q) reported absent, want %q", tc.path, tc.want)
			continue
		}
		if got := logline.String(v); got != tc.want {
			t.Errorf("Get(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}

	// Every way a path can fail to land reports absent rather than erroring.
	for _, path := range []string{
		"absent",           // missing at the top level
		"Backup.absent",    // missing one level down
		"msg.name",         // walks into a string
		"count.name",       // walks into a number
		"nil.name",         // walks into a null
		"Backup.name.deep", // walks past a leaf
		"",                 // the empty path names nothing
	} {
		if v, ok := logline.Get(l, path); ok {
			t.Errorf("Get(%q) = %v, want absent", path, v)
		}
	}
}

// Time-format detection is the other place the spec asks for unit tests. The
// slot is cosmetic, so every wrong answer is quiet: a garbage value rendered as
// a plausible time, or a real timestamp silently dropped, both look like
// working output.
func TestTimeOfDayDetectsFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		// RFC3339 is cut, never reformatted, so the digits printed are the
		// digits written — including a fraction, and including an offset, which
		// says what the written time of day is relative to.
		{"rfc3339 utc", "2026-08-21T05:03:12Z", "05:03:12"},
		{"rfc3339 fraction", "2026-08-21T05:03:12.123456Z", "05:03:12.123456"},
		{"rfc3339 offset", "2026-08-21T05:03:12+02:00", "05:03:12+02:00"},
		{"rfc3339 no zone", "2026-08-21T05:03:12", "05:03:12"},
		// RFC 3339 §5.6 permits the lowercase spelling, so a stream using it
		// must not lose every timestamp it has.
		{"rfc3339 lowercase", "2026-08-21t05:03:12z", "05:03:12"},
		{"rfc3339 lowercase zone only", "2026-08-21T05:03:12z", "05:03:12"},

		// Epoch values are read in UTC, which is not a conversion: the count is
		// defined from 1970-01-01T00:00:00Z, so those are the units it is in.
		{"epoch seconds", float64(1755750192), "04:23:12"},
		{"epoch seconds as string", "1755750192", "04:23:12"},
		{"epoch seconds with fraction", 1755750192.75, "04:23:12"},
		{"epoch millis", float64(1755750192123), "04:23:12.123"},
		{"epoch millis as string", "1755750192123", "04:23:12.123"},

		// Everything unreadable leaves the slot empty rather than guessing. The
		// bare word matters: cutting at the first T without checking would have
		// rendered "STARTED" as "ED".
		{"word containing T", "STARTED", ""},
		{"empty string", "", ""},
		{"prose", "not a time at all", ""},
		{"date only", "2026-08-21", ""},
		{"impossible date", "2026-13-45T99:99:99Z", ""},
		{"boolean", true, ""},
		{"null", nil, ""},
		{"object", map[string]any{"seconds": float64(1755750192)}, ""},
		{"not a number", "NaN", ""},
		// Go's float syntax is far wider than anything a logger writes, and a
		// field holding one of these is not a timestamp.
		{"hex float", "0x1p+10", ""},
		{"exponent", "1e9", ""},
		{"underscores", "1_755_750_192", ""},
		{"trailing dot", "1755750192.", ""},
		{"lone sign", "-", ""},
		// A signed epoch is a real instant before 1970, and reads as one.
		{"negative epoch seconds", float64(-14182940), "20:17:40"},
		{"beyond any clock", 1e300, ""},

		// Microsecond and nanosecond epochs read as milliseconds would render a
		// plausible wrong time, and nothing about the output would look wrong.
		// Detection declines the guess instead.
		{"epoch micros", float64(1755750192123456), ""},
		{"epoch nanos", float64(1755750192123456789), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logline.TimeOfDay(tc.v, logline.TimeAuto); got != tc.want {
				t.Errorf("TimeOfDay(%#v, auto) = %q, want %q", tc.v, got, tc.want)
			}
		})
	}
}

// Detection splits seconds from milliseconds by magnitude, so a value on the
// wrong side of the boundary is read as the other format. Naming the format is
// how a style says which it meant.
func TestTimeOfDayStatedFormatBeatsDetection(t *testing.T) {
	// Small enough that detection reads it as seconds, so the millis reading
	// can only come from the stated format.
	const ambiguous = float64(1755750192)

	for _, tc := range []struct {
		name   string
		v      any
		format logline.TimeFormat
		want   string
	}{
		{"stated millis", ambiguous, logline.TimeEpochMillis, "07:42:30.192"},
		{"stated seconds", ambiguous, logline.TimeEpochSeconds, "04:23:12"},
		{"detected", ambiguous, logline.TimeAuto, "04:23:12"},
		// A stated format is also a refusal of the others: a profile saying its
		// times are epochs should not quietly render a timestamp string.
		{"stated seconds, given rfc3339", "2026-08-21T05:03:12Z", logline.TimeEpochSeconds, ""},
		{"stated rfc3339, given epoch", float64(1755750192), logline.TimeRFC3339, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logline.TimeOfDay(tc.v, tc.format); got != tc.want {
				t.Errorf("TimeOfDay(%#v, %q) = %q, want %q", tc.v, tc.format, got, tc.want)
			}
		})
	}
}

// The prefix is detected rather than declared, so one profile has to serve
// every viewing tool: bare from a single-container read, one token from k9s,
// two from stern. All three carry the same payload and must arrive as the same
// line.
func TestParseDetectsPrefixOfZeroOneOrTwoTokens(t *testing.T) {
	const payload = `{"msg":"hello"}`
	for _, tc := range []struct{ line, prefix string }{
		{payload, ""},
		{"manager " + payload, "manager"},
		{"my-backup-operator-7c45d48bb4-cgkp2 manager " + payload, "my-backup-operator-7c45d48bb4-cgkp2 manager"},
		// stern writes a tab between its two tokens, and the prefix is returned
		// as the line wrote it rather than tidied.
		{"pod\tmanager\t" + payload, "pod\tmanager"},
	} {
		l, prefix, ok := logline.Parse(tc.line, logline.FormatJSON)
		if !ok {
			t.Errorf("Parse(%q) did not parse", tc.line)
			continue
		}
		if prefix != tc.prefix {
			t.Errorf("Parse(%q) prefix = %q, want %q", tc.line, prefix, tc.prefix)
		}
		if v, ok := logline.Get(l, "msg"); !ok || logline.String(v) != "hello" {
			t.Errorf("Parse(%q) msg = %v (present: %v)", tc.line, v, ok)
		}
	}
}

// The regressions this whole change exists for: a positional record that merely
// *ends* in a JSON object keeps its real time, level and message by not parsing
// at all, so the renderer writes it out as it came in. Reading only the JSON
// suffix is the confidently wrong answer.
func TestParseRefusesPositionalLinesEndingInJSON(t *testing.T) {
	for _, s := range []string{
		// zap console: timestamp, level, logger, message, then JSON context.
		"2026-09-02T05:16:47Z\tINFO\tstarting server\t{\"name\": \"health probe\"}",
		"2026-09-02T05:16:47Z\tINFO\tStarting Controller\t{\"controller\": \"debugmode\"}",
		// klog: severity/date, source location, quoted message, empty object.
		`I0902 05:16:55.744117       1 server.go:288] "Using annotations allowlist" annotationsAllowList={}`,
		// Three tokens ahead of the payload is one more than any viewing tool
		// writes, so it is a positional record and not a prefixed one.
		`a b c {"msg":"hello"}`,
	} {
		if _, _, ok := logline.Parse(s, logline.FormatJSON); ok {
			t.Errorf("Parse(%q) parsed, want rejected so the line passes through verbatim", s)
		}
	}
}

// A prefix token names a pod or a container. A token holding `=`, `"` or `{` is
// a field of some format, and stripping one would be reading data as a name.
func TestParseRefusesTokensThatAreNotNames(t *testing.T) {
	for _, s := range []string{
		`level=info {"msg":"hello"}`,
		`"quoted" {"msg":"hello"}`,
		`{} {"msg":"hello"}`,
	} {
		if _, _, ok := logline.Parse(s, logline.FormatJSON); ok {
			t.Errorf("Parse(%q) parsed, want rejected", s)
		}
	}
}

// The whole remainder has to decode, so trailing text after a complete object
// is not a payload. Nor is JSON's `null`, which unmarshals into a map without
// error and would otherwise arrive as a line with no fields at all.
func TestParseRefusesPartialAndEmptyPayloads(t *testing.T) {
	for _, s := range []string{
		`{"msg":"hello"} and then some`,
		`{"msg":"hello"}{"msg":"again"}`,
		"null",
		"pod null",
	} {
		if _, _, ok := logline.Parse(s, logline.FormatJSON); ok {
			t.Errorf("Parse(%q) parsed, want rejected", s)
		}
	}
}

// The name a profile writes and the format the parser reads are matched the way
// every other name in a profile is: case-insensitively, with the empty name
// meaning the format the tool was built for.
func TestParseFormatNames(t *testing.T) {
	for _, name := range []string{"", "json", "JSON"} {
		f, ok := logline.ParseFormat(name)
		if !ok || f != logline.FormatJSON {
			t.Errorf("ParseFormat(%q) = %q, %v", name, f, ok)
		}
	}
	for _, name := range []string{"logfmt", "LOGFMT"} {
		f, ok := logline.ParseFormat(name)
		if !ok || f != logline.FormatLogfmt {
			t.Errorf("ParseFormat(%q) = %q, %v", name, f, ok)
		}
	}
	if _, ok := logline.ParseFormat("logstash"); ok {
		t.Error("ParseFormat(\"logstash\") accepted, want refused: an unread format is refused by name")
	}
}

// Real anonymized logfmt lines, reduced to the shapes that distinguish one reading
// from another: quoted and unquoted values, spaces and escaped newlines inside
// quotes, a JSON object as a value, `=` inside a value, hyphenated keys, and a
// duplicate key. Every value is a string, because logfmt has no types.
func TestParseLogfmtValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want map[string]string
	}{
		{
			"unquoted and quoted",
			`ts=2026-09-02T05:18:22.255908831Z caller=memberlist_logger.go:74 level=warn msg="Failed to resolve k8s-loki-memberlist"`,
			map[string]string{"ts": "2026-09-02T05:18:22.255908831Z", "caller": "memberlist_logger.go:74", "level": "warn", "msg": "Failed to resolve k8s-loki-memberlist"},
		},
		{
			"escaped newlines and tabs inside a quoted value",
			`level=warn err="1 error occurred:\n\t* no such host\n\n"`,
			map[string]string{"level": "warn", "err": "1 error occurred:\n\t* no such host\n\n"},
		},
		{
			"a JSON object as a field's string value",
			`level=info App="{\"name\":\"redmine\",\"namespace\":\"production\"}" name=redmine`,
			map[string]string{"level": "info", "App": `{"name":"redmine","namespace":"production"}`, "name": "redmine"},
		},
		{
			"hyphenated keys and a path value holding a slash",
			`time="2026-09-02T11:00:44Z" backup-storage-location=production/default max_attempts=10`,
			map[string]string{"time": "2026-09-02T11:00:44Z", "backup-storage-location": "production/default", "max_attempts": "10"},
		},
		{
			"an unquoted value runs to the whitespace, so it may hold =",
			`level=info query=a=b&c=d empty=`,
			map[string]string{"level": "info", "query": "a=b&c=d", "empty": ""},
		},
		{
			"a quoted value may hold = and spaces",
			`level=info msg="Leaving GOMAXPROCS=4: CPU quota undefined"`,
			map[string]string{"level": "info", "msg": "Leaving GOMAXPROCS=4: CPU quota undefined"},
		},
		{
			"a bare word among assignments is a field with an empty value",
			`level=info bare msg=hi`,
			map[string]string{"level": "info", "bare": "", "msg": "hi"},
		},
		{
			"a duplicate key resolves last-wins",
			`level=info msg=first msg=second`,
			map[string]string{"level": "info", "msg": "second"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, prefix, ok := logline.Parse(tc.line, logline.FormatLogfmt)
			if !ok {
				t.Fatalf("Parse(%q) did not parse", tc.line)
			}
			if prefix != "" {
				t.Errorf("prefix = %q, want none", prefix)
			}
			if len(l) != len(tc.want) {
				t.Errorf("fields = %v, want %d of them", l, len(tc.want))
			}
			for k, want := range tc.want {
				v, ok := logline.Get(l, k)
				if !ok {
					t.Errorf("%s missing", k)
					continue
				}
				if s, isString := v.(string); !isString {
					t.Errorf("%s = %T, want a string: logfmt has no types", k, v)
				} else if s != want {
					t.Errorf("%s = %q, want %q", k, s, want)
				}
			}
		})
	}
}

// The one-token prefixes of the corpus — velero, grafana, prometheus — resolve
// under logfmt exactly as they do under JSON, and the stern-shaped two-token
// case with them. That is what the payload having to *open* with an assignment
// buys: without it, the prefix would read as a field with an empty value and
// disappear from the line.
func TestParseLogfmtSplitsStreamPrefix(t *testing.T) {
	for _, tc := range []struct{ line, prefix string }{
		{`level=info msg=hi`, ""},
		{`velero level=info msg=hi`, "velero"},
		{`prometheus level=info msg=hi`, "prometheus"},
		{`k8s-loki-0 loki level=info msg=hi`, "k8s-loki-0 loki"},
		{`[pod/k8s-loki-0/loki] level=info msg=hi`, "[pod/k8s-loki-0/loki]"},
	} {
		l, prefix, ok := logline.Parse(tc.line, logline.FormatLogfmt)
		if !ok {
			t.Errorf("Parse(%q) did not parse", tc.line)
			continue
		}
		if prefix != tc.prefix {
			t.Errorf("Parse(%q) prefix = %q, want %q", tc.line, prefix, tc.prefix)
		}
		if v, ok := logline.Get(l, "msg"); !ok || logline.String(v) != "hi" {
			t.Errorf("Parse(%q) msg = %v (present: %v)", tc.line, v, ok)
		}
	}
}

// A malformed quoted value fails the whole line rather than half of it: a
// partly read record renders with plausible slots and nothing says which half
// was lost. Prose that never opens with an assignment is not a record either,
// which is what keeps the corpus's startup chatter passing through verbatim.
func TestParseLogfmtRefusesMalformedAndBareLines(t *testing.T) {
	for _, s := range []string{
		`level=info msg="unterminated`,
		`level=info msg="trailing backslash\`,
		`level=info msg="a"b`,
		`level=info msg="\q"`,
		`level=info =novalue`,
		`velero-plugin-for-gcp Copying /plugins/velero-plugin-for-gcp to /target ...  done.`,
		`stream closed: EOF for production/velero-5474788c94-t29rg (velero-plugin-for-gcp)`,
		`grafana   ./////,`,
		"",
		"   ",
	} {
		if _, _, ok := logline.Parse(s, logline.FormatLogfmt); ok {
			t.Errorf("Parse(%q) parsed, want rejected so the line passes through verbatim", s)
		}
	}
}
