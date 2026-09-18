package state

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/meiserloh/sheeshlog/internal/profile"
)

// path is where a config root keeps the remembered profile
func path(configRoot string) string {
	return filepath.Join(configRoot, "sheesh", "current")
}

// Current reports the remembered profile name, or the empty string when there
// is nothing usable to report. Every failure reads as "nothing remembered"
// rather than as an error
func Current(configRoot string) string {
	b, err := os.ReadFile(path(configRoot))
	if err != nil {
		return ""
	}

	name := strings.TrimSpace(string(b))
	if profile.ValidName(name) != nil {
		return ""
	}
	return name
}

func SetCurrent(configRoot, name string) error {
	if err := profile.ValidName(name); err != nil {
		return err
	}
	p := path(configRoot)

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(name+"\n"), 0o644)
}
