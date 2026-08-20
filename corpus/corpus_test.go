package corpus_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/corpus"
)

type counter struct{ n int }

var machine = fuzztape.Machine[*counter]{
	Init: func(t *fuzztape.T) *counter { return new(counter) },
	Ops: []fuzztape.Op[*counter]{
		{Name: "inc", Apply: func(t *fuzztape.T, c *counter) { c.n++ }},
		{Name: "dec", When: func(c *counter) bool { return c.n > 0 },
			Apply: func(t *fuzztape.T, c *counter) { c.n-- }},
	},
}

func TestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, data := range [][]byte{
		{},
		{0},
		[]byte("plain"),
		{0x00, 0x22, 0x5c, 0xff, '\n', '\t'},
		bytes.Repeat([]byte{0xde, 0xad}, 64),
	} {
		path, err := corpus.WriteFile(dir, data)
		if err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := corpus.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if !bytes.Equal(got, data) {
			t.Errorf("round trip = %x, want %x", got, data)
		}
	}
}

func TestSeedsMissingDirectory(t *testing.T) {
	seeds, err := corpus.Seeds(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Errorf("missing directory reported an error: %v", err)
	}
	if len(seeds) != 0 {
		t.Errorf("missing directory yielded %d seeds", len(seeds))
	}
}

func TestSeedsReadsAll(t *testing.T) {
	dir := t.TempDir()
	want := [][]byte{{1}, {2, 3}, {4, 5, 6}}
	for _, w := range want {
		if _, err := corpus.WriteFile(dir, w); err != nil {
			t.Fatal(err)
		}
	}
	seeds, err := corpus.Seeds(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != len(want) {
		t.Fatalf("read %d seeds, want %d", len(seeds), len(want))
	}
	for _, w := range want {
		found := false
		for _, s := range seeds {
			if bytes.Equal(s, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("seed %x not read back", w)
		}
	}
}

// TestAuditFindsEmptyAndDuplicate covers both stale shapes: an input
// that decodes to nothing, and two inputs that decode to the same
// sequence by different bytes.
func TestAuditFindsEmptyAndDuplicate(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, []byte{})        // no ops at all
	mustWrite(t, dir, []byte{0, 0})    // inc inc
	mustWrite(t, dir, []byte{0, 0, 0}) // inc inc inc — distinct
	// With only "inc" enabled, IntN(1) returns 0 for every boundary
	// selector, so a leading 2 decodes exactly as a leading 0: the
	// same op sequence from different bytes, which is the duplicate
	// no comparison of file contents can find.
	mustWrite(t, dir, []byte{2, 0})

	findings := corpus.Audit(t, machine, dir)
	var empty, dup int
	for _, f := range findings {
		switch {
		case strings.Contains(f.Reason, "no ops"):
			empty++
		case strings.Contains(f.Reason, "same"):
			dup++
			if f.Same == "" {
				t.Errorf("duplicate finding does not name the file it duplicates: %s", f)
			}
		}
	}
	if empty != 1 {
		t.Errorf("found %d empty seeds, want 1: %v", empty, findings)
	}
	if dup != 1 {
		t.Errorf("found %d duplicate seeds, want 1: %v", dup, findings)
	}
}

func TestAuditCleanCorpus(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, dir, []byte{0})
	mustWrite(t, dir, []byte{0, 0})
	mustWrite(t, dir, []byte{0, 0, 0})
	if findings := corpus.Audit(t, machine, dir); len(findings) != 0 {
		t.Errorf("clean corpus reported %v", findings)
	}
}

func TestAuditMissingDirectory(t *testing.T) {
	if findings := corpus.Audit(t, machine, filepath.Join(t.TempDir(), "absent")); findings != nil {
		t.Errorf("missing directory reported %v", findings)
	}
}

func TestAuditUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk"), []byte("not a corpus file"), 0o644); err != nil {
		t.Fatal(err)
	}
	findings := corpus.Audit(t, machine, dir)
	if len(findings) != 1 || !strings.Contains(findings[0].Reason, "unreadable") {
		t.Errorf("findings = %v, want one unreadable", findings)
	}
}

// TestAddSeedsTarget checks the one-line path into a fuzz target.
func FuzzWithCorpus(f *testing.F) {
	dir := f.TempDir()
	if _, err := corpus.WriteFile(dir, []byte{0, 0, 0}); err != nil {
		f.Fatal(err)
	}
	corpus.Add(f, dir)
	corpus.Add(f, filepath.Join(dir, "absent")) // must be silent
	machine.Fuzz(f)
}

func mustWrite(t *testing.T, dir string, data []byte) {
	t.Helper()
	if _, err := corpus.WriteFile(dir, data); err != nil {
		t.Fatal(err)
	}
}
