// Package level owns the severity vocabulary: the names a level may be written
// with, and the order they rank in.
package level

import "strings"

// Default is the level a line is treated as when its level is missing or
// unrecognised, and the threshold used when neither --level nor LEVEL is set.
const Default = "info"

// The rungs of the ordering, exported so that anything keyed on severity — the
// renderer's colour ramp above all — depends on this ordering itself rather
// than keeping a second copy of the level names in step with it.
const (
	Trace = iota
	Debug
	Info
	Warn
	Error
)

// ranks is the case-insensitive ordering trace < debug < info < warn(ing) <
// error. warn and warning share a rung because loggers disagree on the spelling
// and nobody means two different severities by them.
var ranks = map[string]int{
	"trace":   Trace,
	"debug":   Debug,
	"info":    Info,
	"warn":    Warn,
	"warning": Warn,
	"error":   Error,
}

// Rank returns the position of a level in the ordering. It reports false for a
// name outside the vocabulary rather than guessing, so a threshold that cannot
// be understood can be refused instead of silently hiding lines.
func Rank(name string) (int, bool) {
	r, ok := ranks[strings.ToLower(name)]
	return r, ok
}

// RankOrDefault ranks a level the way a rendered line is ranked: a missing or
// unrecognised level is treated as Default, so it stays visible rather than
// vanishing below the threshold.
func RankOrDefault(name string) int {
	if r, ok := Rank(name); ok {
		return r
	}
	r, _ := Rank(Default)
	return r
}
