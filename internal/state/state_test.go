package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/meiserloh/sheeshlog/internal/state"
)

// The path is spelled out here rather than borrowed from the package: it is
// where a user's remembered profile lives, so a change to it should break a
// test rather than silently relocate everyone's state.
func currentPath(root string) string {
	return filepath.Join(root, "sheesh", "current")
}

func TestSetCurrentThenCurrentRoundTrips(t *testing.T) {
	root := t.TempDir()
	if err := state.SetCurrent(root, "operator"); err != nil {
		t.Fatal(err)
	}
	if got := state.Current(root); got != "operator" {
		t.Errorf("Current = %q, want %q", got, "operator")
	}
}

// The directory does not exist before the first `sheesh use`, so writing has to
// create it rather than fail on a fresh machine.
func TestSetCurrentCreatesTheDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nothing", "here", "yet")
	if err := state.SetCurrent(root, "backup"); err != nil {
		t.Fatal(err)
	}
	if got := state.Current(root); got != "backup" {
		t.Errorf("Current = %q, want %q", got, "backup")
	}
}

// Nothing remembered is the ordinary state of a new install, not a failure: it
// reads as no profile, which is what makes the generic profile the default.
func TestCurrentWithNoStateIsEmpty(t *testing.T) {
	if got := state.Current(t.TempDir()); got != "" {
		t.Errorf("Current = %q, want empty", got)
	}
}

// A hand-edited file ends in a newline, and a dotfile may indent. Neither is
// corruption, so neither may lose the profile.
func TestCurrentTolerateSurroundingWhitespace(t *testing.T) {
	root := t.TempDir()
	writeState(t, root, "  operator \n")
	if got := state.Current(root); got != "operator" {
		t.Errorf("Current = %q, want %q", got, "operator")
	}
}

// Corrupt state degrades to no profile rather than erroring: a truncated write
// or a stray edit must not stop a pipe from rendering, and it must never be
// joined onto the profiles directory as a path.
func TestCurrentWithCorruptStateIsEmpty(t *testing.T) {
	for _, body := range []string{
		"",
		"   \n",
		"../../etc/passwd",
		`..\evil`,
		".",
		"..",
		"one\ntwo\n",
		"\x00binary",
	} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			writeState(t, root, body)
			if got := state.Current(root); got != "" {
				t.Errorf("Current = %q, want empty", got)
			}
		})
	}
}

// A directory where the file should be is unreadable, and unreadable state is
// still just no state.
func TestCurrentWithUnreadableStateIsEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(currentPath(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := state.Current(root); got != "" {
		t.Errorf("Current = %q, want empty", got)
	}
}

// A name that is not a profile name is refused at the point it is recorded, so
// that the corrupt-state path is a recovery route and not a normal one.
func TestSetCurrentRefusesANameThatIsNotAFileName(t *testing.T) {
	root := t.TempDir()
	if err := state.SetCurrent(root, "../elsewhere"); err == nil {
		t.Fatal("SetCurrent accepted a path as a profile name")
	}
	if got := state.Current(root); got != "" {
		t.Errorf("Current = %q, want empty: a refused name must not be written", got)
	}
}

func writeState(t *testing.T, root, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(currentPath(root)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentPath(root), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
