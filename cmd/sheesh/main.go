// Command sheesh renders structured log lines into a compact, human-readable
// form. It is a shim: all behaviour lives behind sheeshlog.Run.
package main

import (
	"os"
	"path/filepath"

	"github.com/meiserloh/sheeshlog"
)

func main() {
	env := sheeshlog.Env{
		ConfigRoot:       configRoot(),
		Vars:             map[string]string{},
		StdinIsTerminal:  isTerminal(os.Stdin),
		StdoutIsTerminal: isTerminal(os.Stdout),
	}
	for _, name := range []string{"LEVEL", "NO_COLOR", "XDG_CONFIG_HOME"} {
		if v, ok := os.LookupEnv(name); ok {
			env.Vars[name] = v
		}
	}
	os.Exit(sheeshlog.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, env))
}

// configRoot honours XDG_CONFIG_HOME and falls back to the directory the
// convention names when it is unset. Without the fallback an unset variable
// would resolve profile paths against the working directory, which is where
// the user is standing rather than where their profiles are.
func configRoot() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Nothing usable to fall back to. Returning empty is not silent: any
		// --profile then fails naming the path it looked in, which is a better
		// error than one invented here about a home directory.
		return ""
	}
	return filepath.Join(home, ".config")
}

// isTerminal reports whether f is a character device, which is how a terminal
// is distinguished from a pipe or a file without taking a dependency.
func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}
