package sheeshlog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/meiserloh/sheeshlog/internal/profile"
	"github.com/meiserloh/sheeshlog/internal/state"
)

// errCancelled says the user left the picker without choosing
var errCancelled = errors.New("cancelled")

// pick shows the numbered list and records what the user chooses
func pick(stdin io.Reader, stdout, stderr io.Writer, env Env) error {
	names, err := profile.List(env.ConfigRoot)
	if err != nil {
		return err
	}

	if len(names) == 0 {
		return fmt.Errorf("no profiles in %s\n(a profile is a YAML file there; its file name is its name)", profile.Dir(env.ConfigRoot))
	}

	// The list and the prompt are interface, not output: they go to stderr so
	// that `sheesh > picked` at a terminal still shows the user what they are
	// being asked, instead of a blank terminal to type blind at. Only the
	// confirmation is stdout, which is where `use` puts the same sentence.
	current := state.Current(env.ConfigRoot)
	for i, name := range names {
		if name == current {
			_, _ = fmt.Fprintf(stderr, "%d) %s (current)\n", i+1, name)
			continue
		}
		_, _ = fmt.Fprintf(stderr, "%d) %s\n", i+1, name)
	}

	in := bufio.NewReader(stdin)
	for {
		_, _ = fmt.Fprintf(stderr, "profile [1-%d]: ", len(names))

		line, readErr := in.ReadString('\n')

		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("reading the selection: %w", readErr)
		}
		ended := readErr != nil

		typed := strings.TrimSpace(line)
		if typed == "" && ended {
			return errCancelled
		}

		n, convErr := strconv.Atoi(typed)
		if convErr != nil || n < 1 || n > len(names) {
			_, _ = fmt.Fprintf(stderr, "sheesh: %q is not one of 1-%d\n", typed, len(names))

			if ended {
				return errCancelled
			}
			continue
		}

		name := names[n-1]
		if _, err := profile.Load(env.ConfigRoot, name); err != nil {
			_, _ = fmt.Fprintf(stderr, "sheesh: %v\n", err)
			if ended {
				return errCancelled
			}
			continue
		}
		return remember(name, stdout, env)
	}
}
