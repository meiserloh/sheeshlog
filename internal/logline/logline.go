// Package logline is the parser boundary: it turns one raw line of input into
// the fields a profile can address. JSON and logfmt are the formats it reads;
// a further one — regex capture, say — is meant to arrive here without the
// renderer noticing.
package logline

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A Line is one parsed log line, addressed by dotted path.
type Line map[string]any

// PrefixField is the name a stream prefix is addressed by, so a profile can
// surface which pod a line came from with the same dotted path it uses for any
// other field. Parse does not write it: the caller places the prefix under this
// name only when there was one, so that a payload carrying its own _prefix key
// stays visible as an extra rather than vanishing.
const PrefixField = "_prefix"

// A Format names how a payload is decoded into addressable fields
type Format string

const (
	FormatJSON Format = "json"
	// FormatLogfmt reads `key=value` pairs separated by whitespace, values
	// optionally quoted. Every value is a string: logfmt has no types.
	FormatLogfmt Format = "logfmt"
)

var formatNames = []Format{FormatJSON, FormatLogfmt}

// ParseFormat reads a format name as a profile writes it, matched
// case-insensitively. The default is FormatJSON
func ParseFormat(s string) (Format, bool) {
	if s == "" {
		return FormatJSON, true
	}
	want := Format(strings.ToLower(s))
	for _, f := range formatNames {
		if want == f {
			return f, true
		}
	}
	return "", false
}

// FormatNames lists the statable format names, so that whoever refuses an
// unknown one can say what was expected without keeping its own copy.
func FormatNames() []string {
	names := make([]string, 0, len(formatNames))
	for _, f := range formatNames {
		names = append(names, string(f))
	}
	return names
}

// maxPrefixTokens is how many leading tokens a stream prefix may be. Two covers
// every viewing tool in use: none reading a container directly, one for k9s on
// a multi-container pod and for `kubectl logs --prefix`, two for kubectl stern.
const maxPrefixTokens = 2

// Parse decodes one raw line in the declared format, detecting any stream
// prefix ahead of the payload. It reports false when no token count yields a payload.
func Parse(s string, f Format) (Line, string, bool) {
	s = strings.TrimRight(s, "\r\n")
	rest := s
	for k := 0; ; k++ {
		if l, ok := decode(rest, f); ok {
			// rest is always a suffix of s, so what precedes it is exactly the
			// tokens that were stripped, returned as the line wrote them.
			return l, strings.TrimSpace(s[:len(s)-len(rest)]), true
		}
		if k == maxPrefixTokens {
			return nil, "", false
		}
		var ok bool
		if rest, ok = stripToken(rest); !ok {
			return nil, "", false
		}
	}
}

// stripToken removes one leading whitespace-separated token, reporting false
// when there is no token to remove. A prefix token names a pod or a container, so it holds no
// whitespace, `=`, `"` or `{`: those belong to the payload of some format.
func stripToken(s string) (string, bool) {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	end := strings.IndexFunc(s, unicode.IsSpace)
	if end < 0 {
		// No whitespace left -> nothing to strip anymore
		return "", false
	}
	if strings.ContainsAny(s[:end], `="{`) {
		return "", false
	}
	return s[end:], true
}

// decode reads a whole payload in one format. Anything left over is a failure
// rather than a partial reading.
func decode(s string, f Format) (Line, bool) {
	switch f {
	case FormatJSON:
		s = strings.TrimSpace(s)

		if !strings.HasPrefix(s, "{") {
			return nil, false
		}
		var l Line
		if err := json.Unmarshal([]byte(s), &l); err != nil {
			return nil, false
		}
		return l, true
	case FormatLogfmt:
		return decodeLogfmt(s)
	}
	return nil, false
}

// decodeLogfmt reads a whole payload as logfmt. A failure anywhere fails the
// whole line, which the caller renders verbatim: half a record read is worse
// than none, because nothing in the output says which half is missing.
//
// The payload must *open* with an assignment. Everything is arguably logfmt —
// a bare word is a legal field — so without that rule no line would ever fail
// to decode, and the stream prefix could never be detected. After the first token
// a bare word is what the format says it is: a field with an empty value.
func decodeLogfmt(s string) (Line, bool) {
	l := Line{}
	for i, first := 0, true; ; first = false {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i == len(s) {
			// An empty payload is not a record. It is also what a line of
			// nothing but whitespace decodes to, and that is not one either.
			return l, !first
		}

		start := i
		for i < len(s) && !isSpace(s[i]) && s[i] != '=' {
			i++
		}
		key := s[start:i]
		// A token opening with `=` has no key, so there is nothing to address
		// the value by and the line is malformed rather than partly readable.
		if key == "" {
			return nil, false
		}

		if i == len(s) || s[i] != '=' {
			// first token has to be an assignment
			if first {
				return nil, false
			}
			// Last key wins
			l[key] = ""
			continue
		}

		v, n, ok := readValue(s[i+1:])
		if !ok {
			return nil, false
		}
		i += 1 + n
		l[key] = v
	}
}

// readValue reads one value and reports how many bytes it took. An unquoted
// value runs to the next whitespace, so an `=` inside one is part of it; a
// quoted value is unquoted Go-style, so `\n` and an embedded JSON object both
// arrive as what they were written to mean.
func readValue(s string) (string, int, bool) {
	if s == "" || isSpace(s[0]) {
		// `key=` with nothing after it. An empty value is a value.
		return "", 0, true
	}
	if s[0] != '"' {
		end := 0
		for end < len(s) && !isSpace(s[end]) {
			end++
		}
		return s[:end], end, true
	}

	i, closed := 1, false
	for i < len(s) && !closed {
		switch s[i] {
		case '\\':
			// Skips whatever follows, so an escaped quote does not close the
			// value. A trailing backslash runs i past the end, which is the
			// unterminated case below.
			i += 2
		case '"':
			i, closed = i+1, true
		default:
			i++
		}
	}
	// Unterminated — which is also where a trailing backslash lands, having run
	// i past the end — or something other than whitespace butted against the
	// closing quote: `k="a"b` is neither one value nor two.
	if !closed || (i < len(s) && !isSpace(s[i])) {
		return "", 0, false
	}
	// Unquote is the arbiter of a malformed escape: it refuses `"\q"`, and the
	// line goes to verbatim passthrough. It is not a filter on what reaches the
	// terminal — a literal tab and an escaped `\x1b` both unquote happily, as
	// their JSON spellings already do.
	v, err := strconv.Unquote(s[:i])
	if err != nil {
		return "", 0, false
	}
	return v, i, true
}

// isSpace is the byte-wise separator of logfmt tokens. Bytes rather than runes,
// so that indices stay valid mid-scan; every whitespace character a log line
// separates fields with is ASCII.
func isSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

// Get resolves a dotted path such as "Backup.name". A path that is missing, or
// that lands on a non-object part way down, reports false rather than erroring:
// an unfilled slot is a rendering outcome, not a failure.
func Get(l Line, path string) (any, bool) {
	var cur any = map[string]any(l)
	for _, seg := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// String renders a field value the way the original oplog script's `tostring`
// does: scalars bare, objects and arrays as compact JSON.
func String(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// A TimeFormat names how a time value is read. TimeAuto covers the styles a
// profile is likely to meet without being told; the named formats exist so that
// a style whose values are ambiguous can say which it means.
type TimeFormat string

const (
	// TimeAuto detects the format from the value itself.
	TimeAuto TimeFormat = "auto"
	// TimeRFC3339 reads a timestamp string, with or without a zone.
	TimeRFC3339 TimeFormat = "rfc3339"
	// TimeEpochSeconds reads seconds since the Unix epoch, fraction included.
	TimeEpochSeconds TimeFormat = "epoch"
	// TimeEpochMillis reads milliseconds since the Unix epoch.
	TimeEpochMillis TimeFormat = "epochmillis"
)

// timeFormatNames are the formats a profile may state, in the order an error
// message should list them. Kept here beside the constants so that the names a
// profile writes and the formats this package reads cannot drift apart.
var timeFormatNames = []TimeFormat{TimeAuto, TimeRFC3339, TimeEpochSeconds, TimeEpochMillis}

// ParseTimeFormat reads a format name as a profile writes it, matched
// case-insensitively the way a level or a colour mode is. The empty name is
// TimeAuto: a profile that says nothing about its timestamps gets detection,
// which is the whole point of detection.
func ParseTimeFormat(s string) (TimeFormat, bool) {
	if s == "" {
		return TimeAuto, true
	}
	want := TimeFormat(strings.ToLower(s))
	for _, f := range timeFormatNames {
		if want == f {
			return f, true
		}
	}
	return "", false
}

// TimeFormatNames lists the statable format names, so that whoever refuses an
// unknown one can say what was expected without keeping its own copy.
func TimeFormatNames() []string {
	names := make([]string, 0, len(timeFormatNames))
	for _, f := range timeFormatNames {
		names = append(names, string(f))
	}
	return names
}

// epochMillisThreshold splits seconds from milliseconds by magnitude. A present
// day instant is about 1.7e9 seconds or 1.7e12 milliseconds, so a boundary at
// 1e11 sits two orders of magnitude clear of both and stays right for decades
// either side of today. A style whose values genuinely fall the wrong side of
// it is what stating the format explicitly is for.
const epochMillisThreshold = 1e11

// epochMillisCeiling is the other end of the same guess. Microsecond and
// nanosecond epochs exist in the wild — journald and some gRPC styles emit them
// — and read as milliseconds they render a perfectly plausible wrong time,
// which is worse than an empty slot because nothing about it looks wrong. Above
// this, detection declines to guess and the value stays unread.
//
// There is no format name for those styles either, so today they have no
// reading at all: an empty slot and the value kept as an extra. That is a gap,
// not a design — it is the honest half of the answer, and adding the formats is
// a smaller change than the wrong time would be to notice.
const epochMillisCeiling = 1e14

// epochLimit bounds the value so the int64 conversions below are always
// defined. It is deliberately not a plausibility check: only the time of day is
// printed, so an instant in the year four billion looks exactly as reasonable
// as this morning, and no bound here could tell them apart.
const epochLimit = 1e18

// rfc3339Layouts are the timestamp shapes accepted as RFC3339. The zoneless
// spelling is not strictly RFC3339, but enough loggers emit it that refusing it
// would send people to the explicit-format key for a timestamp everyone would
// call ordinary. Both spellings tolerate a fractional second or none.
var rfc3339Layouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"}

// TimeOfDay renders a timestamp's time of day. Nothing is converted: an RFC3339
// value is cut, never parsed-and-reformatted, so the digits printed are the
// digits written; an epoch value carries no zone at all, being defined as a
// count from 1970-01-01T00:00:00Z, so reading it in UTC is reading it in the
// only units it was ever expressed in.
//
// A value that cannot be read as the given format leaves the slot empty rather
// than erroring: an unfilled slot is a rendering outcome, and the rest of the
// line still has to render.
func TimeOfDay(v any, f TimeFormat) string {
	switch f {
	case TimeRFC3339:
		s, ok := v.(string)
		if !ok {
			return ""
		}
		return rfc3339TimeOfDay(s)
	case TimeEpochSeconds:
		return epochTimeOfDay(v, false)
	case TimeEpochMillis:
		return epochTimeOfDay(v, true)
	}
	// TimeAuto falls through to detection, and so does anything unrecognised:
	// the profile key naming a format is validated where it is read, so that a
	// typo in a hand-written profile is refused by name rather than quietly
	// arriving here as a format nobody defined.

	// Detection, in the one order that cannot mistake one format for another: a
	// timestamp string never parses as a number, and a number never parses as a
	// timestamp.
	if s, ok := v.(string); ok {
		if t := rfc3339TimeOfDay(s); t != "" {
			return t
		}
	}
	n, ok := epochNumber(v)
	if !ok {
		return ""
	}
	mag := math.Abs(n)
	if mag >= epochMillisCeiling {
		return ""
	}
	return epochTimeOfDay(n, mag >= epochMillisThreshold)
}

// rfc3339TimeOfDay validates before it cuts. The validation is what keeps a
// value like "STARTED" out of the time slot; the cut is what keeps the printed
// digits identical to the written ones, offset and all.
//
// RFC 3339 §5.6 permits a lowercase separator and zone marker, and Go's layouts
// do not, so the value is upper-cased to be validated and then cut from the
// original: a stream writing the lowercase spelling must not lose every one of
// its timestamps to a spelling the standard allows.
func rfc3339TimeOfDay(s string) string {
	parsed := false
	for _, layout := range rfc3339Layouts {
		// Safe to upper-case wholesale: every other character RFC 3339 allows
		// is a digit or punctuation.
		if _, err := time.Parse(layout, strings.ToUpper(s)); err == nil {
			parsed = true
			break
		}
	}
	if !parsed {
		return ""
	}
	sep := strings.IndexAny(s, "Tt")
	if sep < 0 {
		return ""
	}
	after := s[sep+1:]
	return strings.TrimSuffix(strings.TrimSuffix(after, "Z"), "z")
}

// epochNumber accepts a JSON number and a string holding one, because a logger
// emitting "ts":"1755750192" means the same instant as one emitting the number.
func epochNumber(v any) (float64, bool) {
	var n float64
	switch t := v.(type) {
	case float64:
		n = t
	case string:
		// Checked before parsing, because ParseFloat accepts the whole of Go's
		// float syntax: "0x1p+10", "1e9" and "1_755_750_192" all parse, and a
		// field holding one of those is not a timestamp. Rendering it as a time
		// of day would be a confident wrong answer of exactly the kind the
		// millisecond ceiling above refuses to give.
		if !decimalNumber(t) {
			return 0, false
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return 0, false
		}
		n = f
	default:
		return 0, false
	}
	// ParseFloat accepts "NaN" and "Inf", and neither is an instant.
	if math.IsNaN(n) || math.Abs(n) > epochLimit {
		return 0, false
	}
	return n, true
}

// decimalNumber reports whether s is a plain decimal number, optionally signed
// and optionally fractional — which is how every logger that writes an epoch
// into a string writes it.
func decimalNumber(s string) bool {
	s = strings.TrimPrefix(s, "-")
	whole, frac, hasDot := strings.Cut(s, ".")
	if whole == "" || (hasDot && frac == "") {
		return false
	}
	for _, digits := range [2]string{whole, frac} {
		for _, r := range digits {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// epochTimeOfDay renders a count from the Unix epoch in UTC. Milliseconds keep
// their fraction: a style that went to the trouble of logging them is a style
// where the order of two events inside one second is worth seeing.
func epochTimeOfDay(v any, millis bool) string {
	n, ok := epochNumber(v)
	if !ok {
		return ""
	}
	if millis {
		return time.UnixMilli(int64(math.Round(n))).UTC().Format("15:04:05.000")
	}
	// Split rather than scaled, so that a whole second stays exact however far
	// from the epoch it is.
	whole, frac := math.Modf(n)
	return time.Unix(int64(whole), int64(math.Round(frac*1e9))).UTC().Format("15:04:05")
}
