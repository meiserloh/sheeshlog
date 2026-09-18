package profile

import (
	_ "embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// exampleName is the profile the example is written as. It is the operator log
// style the tool was built against, so the example is a profile that actually
// reads a real service's logs rather than an illustration of the fields.
const exampleName = "operator"

// example is the file itself, embedded rather than generated, so that the
// profile the tool writes on a first run and the profile the tests run are the
// same bytes.
//
//go:embed examples/operator.yaml
var example []byte

// WriteExample writes the example profile on a first run, returning the path it
// wrote or the empty string when it wrote nothing. A first run is one that finds no
// profiles directory at all
func WriteExample(configRoot string) (string, error) {
	if configRoot == "" {
		return "", nil
	}

	dir := Dir(configRoot)
	if _, err := os.Stat(dir); err == nil || !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, exampleName+ext)

	if err := writeAll(path, example); err != nil {
		// Best effort, and deliberately unchecked: A failed cleanup has no recovery of
		// its own. What it leaves behind is a half-written profile, which the
		// next load reports loudly.
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// writeAll creates the file, writes the example to it and closes it, reporting
// the first failure of any of the three. The close is part of the write: a
// buffered filesystem can fail there and nowhere else. Opening here rather than
// in the caller keeps the file's whole life in one function, so every path out
// of it visibly closes.
func writeAll(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
