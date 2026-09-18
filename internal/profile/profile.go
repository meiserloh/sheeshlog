// Package profile reads the files describing how to read one log style.
// A profile says how to *read* a log style and never how the output
// looks, so nothing here knows anything about the layout.
package profile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/goccy/go-yaml"

	"github.com/meiserloh/sheeshlog/internal/logline"
)

// A Profile is one log style's reading recipe. The slot and subject values are
// dotted paths into a parsed line, so `msg` and `Backup.name` are both
// addressable, as is the reserved stream-prefix field. Context fields are the
// exception and are plain top-level names; see the field below for why.
//
// The file name is the profile name, so there is deliberately no name field
// here for it to drift from.
type Profile struct {
	// Format states how this stream's payload is decoded into fields. Default: JSON.
	Format logline.Format `yaml:"format"`
	// Time, Level and Message are the paths filling those three slots.
	Time    string `yaml:"time"`
	Level   string `yaml:"level"`
	Message string `yaml:"message"`
	// TimeFormat states how the time value is read, for the styles where
	// detection would have to guess. Saying nothing means detection, which is
	// what nearly every profile should say.
	TimeFormat logline.TimeFormat `yaml:"timeformat"`
	// Subjects are tried in the order written; the first one present fills the
	// subject slot, so a Backup, a Restore and a BackupSchedule line can all
	// show the thing they are about.
	Subjects []string `yaml:"subject"`
	// ContextFields is a deny-list: these are suppressed from the extras
	// because their value repeats on nearly every line. It is a deny-list
	// rather than an allow-list so that a field the profile has never heard of
	// still shows up, which is how new information gets noticed.
	//
	// These are top-level field names, not dotted paths: extras render a
	// top-level value whole, so there is no half of one to suppress.
	ContextFields []string `yaml:"context"`
	// Noise are the rules discarding a whole line as uninteresting. They are
	// one shape only — a field and a regex — so that there is one thing to
	// learn and one thing to document.
	Noise []NoiseRule `yaml:"noise"`
}

// A NoiseRule discards a line whose named field matches, whatever the line's
// level: startup chatter is not made interesting by being logged as a warning.
type NoiseRule struct {
	// Field is a dotted path, addressed exactly as a slot's is, so a nested
	// field and the reserved stream-prefix field are both matchable.
	Field string `yaml:"field"`
	// Pattern is the regex as written, kept for error messages and for anyone
	// printing a profile back.
	Pattern string `yaml:"matches"`
	// Regexp is Pattern compiled. Load fills it, so a broken regex is a
	// start-up failure rather than something discovered halfway down a tail.
	Regexp *regexp.Regexp `yaml:"-"`
}

// Dir is where a config root keeps its profiles. One file per profile, so
// handing a colleague a profile is a copy. The directory is named for the
// command rather than the module, so that someone who types sheesh finds its
// configuration where XDG practice says to look for it.
func Dir(configRoot string) string {
	return filepath.Join(configRoot, "sheesh", "profiles")
}

// ext is the suffix a profile file carries. It is one constant because the
// loader and the lister have to agree on which files are profiles.
const ext = ".yaml"

// ValidName refuses anything that is not a plain file name. The file name is
// the profile name, so a name carrying a separator is a typo or an attempt to
// read a file from somewhere else; either way it is not a profile of the
// user's, and joining it onto the profiles directory would silently escape.
func ValidName(name string) error {
	// Control characters are refused alongside the separators: no file system
	// holds them, so a name carrying one arrived from something that mangled it
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("invalid profile name %q (a profile name is a file name, not a path)", name)
	}
	return nil
}

// A NotFoundError reports a profile name with no file behind it. It is a named
// type rather than a plain error so that a caller can tell "there is no such
// profile" — which a remembered name that has since been deleted degrades from
// — apart from "that profile is broken", which nothing should degrade from.
type NotFoundError struct {
	Name, Path string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no profile named %q (looked for %s)", e.Name, e.Path)
}

// An UnreadableError reports a profile whose file is there but could not be
// read: a directory in its place, a permission the tool does not have, a failing
// disk. It is grouped with NotFoundError rather than with the parse failures
// because neither says anything about what the user wrote — there is nothing in
// the profile to go and fix — so a remembered profile may degrade from either.
type UnreadableError struct {
	Name, Path string
	Err        error
}

func (e *UnreadableError) Error() string {
	return fmt.Sprintf("reading profile %q (%s): %v", e.Name, e.Path, e.Err)
}

func (e *UnreadableError) Unwrap() error { return e.Err }

// List returns the names of the profiles in the config root, sorted. A config
// root with no profiles directory yet is not an error: nobody has written a
// profile, which is the ordinary state of a fresh install and reads as an empty
// list.
func List(configRoot string) ([]string, error) {
	entries, err := os.ReadDir(Dir(configRoot))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading profiles: %w", err)
	}

	var names []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ext {
			continue
		}
		// ReadDir does not follow links, so a symlink named like a profile
		// arrives here looking like a regular file whatever it points at. Only
		// the entries that are not already regular files pay for the extra
		// stat, and one that cannot be resolved is not a profile either.
		if !e.Type().IsRegular() {
			fi, err := os.Stat(filepath.Join(Dir(configRoot), e.Name()))
			if err != nil || !fi.Mode().IsRegular() {
				continue
			}
		}
		name := strings.TrimSuffix(e.Name(), ext)
		if ValidName(name) != nil {
			continue
		}
		names = append(names, name)
	}
	// Sorted rather than left in directory order, so that the list a user reads
	// and the list a script diffs are both stable.
	sort.Strings(names)
	return names, nil
}

// Load reads the named profile. Both failures name what the user has to go and
// look at: the path that was not there, or the file and the problem in it.
// Unknown keys are refused rather than ignored, because a typo in a
// hand-written profile that silently does nothing is the failure this whole
// tool exists to stop being silent.
func Load(configRoot, name string) (Profile, error) {
	if err := ValidName(name); err != nil {
		return Profile{}, err
	}
	path := filepath.Join(Dir(configRoot), name+ext)

	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Profile{}, &NotFoundError{Name: name, Path: path}
	}
	if err != nil {
		return Profile{}, &UnreadableError{Name: name, Path: path, Err: err}
	}

	var p Profile
	if err := yaml.UnmarshalWithOptions(b, &p, yaml.Strict()); err != nil {
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	// Refused rather than ignored: a dotted context field would match no
	// top-level key and quietly suppress nothing, which looks exactly like a
	// profile that works.
	for _, f := range p.ContextFields {
		if strings.Contains(f, ".") {
			return Profile{}, fmt.Errorf("%s: context field %q is a dotted path, but context fields are top-level field names", path, f)
		}
	}
	// Refused by name rather than left to decode nothing: a profile naming a
	// format sheesh does not read would pass every one of its lines through
	// verbatim, which looks exactly like a stream sheesh cannot help with.
	format, ok := logline.ParseFormat(string(p.Format))
	if !ok {
		return Profile{}, fmt.Errorf("%s: format %q is not a format this reads (want %s)", path, p.Format, strings.Join(logline.FormatNames(), ", "))
	}
	p.Format = format
	// Refused by name rather than quietly detecting anyway: a profile stating a
	// format has a reason to, and a typo that silently falls back to detection
	// would look like a working profile right up until a timestamp is ambiguous.
	f, ok := logline.ParseTimeFormat(string(p.TimeFormat))
	if !ok {
		return Profile{}, fmt.Errorf("%s: timeformat %q is not a format this reads (want %s)", path, p.TimeFormat, strings.Join(logline.TimeFormatNames(), ", "))
	}
	p.TimeFormat = f
	// Stating how to read a value the profile never names is the same silent
	// no-op the validations around it refuse: it reads as a profile that has
	// thought about its timestamps, and it does nothing whatsoever.
	if p.TimeFormat != logline.TimeAuto && p.Time == "" {
		return Profile{}, fmt.Errorf("%s: timeformat %q is stated but no time field is named, so nothing would read it", path, p.TimeFormat)
	}

	// Compiled here rather than at first use: a profile with a broken regex is
	// broken whether or not a matching line happens to arrive, and finding out
	// mid-tail would mean a stream that renders fine until it suddenly does not.
	for i := range p.Noise {
		r := &p.Noise[i]
		// An empty field would resolve to nothing on every line, so the rule
		// would silently discard nothing and look like a rule that works.
		if r.Field == "" {
			return Profile{}, fmt.Errorf("%s: noise rule %d has no field", path, i+1)
		}
		// The worse half of the same mistake: the empty regex compiles happily
		// and matches everything, so a forgotten `matches` would discard every
		// line carrying the field and leave a live tail silently blank.
		if r.Pattern == "" {
			return Profile{}, fmt.Errorf("%s: noise rule %d (field %q) has no regex", path, i+1, r.Field)
		}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return Profile{}, fmt.Errorf("%s: noise rule %d (field %q): invalid regex %q: %w", path, i+1, r.Field, r.Pattern, err)
		}
		r.Regexp = re
	}
	return p, nil
}
