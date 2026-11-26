package main

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestDiscover covers what counts as a fuzz target and what does not.
// The near-misses matter more than the hits: a helper named Fuzzy or a
// target taking the wrong parameter must not be run as a target, because
// the go command would reject it and the whole sweep would report a
// failure that is not one.
func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a_test.go"), `package a

import "testing"

func Fuzz(f *testing.F)        {}
func FuzzOne(f *testing.F)     {}
func FuzzTwo(f *testing.F)     {}
func Fuzz_under(f *testing.F)  {}
func Fuzz1(f *testing.F)       {}
func TestNotAFuzz(t *testing.T) {}
func FuzzWrongParam(t *testing.T) {}
func FuzzNoParams()            {}
func FuzzWithResult(f *testing.F) error { return nil }
func Fuzzy(f *testing.F)       {}
func FuzzGeneric[T any](f *testing.F) {}
func FuzzMultiParam(a, b *testing.F)  {}
`)
	// A non-test file is not scanned, however Fuzz-shaped its contents.
	write(t, filepath.Join(dir, "helpers.go"), `package a

import "testing"

func FuzzInNonTestFile(f *testing.F) {}
`)
	sub := filepath.Join(dir, "sub")
	mkdir(t, sub)
	write(t, filepath.Join(sub, "b_test.go"), `package b

import "testing"

func FuzzNested(f *testing.F) {}
`)
	// A target inside testdata belongs to a fixture, not to this module.
	td := filepath.Join(dir, "testdata")
	mkdir(t, td)
	write(t, filepath.Join(td, "c_test.go"), `package c

import "testing"

func FuzzInTestdata(f *testing.F) {}
`)
	// An unparsable test file must be reported and stepped over, not
	// abandon the walk: the targets in the rest of the tree still exist.
	write(t, filepath.Join(dir, "broken_test.go"), "package a\n\nfunc oops( {\n")

	targets, gaps, err := discover(buildContext(""), dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(gaps) != 1 || !strings.Contains(gaps[0], "broken_test.go") {
		t.Errorf("gaps = %v, want the unparsable file", gaps)
	}
	var names []string
	for _, tg := range targets {
		names = append(names, tg.Name)
	}
	want := []string{"Fuzz", "FuzzOne", "FuzzTwo", "Fuzz_under", "Fuzz1", "FuzzNested"}
	slices.Sort(names)
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("discovered %v, want %v", names, want)
	}
}

// TestDiscoverSorted pins the order, because a matrix that reshuffles
// between runs makes its per-target caches useless.
func TestDiscoverSorted(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"z", "a", "m"} {
		sub := filepath.Join(dir, name)
		mkdir(t, sub)
		write(t, filepath.Join(sub, name+"_test.go"), "package "+name+`

import "testing"

func FuzzB(f *testing.F) {}
func FuzzA(f *testing.F) {}
`)
	}
	targets, _, err := discover(buildContext(""), dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(targets, func(x, y target) int {
		if c := cmp.Compare(x.Pkg, y.Pkg); c != 0 {
			return c
		}
		return cmp.Compare(x.Name, y.Name)
	}) {
		t.Errorf("targets not sorted: %v", targets)
	}
}

func TestPkgPattern(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "abs", "mod")
	for _, tc := range []struct{ base, pkg, want string }{
		{".", ".", "."},
		{".", "sub", "./sub"},
		{".", "./sub", "./sub"},
		{".", "a/b", "./a/b"},
		{"sub", "sub", "."},
		{"sub", "sub/nested", "./nested"},
		{abs, abs, "."},
		{abs, filepath.Join(abs, "sub"), "./sub"},
		{abs, filepath.Join(abs, "a", "b"), "./a/b"},
	} {
		if got := pkgPattern(tc.base, tc.pkg); got != tc.want {
			t.Errorf("pkgPattern(%q, %q) = %q, want %q", tc.base, tc.pkg, got, tc.want)
		}
	}
}

func TestGoTestArgs(t *testing.T) {
	abs := filepath.Join(string(filepath.Separator), "abs", "mod")
	for _, tc := range []struct {
		name     string
		dir      string
		target   target
		fuzztime time.Duration
		verbose  bool
		tags     string
		want     []string
	}{
		{
			name:     "default",
			dir:      ".",
			target:   target{Pkg: "sub", Name: "FuzzOne"},
			fuzztime: 30 * time.Second,
			want:     []string{"test", "-run", "^$", "-fuzz", "^FuzzOne$", "-fuzztime", "30s", "./sub"},
		},
		{
			name:     "verbose_and_tags",
			dir:      ".",
			target:   target{Pkg: ".", Name: "FuzzOne"},
			fuzztime: 5 * time.Second,
			verbose:  true,
			tags:     "extra,slow",
			want:     []string{"test", "-v", "-tags", "extra,slow", "-run", "^$", "-fuzz", "^FuzzOne$", "-fuzztime", "5s", "."},
		},
		{
			name:     "abs_dir_with_tags",
			dir:      abs,
			target:   target{Pkg: filepath.Join(abs, "pkg"), Name: "FuzzAbs"},
			fuzztime: 10 * time.Second,
			tags:     "extra",
			want:     []string{"test", "-tags", "extra", "-run", "^$", "-fuzz", "^FuzzAbs$", "-fuzztime", "10s", "./pkg"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := goTestArgs(tc.dir, tc.target, tc.fuzztime, tc.verbose, tc.tags)
			if !slices.Equal(got, tc.want) {
				t.Errorf("goTestArgs() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDiscoverEmpty(t *testing.T) {
	targets, _, err := discover(buildContext(""), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Errorf("found %v in an empty tree", targets)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverRenamedImport covers both directions of the qualifier
// question, which discovery answers from the file's own imports rather
// than by type-checking: a renamed testing import is a real target, and
// a *testing.F that does not come from the standard library is a gap
// rather than a silently counted target.
func TestDiscoverRenamedImport(t *testing.T) {
	dir := t.TempDir()
	mkdir(t, filepath.Join(dir, "renamed"))
	write(t, filepath.Join(dir, "renamed", "a_test.go"), `package a

import q "testing"

func FuzzRenamed(f *q.F) {}
`)
	mkdir(t, filepath.Join(dir, "impostor"))
	write(t, filepath.Join(dir, "impostor", "a_test.go"), `package a

import "example.com/testing"

func FuzzImpostor(f *testing.F) {}
`)
	targets, gaps, err := discover(buildContext(""), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Name != "FuzzRenamed" {
		t.Errorf("targets = %v, want just FuzzRenamed", targets)
	}
	if len(gaps) != 1 || !strings.Contains(gaps[0], "FuzzImpostor") {
		t.Errorf("gaps = %v, want FuzzImpostor reported", gaps)
	}
}

// TestDiscoverBuildTags checks that a file go test would not compile
// contributes no targets, and that -tags brings it back.
func TestDiscoverBuildTags(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a_test.go"), `//go:build extra

package a

import "testing"

func FuzzTagged(f *testing.F) {}
`)
	for _, tc := range []struct {
		tags    string
		goflags string
		want    int
	}{
		{"", "", 0},
		{"extra", "", 1},
		{"", "-tags=extra", 1},      // GOFLAGS alone is enough
		{"", "-tags extra", 1},      // space-separated -tags in GOFLAGS
		{"extra", "-tags=other", 1}, // -tags adds to GOFLAGS, does not replace it
	} {
		t.Setenv("GOFLAGS", tc.goflags)
		targets, gaps, err := discover(buildContext(tc.tags), dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(targets) != tc.want || len(gaps) != 0 {
			t.Errorf("tags=%q GOFLAGS=%q: targets %v, gaps %v, want %d targets and no gaps", tc.tags, tc.goflags, targets, gaps, tc.want)
		}
	}
}

// TestDiscoverNestedModule checks the module boundary. go test ./... does
// not descend into a nested module, so listing a target from one would
// promise work that run could never do.
func TestDiscoverNestedModule(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	mkdir(t, sub)
	write(t, filepath.Join(sub, "go.mod"), "module example.com/sub\n\ngo 1.25\n")
	write(t, filepath.Join(sub, "a_test.go"), `package a

import "testing"

func FuzzNested(f *testing.F) {}
`)
	targets, gaps, err := discover(buildContext(""), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 || len(gaps) != 0 {
		t.Errorf("targets %v, gaps %v, want nothing from a nested module", targets, gaps)
	}
}

func TestGoflagsTags(t *testing.T) {
	for _, tc := range []struct {
		goflags string
		want    []string
	}{
		{"", nil},
		{"-tags=a,b", []string{"a", "b"}},
		{"-tags a,b", []string{"a", "b"}},
		{"--tags a", []string{"a"}},
		{"-mod=mod -tags=a", []string{"a"}},
		{"-mod=mod -tags a", []string{"a"}},
		{"-tags=a -tags=b", []string{"b"}}, // last wins, as in the go command
		{"-tags=a -tags b", []string{"b"}},
		{"-tags a -tags=b", []string{"b"}},
		{"--tags=a", []string{"a"}},
		{"-tags=", nil},
		{"-tags", nil},
	} {
		t.Setenv("GOFLAGS", tc.goflags)
		if got := goflagsTags(); !slices.Equal(got, tc.want) {
			t.Errorf("GOFLAGS=%q: got %v, want %v", tc.goflags, got, tc.want)
		}
	}
}
