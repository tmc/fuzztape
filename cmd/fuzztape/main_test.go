package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestDiscover covers what counts as a fuzz target and what does not.
// The near-misses matter more than the hits: a helper named FuzzHelper
// or a target taking the wrong parameter must not be run as a target,
// because the go command would reject it and the whole sweep would
// report a failure that is not one.
func TestDiscover(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a_test.go"), `package a

import "testing"

func FuzzOne(f *testing.F)     {}
func FuzzTwo(f *testing.F)     {}
func TestNotAFuzz(t *testing.T) {}
func FuzzWrongParam(t *testing.T) {}
func FuzzNoParams()            {}
func FuzzWithResult(f *testing.F) error { return nil }
func Fuzzy(f *testing.F)       {}
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

	targets, err := discover(dir)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var names []string
	for _, tg := range targets {
		names = append(names, tg.Name)
	}
	want := []string{"FuzzOne", "FuzzTwo", "Fuzzy", "FuzzNested"}
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
	targets, err := discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(targets, func(x, y Target) int {
		if x.Pkg != y.Pkg {
			return compare(x.Pkg, y.Pkg)
		}
		return compare(x.Name, y.Name)
	}) {
		t.Errorf("targets not sorted: %v", targets)
	}
}

func TestPkgPattern(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{".", "."},
		{"sub", "./sub"},
		{"./sub", "./sub"},
		{"a/b", "./a/b"},
	} {
		if got := pkgPattern(tc.in); got != tc.want {
			t.Errorf("pkgPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDiscoverEmpty(t *testing.T) {
	targets, err := discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 0 {
		t.Errorf("found %v in an empty tree", targets)
	}
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
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
