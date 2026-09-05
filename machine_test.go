package fuzztape

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCorpusEntries checks that only a missing corpus directory counts as an
// empty corpus. A regular file where the directory belongs used to be
// indistinguishable from absence, so Run replayed nothing and still passed.
func TestCorpusEntries(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if entries, err := corpusEntries(missing); err != nil || entries != nil {
		t.Errorf("corpusEntries(missing) = %v, %v; want nil, nil", entries, err)
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("go test fuzz v1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := corpusEntries(file); err == nil {
		t.Error("corpusEntries(regular file) = nil error; want the read to fail")
	}

	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := corpusEntries(real)
	if err != nil || len(entries) != 1 {
		t.Errorf("corpusEntries(dir) = %d entries, %v; want 1, nil", len(entries), err)
	}
}
