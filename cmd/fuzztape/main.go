// Fuzztape runs every fuzz target in a module.
//
// Usage:
//
//	fuzztape list [dir]
//	fuzztape run [-time d] [-v] [dir]
//	fuzztape matrix [dir]
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
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	log := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "fuzztape: "+format+"\n", args...)
	}
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]

	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fuzztime := fs.Duration("time", 30*time.Second, "fuzzing budget per target")
	verbose := fs.Bool("v", false, "pass -v to go test")
	fs.Parse(args)

	dir := "."
	if rest := fs.Args(); len(rest) > 0 {
		dir = rest[0]
	}
	targets, err := discover(dir)
	if err != nil {
		log("%v", err)
		os.Exit(1)
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
		if !run(targets, *fuzztime, *verbose) {
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
	fuzztape list [packages]
	fuzztape run [-time d] [-v] [packages]
	fuzztape matrix [packages]
`)
	os.Exit(2)
}

// A Target is one fuzz target: the package directory holding it and the
// function name. The JSON field names are what a GitHub Actions matrix
// entry needs.
type Target struct {
	Pkg  string `json:"pkg"`
	Name string `json:"name"`
}

// discover walks dir and returns every fuzz target it finds, sorted.
//
// It parses the test files rather than building them, which keeps it
// working on a tree that does not currently compile — the state a
// repository is in precisely when someone wants to know what the fuzz
// targets are.
func discover(dir string) ([]Target, error) {
	var targets []Target
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || name == "vendor" ||
				(len(name) > 1 && (name[0] == '.' || name[0] == '_') && path != dir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			// A test file that does not parse has no discoverable
			// targets, but it is not a reason to abandon the walk.
			fmt.Fprintf(os.Stderr, "fuzztape: %s: %v\n", path, err)
			return nil
		}
		pkg := filepath.Dir(path)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && isFuzzTarget(fn) {
				targets = append(targets, Target{Pkg: filepath.ToSlash(pkg), Name: fn.Name.Name})
			}
		}
		return nil
	})
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Pkg != targets[j].Pkg {
			return targets[i].Pkg < targets[j].Pkg
		}
		return targets[i].Name < targets[j].Name
	})
	return targets, err
}

// isFuzzTarget reports whether fn has the shape go test recognizes:
// func FuzzXxx(f *testing.F), with no receiver and no results.
func isFuzzTarget(fn *ast.FuncDecl) bool {
	if fn.Recv != nil || fn.Name == nil || !strings.HasPrefix(fn.Name.Name, "Fuzz") {
		return false
	}
	if fn.Type.Results != nil || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "testing" && sel.Sel.Name == "F"
}

// pkgPattern turns a directory into the relative package pattern the
// go command expects.
func pkgPattern(dir string) string {
	dir = filepath.ToSlash(dir)
	if dir == "." || strings.HasPrefix(dir, "./") || strings.HasPrefix(dir, "/") {
		return dir
	}
	return "./" + dir
}

// run fuzzes every target in turn and reports whether all passed.
func run(targets []Target, fuzztime time.Duration, verbose bool) bool {
	type result struct {
		Target
		err error
		dur time.Duration
	}
	results := make([]result, 0, len(targets))
	for i, t := range targets {
		fmt.Fprintf(os.Stderr, "fuzztape: [%d/%d] %s %s for %v\n", i+1, len(targets), t.Pkg, t.Name, fuzztime)
		args := []string{"test"}
		if verbose {
			args = append(args, "-v")
		}
		args = append(args,
			"-run", "^$", // no ordinary tests, only the fuzz target
			"-fuzz", "^"+t.Name+"$",
			"-fuzztime", fuzztime.String(),
			pkgPattern(t.Pkg),
		)
		start := time.Now()
		cmd := exec.Command("go", args...)
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
