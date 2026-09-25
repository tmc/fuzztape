package tlaplus

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestTLC checks the evaluator against TLC, the reference
// implementation. It evaluates each expression of evalTests, writes a
// module asserting that TLC computes the same value,
//
//	ASSUME X3 = {1, 2, 3}
//
// and runs TLC on it. The printed value goes back through TLC's
// parser, so the test checks the printer as well as the evaluator.
//
// It runs only when TLA2TOOLS_JAR names tla2tools.jar and java is on
// PATH (or JAVA names it).
func TestTLC(t *testing.T) {
	var b strings.Builder
	b.WriteString("---- MODULE Diff ----\nEXTENDS Integers, Sequences, FiniteSets, TLC\nVARIABLE dummy\nInit == dummy = 0\nNext == UNCHANGED dummy\n")
	defined := make(map[string]bool)
	for _, tt := range evalTests {
		if tt.defs != "" && !defined[tt.defs] {
			defined[tt.defs] = true
			b.WriteString(tt.defs + "\n")
		}
	}
	lineCase := make(map[int]string)
	for i, tt := range evalTests {
		got, err := evalString(tt.defs, tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		fmt.Fprintf(&b, "X%d == %s\n", i, tt.in)
		lineCase[strings.Count(b.String(), "\n")+1] = fmt.Sprintf("%s: %s = %s", tt.name, tt.in, got)
		fmt.Fprintf(&b, "ASSUME X%d = %s\n", i, got)
	}
	b.WriteString("====\n")
	runTLC(t, b.String(), "", lineCase)
}

// TestTLCConstants does for constTests what TestTLC does for
// evalTests, with the model's constants set to the Go values bound
// with With, printed in TLA+ syntax. The config file can say C = v only
// for a few kinds of v, including model values and sets of them, so any
// other value is defined in the module and substituted: C <- C_value.
func TestTLCConstants(t *testing.T) {
	var b, cfg strings.Builder
	b.WriteString(constModule)
	cfg.WriteString("CONSTANTS\n")
	for _, name := range slices.Sorted(maps.Keys(constValues)) {
		v, err := fromGo(constValues[name])
		if err != nil {
			t.Fatal(err)
		}
		if hasModelValue(v) {
			fmt.Fprintf(&cfg, "    %s = %s\n", name, format(v))
			continue
		}
		fmt.Fprintf(&b, "%s_value == %s\n", name, format(v))
		fmt.Fprintf(&cfg, "    %s <- %s_value\n", name, name)
	}
	b.WriteString("VARIABLE dummy\nInit == dummy = 0\nNext == UNCHANGED dummy\n")
	lineCase := make(map[int]string)
	for i, tt := range constTests {
		got, err := evalConst(tt.in)
		if err != nil {
			t.Fatalf("%s: %v", tt.in, err)
		}
		fmt.Fprintf(&b, "X%d == %s\n", i, tt.in)
		lineCase[strings.Count(b.String(), "\n")+1] = fmt.Sprintf("%s = %s", tt.in, got)
		fmt.Fprintf(&b, "ASSUME X%d = %s\n", i, got)
	}
	b.WriteString("====\n")
	runTLC(t, b.String(), cfg.String(), lineCase)
}

func hasModelValue(v value) bool {
	switch v := v.(type) {
	case ModelValue:
		return true
	case *setValue:
		return slices.ContainsFunc(v.elems, hasModelValue)
	case *fcnValue:
		return slices.ContainsFunc(v.keys, hasModelValue) || slices.ContainsFunc(v.vals, hasModelValue)
	}
	return false
}

// runTLC model-checks module Diff, whose source is mod, with cfg added
// to its model, and reports the case on the line of each assumption
// TLC finds false.
func runTLC(t *testing.T, mod, cfg string, lineCase map[int]string) {
	t.Helper()
	jar := os.Getenv("TLA2TOOLS_JAR")
	if jar == "" {
		t.Skip("TLA2TOOLS_JAR not set")
	}
	java := os.Getenv("JAVA")
	if java == "" {
		var err error
		if java, err = exec.LookPath("java"); err != nil {
			t.Skip("java not found")
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Diff.tla"), []byte(mod), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Diff.cfg"), []byte("INIT Init\nNEXT Next\n"+cfg), 0o666); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(java, "-cp", jar, "tlc2.TLC", "-workers", "1", "-metadir", filepath.Join(dir, "states"), "Diff")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil && strings.Contains(string(out), "No error has been found") {
		return
	}
	// TLC stops at the first false assumption.
	re := regexp.MustCompile(`line (\d+), col \d+ to line \d+, col \d+ of module Diff`)
	for _, m := range re.FindAllStringSubmatch(string(out), -1) {
		n, _ := strconv.Atoi(m[1])
		if c, ok := lineCase[n]; ok {
			t.Errorf("TLC disagrees: %s", c)
		}
	}
	t.Fatalf("tlc: %v\n%s", err, out)
}
