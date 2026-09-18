// Package render owns the fixed output layout. Per ADR-0001 a profile says how
// to *read* a log style and never how the output looks, so this layout is the
// same for every profile and lives only here.
package render

import (
	"strings"
	"unicode/utf8"

	"github.com/meiserloh/sheeshlog/internal/level"
)

// The palette is fixed in code, like the layout it belongs to (ADR-0001): a
// profile says how to read a log style, never how the output looks.
const (
	reset  = "\x1b[0m"
	dim    = "\x1b[90m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

// An Extra is one field that neither filled a slot nor was suppressed as a
// context field.
type Extra struct {
	Key   string
	Value string
}

// Fields are the resolved contents of the layout's slots.
type Fields struct {
	Time    string
	Level   string
	Subject string
	Message string
	Extras  []Extra // rendered in the order given; callers sort for determinism
}

// Line renders one log event. The shape — time, level initial, subject, two
// spaces, message, two spaces, extras — is inherited from the oplog script,
// whose output is the specification of this layout. Time and message are left
// unpainted so that the eye is drawn to severity and subject, and colour is a
// per-run decision the caller has already made.
func Line(f Fields, colour bool) string {
	paint := func(code, s string) string {
		if !colour {
			return s
		}
		return code + s + reset
	}

	var b strings.Builder
	b.WriteString(f.Time)
	b.WriteByte(' ')
	b.WriteString(paint(levelColour(f.Level), levelInitial(f.Level)))
	b.WriteByte(' ')
	b.WriteString(paint(cyan, f.Subject))
	b.WriteString("  ")
	b.WriteString(f.Message)

	if len(f.Extras) > 0 {
		var e strings.Builder
		for i, x := range f.Extras {
			if i > 0 {
				e.WriteByte(' ')
			}
			e.WriteString(x.Key)
			e.WriteByte('=')
			e.WriteString(x.Value)
		}
		// The whole run of extras is dimmed as one, rather than each pair
		// separately: it reads as a single trailing aside.
		b.WriteString("  ")
		b.WriteString(paint(dim, e.String()))
	}
	return b.String()
}

// levelColour ramps the initial by severity, so an error is findable by colour
// without widening the line. An unrecognised level takes the default's colour
// for the same reason it takes the default's rank — it stays visible rather
// than being singled out.
func levelColour(name string) string {
	switch level.RankOrDefault(name) {
	case level.Trace, level.Debug:
		return dim
	case level.Warn:
		return yellow
	case level.Error:
		return red
	default:
		return green
	}
}

// levelInitial compresses the level to one uppercase character, so severity is
// findable without widening the line.
func levelInitial(name string) string {
	if name == "" {
		return " "
	}
	first, _ := utf8.DecodeRuneInString(name)
	return strings.ToUpper(string(first))
}
