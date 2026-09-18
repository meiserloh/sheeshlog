package level_test

import (
	"testing"

	"github.com/meiserloh/sheeshlog/internal/level"
)

func TestRank(t *testing.T) {
	tests := []struct {
		in   string
		want int
		ok   bool
	}{
		{"trace", 0, true},
		{"debug", 1, true},
		{"info", 2, true},
		{"warn", 3, true},
		{"warning", 3, true},
		{"error", 4, true},
		// Case is not part of the vocabulary: loggers disagree on it.
		{"ERROR", 4, true},
		{"Warning", 3, true},
		// Unrecognised is not an error here; the caller decides what to do
		// with a line whose level it cannot rank.
		{"", 0, false},
		{"fatal", 0, false},
		{"30", 0, false},
	}
	for _, tt := range tests {
		got, ok := level.Rank(tt.in)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("Rank(%q) = %d, %v; want %d, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

// The ranking has to be a total order for a threshold comparison to mean
// anything, and warn and warning have to be the same rung.
func TestRankIsOrdered(t *testing.T) {
	order := []string{"trace", "debug", "info", "warn", "error"}
	for i := 1; i < len(order); i++ {
		prev, _ := level.Rank(order[i-1])
		cur, _ := level.Rank(order[i])
		if prev >= cur {
			t.Errorf("Rank(%q) = %d is not below Rank(%q) = %d", order[i-1], prev, order[i], cur)
		}
	}
	warn, _ := level.Rank("warn")
	warning, _ := level.Rank("warning")
	if warn != warning {
		t.Errorf("warn = %d but warning = %d", warn, warning)
	}
}

// A line with no level or an unrecognised one renders at the default, so it is
// visible by default rather than vanishing.
func TestDefaultIsInfo(t *testing.T) {
	info, _ := level.Rank("info")
	if got, ok := level.Rank(level.Default); !ok || got != info {
		t.Errorf("Rank(Default) = %d, %v; want %d, true", got, ok, info)
	}
	for _, name := range []string{"", "fatal", "30"} {
		if got := level.RankOrDefault(name); got != info {
			t.Errorf("RankOrDefault(%q) = %d; want %d", name, got, info)
		}
	}
	if got := level.RankOrDefault("error"); got == info {
		t.Errorf("RankOrDefault(\"error\") fell back to the default")
	}
}
