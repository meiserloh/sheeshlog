package sheeshlog

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/meiserloh/sheeshlog/internal/level"
	"github.com/meiserloh/sheeshlog/internal/logline"
	"github.com/meiserloh/sheeshlog/internal/profile"
)

// check reports what a profile actually did to a sample, rendering none of it.
func check(args []string, fs *flag.FlagSet, f flags, stdin io.Reader, stdout io.Writer, env Env) error {
	if len(args) != 1 {
		return fmt.Errorf("check: want exactly one profile name (got %d)", len(args))
	}

	if env.StdinIsTerminal {
		return fmt.Errorf("check: reads a sample on stdin (try: sheesh check %s < sample.log)", args[0])
	}

	name := args[0]
	p, err := profile.Load(env.ConfigRoot, name)
	if err != nil {
		return err
	}
	// Resolved the way a render resolves it, so the threshold the report counts
	// against is the one a render would have used.
	opts, err := renderOptions(fs, f, env)
	if err != nil {
		return err
	}

	r, err := inspect(stdin, p, opts)
	if err != nil {
		return err
	}
	r.profile, r.threshold = name, opts.levelName
	writeReport(stdout, p, r)
	return nil
}

// A report is what a sample turned out to be, counted against one profile.
// Every count is over the whole sample unless said otherwise.
type report struct {
	profile   string
	threshold string

	lines int
	// passedThrough counts the lines the declared format does not read. They
	// are not dropped: the renderer writes them out verbatim, so they are
	// output the profile had no part in.
	passedThrough int
	noise         int
	belowLevel    int
	rendered      int

	// slots are the four fixed slots in the order they are rendered in.
	slots []slot
	// noiseCounts is parallel to the profile's rules: how many lines each one
	// discarded. A line is counted against the first rule that matched it,
	// which is the rule that actually dropped it, so these sum to noise.
	noiseCounts []int
	// context and seen count how often a field was present. seen counts every
	// field, context only the ones the profile suppresses.
	context map[string]int
	seen    map[string]int
}

// A slot is one of the four rendered positions and what filled it. Paths is
// kept in the profile's own order rather than sorted by count, because for the
// subject that order is the rule being tested: the first candidate present
// wins, so a later one scoring highly says the earlier ones are rarely there.
type slot struct {
	name  string
	paths []string
	count map[string]int
	empty int
	// unread is the time slot's own case: a value was found where the path
	// said, and it could not be read as a time.
	unread int
}

// fill records that path filled this slot, or that nothing did.
func (s *slot) fill(path string) {
	if path == "" {
		s.empty++
		return
	}
	s.count[path]++
}

// inspect runs the sample through the same decisions renderLine makes, in the
// same order, counting instead of rendering.
func inspect(stdin io.Reader, p profile.Profile, opts options) (report, error) {
	r := report{
		noiseCounts: make([]int, len(p.Noise)),
		context:     map[string]int{},
		seen:        map[string]int{},
	}
	for _, s := range []struct {
		name  string
		paths []string
	}{
		{"time", []string{p.Time}},
		{"level", []string{p.Level}},
		{"message", []string{p.Message}},
		{"subject", p.Subjects},
	} {
		r.slots = append(r.slots, slot{name: s.name, paths: s.paths, count: map[string]int{}})
	}

	in := bufio.NewReader(stdin)
	for {
		raw, err := in.ReadString('\n')
		if raw != "" {
			r.count(raw, p, opts)
		}
		if err != nil {
			// Only EOF is the end of the sample. A read that failed part way
			// would otherwise be reported as a complete sample that the profile
			// dropped nearly all of, which is the wrong thing to go and fix.
			if err == io.EOF {
				return r, nil
			}
			return r, fmt.Errorf("check: reading the sample: %w", err)
		}
	}
}

// count puts one raw line through the profile. The order is renderLine's
// order — parse, prefix, noise, slots, level — because a report that judged
// them in a different order would describe a different tool.
func (r *report) count(raw string, p profile.Profile, opts options) {
	r.lines++

	l, prefix, ok := logline.Parse(raw, p.Format)
	if !ok {
		// A line the declared format does not read, which the renderer writes
		// out verbatim and never filters
		r.passedThrough++
		return
	}
	prefixed := prefix != ""
	if prefixed {
		l[logline.PrefixField] = prefix
	}
	for k := range l {
		r.seen[k]++
	}

	if i, dropped := noiseRule(l, p); dropped {
		r.noise++
		r.noiseCounts[i]++
		return
	}

	_, tr := fields(l, prefixed, p)
	if tr.timeUnread {
		// Counted apart from an empty slot rather than as one: the path is
		// right and the timeformat is wrong, which is a different line of the
		// profile to go and edit.
		r.slots[0].unread++
	} else {
		r.slots[0].fill(tr.time)
	}
	for i, path := range []string{tr.level, tr.message, tr.subject} {
		r.slots[i+1].fill(path)
	}
	// Counted after the noise rules, because a suppressed field on a line that
	// was discarded whole was not suppressed by the context list.
	for _, name := range p.ContextFields {
		if _, ok := l[name]; ok {
			r.context[name]++
		}
	}

	// The rank falls back for an unrecognised level exactly as it does when
	// rendering, so a line the renderer would have kept is counted as kept.
	if level.RankOrDefault(valueOf(l, p.Level)) < opts.minLevel {
		r.belowLevel++
		return
	}
	r.rendered++
}

// valueOf is the level slot's value as the renderer reads it, empty when the
// path names nothing.
func valueOf(l logline.Line, path string) string {
	v, ok := logline.Get(l, path)
	if !ok {
		return ""
	}
	return logline.String(v)
}

// writeReport prints the report in a fixed shape: every section, every time,
// in the same order
func writeReport(out io.Writer, p profile.Profile, r report) {
	_, _ = fmt.Fprintf(out, "profile %s, threshold %s\n", r.profile, r.threshold)
	_, _ = fmt.Fprintf(out, "sample %s: %d passed through, %d noise, %d below %s, %d rendered\n",
		plural(r.lines, "line"), r.passedThrough, r.noise, r.belowLevel, r.threshold, r.rendered)
	if r.rendered == 0 && r.lines > 0 {
		_, _ = fmt.Fprintln(out, "nothing rendered through the profile: every line was dropped or passed through")
	}

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	// The denominator for the slots: an unparseable line has no fields to fill
	// them with, and a line dropped as noise never reached them.
	parsed := r.lines - r.passedThrough
	reaching := parsed - r.noise
	_, _ = fmt.Fprintf(out, "\nslots (of %s reaching them)\n", plural(reaching, "line"))
	for _, s := range r.slots {
		if note := notes(s); note != "" {
			_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\n", s.name, filledBy(s), note)
			continue
		}
		_, _ = fmt.Fprintf(w, "  %s\t%s\n", s.name, filledBy(s))
	}
	_ = w.Flush()

	_, _ = fmt.Fprintln(out, "\nnoise dropped")
	if len(p.Noise) == 0 {
		_, _ = fmt.Fprintln(out, "  (no rules)")
	}
	for i, rule := range p.Noise {
		_, _ = fmt.Fprintf(w, "  %s\t%s\t%d\n", rule.Field, rule.Pattern, r.noiseCounts[i])
	}
	_ = w.Flush()

	_, _ = fmt.Fprintf(out, "\ncontext suppressed (of the same %s)\n", plural(reaching, "line"))
	if len(p.ContextFields) == 0 {
		_, _ = fmt.Fprintln(out, "  (no fields)")
	}
	// In the profile's order, so the report reads alongside the file it is
	// about; a field that never appeared is the interesting one and has to keep
	// its place in the list to be noticed as missing.
	for _, name := range p.ContextFields {
		_, _ = fmt.Fprintf(w, "  %s\t%d\n", name, r.context[name])
	}
	_ = w.Flush()

	// Over every parsed line, including the ones dropped as noise
	_, _ = fmt.Fprintf(out, "\nfields seen (of %s parsed)\n", plural(parsed, "line"))
	if len(r.seen) == 0 {
		_, _ = fmt.Fprintln(out, "  (none)")
	}

	names := make([]string, 0, len(r.seen))
	for name := range r.seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		_, _ = fmt.Fprintf(w, "  %s\t%d\n", name, r.seen[name])
	}
	_ = w.Flush()
}

// filledBy is the paths that filled a slot with their counts, in the profile's
// own order. A declared path that never filled it is still listed, at zero:
// that is what a misspelled path looks like, and it can only be seen if it is
// printed.
func filledBy(s slot) string {
	paths := declared(s)
	if len(paths) == 0 {
		return "(no path declared)"
	}
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		parts = append(parts, fmt.Sprintf("%s %d", path, s.count[path]))
	}
	return strings.Join(parts, ", ")
}

// declared is the slot's paths with the blanks removed. A profile that says
// nothing for a slot leaves an empty string there rather than no entry.
func declared(s slot) []string {
	paths := make([]string, 0, len(s.paths))
	for _, path := range s.paths {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

// notes is what went wrong with a slot, and says nothing when nothing did: a
// column of "(0 empty)" is noise in a report about noise.
func notes(s slot) string {
	// A slot with no path declared is empty on every line by construction, and
	// filledBy has already said so; counting them again just adds a number to
	// read past.
	if len(declared(s)) == 0 {
		return ""
	}

	var parts []string
	if s.empty > 0 {
		parts = append(parts, fmt.Sprintf("%d empty", s.empty))
	}
	// Named for the fix rather than the symptom: the path found a value, so it
	// is the timeformat that needs looking at, not the path.
	if s.unread > 0 {
		parts = append(parts, fmt.Sprintf("%d not read as a time", s.unread))
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// plural keeps the summary reading like a sentence rather than like a struct.
func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}
