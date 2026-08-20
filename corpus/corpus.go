// Package corpus loads seed inputs and reports the ones that have
// stopped earning their place.
//
// Two problems, one file format. Good seeds are hard to invent and easy
// to obtain: a project that already has wire vectors from another
// implementation is holding better starting material than a fuzzer will
// find on its own, and [Add] gets it into a target with one line. And a
// corpus rots — an input saved for an invariant that has since changed,
// or for an op that has been renamed, still costs execs on every run
// while proving nothing. [Audit] names those.
//
// The file format is go test's own, so files written by
// [fuzztape.Machine.Run], by go test -fuzz, or by hand all read the
// same way.
package corpus

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tmc/fuzztape"
)

// header is the first line of every go test fuzz corpus file.
const header = "go test fuzz v1"

// Seeds reads every corpus file in dir and returns their inputs, sorted
// by file name so a run is reproducible. A missing directory yields no
// seeds and no error: a corpus that has not been created yet is the
// normal state of a new target, not a failure.
func Seeds(dir string) ([][]byte, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var seeds [][]byte
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		seeds = append(seeds, data)
	}
	return seeds, nil
}

// Add reads dir and seeds f with everything in it. A missing directory
// is skipped silently; an unreadable file fails the target, because a
// corpus file that cannot be parsed is a corpus file that is not being
// tested.
func Add(f *testing.F, dir string) {
	f.Helper()
	seeds, err := Seeds(dir)
	if err != nil {
		f.Fatalf("corpus: %v", err)
	}
	for _, s := range seeds {
		f.Add(s)
	}
}

// ReadFile reads one corpus file holding the single []byte value a
// [fuzztape.Machine] target takes. It reports an error for the other
// shapes go test's format allows — a different version, or a record of
// several typed values — rather than guessing at them.
func ReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != header {
		return nil, fmt.Errorf("%s: not a %s corpus file holding one []byte", path, header)
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(lines[1]), "[]byte(")
	if !ok {
		return nil, fmt.Errorf("%s: corpus value is not a []byte", path)
	}
	v, ok = strings.CutSuffix(v, ")")
	if !ok {
		return nil, fmt.Errorf("%s: malformed corpus value", path)
	}
	s, err := strconv.Unquote(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	return []byte(s), nil
}

// WriteFile saves data as a corpus file in dir, named for its content,
// and returns the path.
func WriteFile(dir string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("corpus-%x", hash(data)))
	content := fmt.Sprintf("%s\n[]byte(%q)\n", header, data)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// A Finding is one corpus file that is not pulling its weight.
type Finding struct {
	Path   string
	Reason string
	// Same, for a duplicate, is the file it duplicates.
	Same string
}

func (f Finding) String() string {
	if f.Same != "" {
		return fmt.Sprintf("%s: %s (%s)", f.Path, f.Reason, filepath.Base(f.Same))
	}
	return fmt.Sprintf("%s: %s", f.Path, f.Reason)
}

// Audit replays every corpus file in dir against m and reports the ones
// that no longer distinguish anything: inputs that decode to no ops at
// all, and inputs that decode to an op sequence another file already
// covers.
//
// It judges seeds by the sequence they decode to, not by their bytes,
// which is the distinction that matters after a machine changes. Adding
// an op renumbers the selection space, so every previously saved input
// still decodes — to something else. Some of those become duplicates of
// each other, and they are invisible to any check on the file contents.
//
// Audit reports; it does not delete. Which of two equivalent seeds to
// keep, and whether a seed with no ops is documenting an edge case, are
// judgment calls.
func Audit[S any](t *testing.T, m fuzztape.Machine[S], dir string) []Finding {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Errorf("corpus: %v", err)
		return nil
	}

	var findings []Finding
	seen := map[string]string{} // op sequence -> first file with it
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := ReadFile(path)
		if err != nil {
			findings = append(findings, Finding{Path: path, Reason: "unreadable: " + err.Error()})
			continue
		}
		ops := m.Trace(t, data)
		if len(ops) == 0 {
			findings = append(findings, Finding{Path: path, Reason: "decodes to no ops"})
			continue
		}
		key := strings.Join(ops, "\x00")
		if first, dup := seen[key]; dup {
			findings = append(findings, Finding{
				Path:   path,
				Reason: fmt.Sprintf("decodes to the same %d ops as another seed", len(ops)),
				Same:   first,
			})
			continue
		}
		seen[key] = path
	}
	return findings
}

// hash is a content hash for corpus file names. It is FNV-1a rather
// than a cryptographic hash because the only requirement is that two
// different inputs rarely collide in a directory listing.
func hash(data []byte) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for _, b := range data {
		h ^= uint64(b)
		h *= prime
	}
	return h
}
