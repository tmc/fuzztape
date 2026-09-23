package tlaplus

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Diff.tla"), []byte(b.String()), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Diff.cfg"), []byte("INIT Init\nNEXT Next\n"), 0o666); err != nil {
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
