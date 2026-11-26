// Fuzztape runs every fuzz target in a module.
//
// Usage:
//
//	fuzztape list [-tags list] [dir]
//	fuzztape run [-tags list] [-time d] [-v] [dir]
//	fuzztape matrix [-tags list] [dir]
//
// The go command fuzzes exactly one target, in one package, per
// invocation: -fuzz takes a regular expression matched against the
// targets of a single package, and go test refuses more than one
// package with it. So "fuzz everything for ten minutes" is not a
// command, it is a loop someone has to write, and "go test -fuzz ./..."
// does not report that error — it silently fuzzes one arbitrary target
// forever.
//
// This command is that loop. It finds the targets by parsing the test
// files, so it needs no build, and gives each its own budget.
//
// Discovery parses the test files rather than building them, so it
// works on a tree that does not compile — the state a repository is in
// precisely when someone wants to know what its fuzz targets are. It is
// one path, and it is loud about what that path cannot see.
//
// It matches func FuzzXxx(f *testing.F) against the name "testing" is
// bound to in that file, so a renamed import — func FuzzX(f *q.F) after
// import q "testing" — is found, and a *testing.F naming some other
// local package is reported rather than counted. It honors build
// constraints through go/build, so a file go test would not compile
// contributes no targets; -tags adds tags, on top of any in GOFLAGS. A
// subdirectory with its own go.mod is a different module and is not
// walked.
//
// Anything it cannot decide is written to stderr and makes the command
// exit non-zero, because a fuzzer that quietly skips a target reports
// success for work it did not do.
//
// List prints the targets it found, one "package target" per line.
//
// Run fuzzes each target in turn for -time, and reports a table at the
// end. It exits non-zero if any target failed, and keeps going after a
// failure rather than stopping at the first: which targets a change
// broke is more useful than which one it broke first.
//
// Matrix prints the targets as JSON for a GitHub Actions matrix, so a
// nightly workflow can fan the targets across runners and sidestep the
// one-target-per-invocation limit entirely:
//
//	strategy:
//	  fail-fast: false
//	  matrix:
//	    include: ${{ fromJSON(needs.discover.outputs.targets) }}
//
// Cache $GOCACHE/fuzz between runs, keyed per target. An uncached
// nightly fuzzer restarts from nothing every night.
package main

import (
	"cmp"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

func main() {
	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "fuzztape: "+format+"\n", args...)
	}
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "list", "matrix", "run":
	default:
		usage()
	}

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	tags := fs.String("tags", "", "comma-separated build tags, added to those in GOFLAGS")
	var fuzztime *time.Duration
	var verbose *bool
	if cmd == "run" {
		fuzztime = fs.Duration("time", 30*time.Second, "fuzzing budget per target")
		verbose = fs.Bool("v", false, "pass -v to go test")
	}
	fs.Parse(args)

	dir := "."
	switch rest := fs.Args(); len(rest) {
	case 0:
	case 1:
		dir = rest[0]
	default:
		usage()
	}

	targets, gaps, err := discover(buildContext(*tags), dir)
	if err != nil {
		log("%v", err)
		os.Exit(1)
	}
	for _, g := range gaps {
		log("%s", g)
	}
	if len(targets) == 0 {
		log("no fuzz targets under %s", dir)
		os.Exit(1)
	}

	switch cmd {
	case "list":
		for _, t := range targets {
			fmt.Printf("%s %s\n", t.Pkg, t.Name)
		}
	case "matrix":
		b, err := json.Marshal(targets)
		if err != nil {
			log("%v", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
	case "run":
		if !run(dir, targets, *fuzztime, *verbose, *tags) {
			os.Exit(1)
		}
	}
	if len(gaps) > 0 {
		// The work that could be done was done and reported. Exiting
		// non-zero is what keeps a gap from reading as a clean run.
		log("%d target(s) could not be decided; see above", len(gaps))
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
	fuzztape list [-tags list] [dir]
	fuzztape run [-tags list] [-time d] [-v] [dir]
	fuzztape matrix [-tags list] [dir]
`)
	os.Exit(2)
}

// A target is one fuzz target: the package directory holding it and the
// function name. The JSON field names are what a GitHub Actions matrix
// entry needs.
type target struct {
	Pkg  string `json:"pkg"`
	Name string `json:"name"`
}

// discover walks dir and returns every fuzz target it finds, sorted.
//
// It reports the targets it is sure of, and separately the gaps: the
// places where a target may exist that it could not decide about. A gap
// is not an error — the walk continues and the other targets are still
// reported — but the caller is expected to print them and exit non-zero,
// because a target skipped in silence is indistinguishable from one that
// passed.
func discover(ctxt *build.Context, dir string) ([]target, []string, error) {
	var targets []target
	var gaps []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "testdata" || name == "vendor" ||
				(len(name) > 1 && (name[0] == '.' || name[0] == '_') && path != dir) {
				return filepath.SkipDir
			}
			// A directory with its own go.mod is a different module.
			// go test ./... stops at that boundary, so a target below
			// it would be listed and then never run.
			if path != dir {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// Build constraints decide whether go test compiles this file
		// at all. MatchFile answers from the name and the //go:build
		// line, with no build and no type-checking.
		switch ok, err := ctxt.MatchFile(filepath.Dir(path), filepath.Base(path)); {
		case err != nil:
			gaps = append(gaps, fmt.Sprintf("%s: build constraints: %v", path, err))
			return nil
		case !ok:
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			// A test file that does not parse has no discoverable
			// targets, but it is not a reason to abandon the walk.
			gaps = append(gaps, fmt.Sprintf("%s: %v", path, err))
			return nil
		}
		pkg := filepath.ToSlash(filepath.Dir(path))
		testing := testingName(f)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			switch name := fuzzParamPkg(fn); {
			case name == "":
			case testing != "" && name == testing:
				targets = append(targets, target{Pkg: pkg, Name: fn.Name.Name})
			case name == "testing":
				// *testing.F where testing is not the standard
				// library: a local package by that name, or a
				// dot-import. go test would not accept it as a
				// target, but saying so beats silence.
				gaps = append(gaps, fmt.Sprintf("%s: %s takes *testing.F but this file does not import \"testing\" under that name", path, fn.Name.Name))
			}
		}
		return nil
	})
	slices.SortFunc(targets, func(a, b target) int {
		if c := cmp.Compare(a.Pkg, b.Pkg); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	slices.Sort(gaps)
	return targets, gaps, err
}

// testingName returns the name the standard library's testing package is
// bound to in f, which is "testing" unless the import was renamed. It
// returns "" if f does not import testing, or imports it as _ or . —
// neither of which can spell *testing.F.
func testingName(f *ast.File) string {
	for _, spec := range f.Imports {
		if spec.Path.Value != `"testing"` {
			continue
		}
		if spec.Name == nil {
			return "testing"
		}
		if name := spec.Name.Name; name != "_" && name != "." {
			return name
		}
	}
	return ""
}

// isFuzzName reports whether name matches cmd/go's isTest(name, "Fuzz") rule:
// "Fuzz" itself, or "Fuzz" followed by a rune that is not a lowercase letter.
func isFuzzName(name string) bool {
	if !strings.HasPrefix(name, "Fuzz") {
		return false
	}
	if len(name) == len("Fuzz") {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len("Fuzz"):])
	return !unicode.IsLower(r)
}

// fuzzParamPkg reports the package qualifier of fn's sole parameter if fn
// has the shape go test recognizes for a fuzz target — func FuzzXxx(f
// *pkg.F), with no receiver, no type parameters, and no results — and ""
// if it does not. The caller decides whether that qualifier names the
// testing package.
func fuzzParamPkg(fn *ast.FuncDecl) string {
	if fn.Recv != nil || fn.Name == nil || !isFuzzName(fn.Name.Name) {
		return ""
	}
	if fn.Type.TypeParams != nil && fn.Type.TypeParams.NumFields() > 0 {
		return ""
	}
	if fn.Type.Results != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return ""
	}
	field := fn.Type.Params.List[0]
	if len(field.Names) > 1 {
		return ""
	}
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "F" {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name
}

// buildContext returns the context discovery evaluates build constraints
// in: the default one, plus the tags in GOFLAGS and then the -tags flag,
// so that fuzztape sees the same files as a go test run in the same
// environment.
func buildContext(tags string) *build.Context {
	ctxt := build.Default
	ctxt.BuildTags = append(goflagsTags(), splitTags(tags)...)
	return &ctxt
}

// goflagsTags returns the tags set by -tags in GOFLAGS. GOFLAGS is a
// space-separated flag list, so the tags are the value of the last -tags
// in it, matching how the go command resolves a repeated flag.
func goflagsTags() []string {
	var tags []string
	fields := strings.Fields(os.Getenv("GOFLAGS"))
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if v, ok := strings.CutPrefix(f, "-tags="); ok {
			tags = splitTags(v)
		} else if v, ok := strings.CutPrefix(f, "--tags="); ok {
			tags = splitTags(v)
		} else if (f == "-tags" || f == "--tags") && i+1 < len(fields) {
			i++
			tags = splitTags(fields[i])
		}
	}
	return tags
}

// splitTags splits a -tags value, which is comma-separated.
func splitTags(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

// pkgPattern turns a target package directory into the relative package
// pattern the go command expects when run from base.
func pkgPattern(base, pkg string) string {
	if base != "" {
		if rel, err := filepath.Rel(base, pkg); err == nil {
			pkg = rel
		}
	}
	pkg = filepath.ToSlash(pkg)
	if pkg == "." || pkg == ".." || strings.HasPrefix(pkg, "./") || strings.HasPrefix(pkg, "../") || filepath.IsAbs(pkg) {
		return pkg
	}
	return "./" + pkg
}

// goTestArgs constructs the arguments to "go" for fuzzing target t from dir.
func goTestArgs(dir string, t target, fuzztime time.Duration, verbose bool, tags string) []string {
	args := []string{"test"}
	if verbose {
		args = append(args, "-v")
	}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	args = append(args,
		"-run", "^$", // no ordinary tests, only the fuzz target
		"-fuzz", "^"+t.Name+"$",
		"-fuzztime", fuzztime.String(),
		pkgPattern(dir, t.Pkg),
	)
	return args
}

// run fuzzes every target in turn and reports whether all passed.
func run(dir string, targets []target, fuzztime time.Duration, verbose bool, tags string) bool {
	type result struct {
		target
		err error
		dur time.Duration
	}
	results := make([]result, 0, len(targets))
	for i, t := range targets {
		fmt.Fprintf(os.Stderr, "fuzztape: [%d/%d] %s %s for %v\n", i+1, len(targets), t.Pkg, t.Name, fuzztime)
		start := time.Now()
		cmd := exec.Command("go", goTestArgs(dir, t, fuzztime, verbose, tags)...)
		if dir != "" {
			cmd.Dir = dir
		}
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		results = append(results, result{t, cmd.Run(), time.Since(start).Round(time.Millisecond)})
	}

	ok := true
	fmt.Fprintln(os.Stderr, "\nfuzztape: summary")
	for _, r := range results {
		status := "ok"
		if r.err != nil {
			status, ok = "FAIL", false
		}
		fmt.Fprintf(os.Stderr, "\t%-6s %s %s (%v)\n", status, r.Pkg, r.Name, r.dur)
	}
	return ok
}
