// Package fuzzfile reads and writes go test fuzz v1 corpus files holding a
// single []byte value.
package fuzzfile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Header is the first line of every go test fuzz corpus file.
const Header = "go test fuzz v1"

// ReadDir lists the corpus entries in dir. A missing directory yields no
// entries and a nil error; every other failure (such as a regular file at dir)
// is returned.
func ReadDir(dir string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// Write saves data as a corpus file in dir, named by its SHA-256 digest,
// and returns the path.
func Write(dir string, data []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf("%s\n[]byte(%q)\n", Header, data)
	path := filepath.Join(dir, fmt.Sprintf("fuzztape-%x", sha256.Sum256(data))[:24])
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// Read reads one corpus file holding a single []byte value. It reports an
// error for the other shapes go test's format allows — a different version,
// or a record of several typed values — rather than guessing at them.
func Read(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) != 2 || strings.TrimSpace(lines[0]) != Header {
		return nil, fmt.Errorf("%s: not a %s corpus file holding one []byte", path, Header)
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
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return []byte(s), nil
}
