package fuzztape

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestTapeZero(t *testing.T) {
	tape := New(nil)
	if !tape.Done() {
		t.Error("empty tape not Done")
	}
	if got := tape.Byte(); got != 0 {
		t.Errorf("Byte() = %d, want 0", got)
	}
	if tape.Bool() {
		t.Error("Bool() = true, want false")
	}
	if got := tape.Uint64(); got != 0 {
		t.Errorf("Uint64() = %d, want 0", got)
	}
	if got := tape.IntN(100); got != 0 {
		t.Errorf("IntN(100) = %d, want 0", got)
	}
	if got := tape.Bytes(16); len(got) != 0 {
		t.Errorf("Bytes(16) = %d bytes, want 0", len(got))
	}
	if got := Pick(tape, []string{"a", "b"}); got != "a" {
		t.Errorf("Pick = %q, want %q (first option)", got, "a")
	}
}

func TestTapeIntN(t *testing.T) {
	// Every selector byte, with a fixed uniform payload, stays in range
	// for a spread of n, and decoding is deterministic.
	for _, n := range []int{1, 2, 3, 7, 100, 1 << 20} {
		for sel := 0; sel < 256; sel++ {
			data := append([]byte{byte(sel)}, 0xde, 0xad, 0xbe, 0xef, 1, 2, 3, 4)
			got := New(data).IntN(n)
			if got < 0 || got >= n {
				t.Fatalf("IntN(%d) with selector %d = %d, out of range", n, sel, got)
			}
			if again := New(data).IntN(n); again != got {
				t.Fatalf("IntN(%d) not deterministic: %d then %d", n, got, again)
			}
		}
	}
	// The boundary selectors hit the documented values.
	for sel, want := range map[byte]int{0: 0, 1: 1, 2: 99, 3: 64} {
		if got := New([]byte{sel}).IntN(100); got != want {
			t.Errorf("IntN(100) with selector %d = %d, want %d", sel, got, want)
		}
	}
}

func TestTapeIntNPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("IntN(0) did not panic")
		}
	}()
	New(nil).IntN(0)
}

func TestTapeBytes(t *testing.T) {
	// Selector 2 forces the boundary length max; payload comes from the
	// tape then zero-fills.
	tape := New([]byte{2, 0xaa, 0xbb})
	got := tape.Bytes(4)
	if want := []byte{0xaa, 0xbb, 0, 0}; !bytes.Equal(got, want) {
		t.Errorf("Bytes(4) = %x, want %x", got, want)
	}
}

func TestTapeFrontToBack(t *testing.T) {
	tape := New([]byte{7, 8, 9})
	if got := tape.Byte(); got != 7 {
		t.Errorf("first Byte() = %d, want 7", got)
	}
	if got := tape.Byte(); got != 8 {
		t.Errorf("second Byte() = %d, want 8", got)
	}
	if tape.Done() {
		t.Error("Done() with one byte left")
	}
	tape.Byte()
	if !tape.Done() {
		t.Error("not Done() after consuming all input")
	}
}

func TestGen(t *testing.T) {
	if got := Const(42)(New(nil)); got != 42 {
		t.Errorf("Const(42) = %d", got)
	}
	for sel := 0; sel < 256; sel++ {
		g := IntRange(10, 20)(New([]byte{byte(sel), 5, 6, 7, 8}))
		if g < 10 || g > 20 {
			t.Fatalf("IntRange(10, 20) = %d, out of range", g)
		}
	}
	one := OneOf(Const("x"), Const("y"))
	if got := one(New(nil)); got != "x" {
		t.Errorf("OneOf on zero tape = %q, want first generator", got)
	}
	double := Map(Const(21), func(n int) int { return 2 * n })
	if got := double(New(nil)); got != 42 {
		t.Errorf("Map = %d, want 42", got)
	}
	s := SliceOf(Const(1), 5)(New([]byte{2}))
	if len(s) != 5 {
		t.Errorf("SliceOf with max-boundary selector: len = %d, want 5", len(s))
	}
}

// counter is the canary system under test: a counter that the planted
// invariant forbids from reaching 7.
type counter struct{ n int }

var canary = Machine[*counter]{
	Init: func(t *testing.T) *counter { return new(counter) },
	Ops: []Op[*counter]{
		{Name: "inc", Weight: 3, Apply: func(c *counter, t *Tape) error { c.n++; return nil }},
		{Name: "dec", When: func(c *counter) bool { return c.n > 0 },
			Apply: func(c *counter, t *Tape) error { c.n--; return nil }},
	},
	Check: func(t *testing.T, c *counter) {
		if c.n == 7 {
			t.Fatalf("planted violation: counter reached 7")
		}
	},
}

// TestMachineRunPasses exercises Run on a machine whose invariant holds.
var clean = Machine[*counter]{
	Init: canary.Init,
	Ops:  canary.Ops,
	Check: func(t *testing.T, c *counter) {
		if c.n < 0 {
			t.Fatalf("counter negative: %d", c.n)
		}
	},
}

func TestMachineRunPasses(t *testing.T) {
	clean.Run(t, 50)
}

func TestMachineFuzzOpSelection(t *testing.T) {
	// A zero tape of any length decodes to a prefix of "inc" ops (the
	// first enabled op) and never trips the clean invariant.
	clean.runTape(t, make([]byte, 64))
}

// TestMachineCanary proves the harness has teeth: a planted invariant
// violation must be found by Run, and the shrunk op sequence must be
// reported. The failing Run executes in a child process so the failure
// does not fail this test.
func TestMachineCanary(t *testing.T) {
	if os.Getenv("FUZZTAPE_CANARY") == "1" {
		canary.Run(t, 2000)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestMachineCanary$", "-test.v")
	cmd.Env = append(os.Environ(), "FUZZTAPE_CANARY=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("planted violation not found in 2000 cases; output:\n%s", out)
	}
	for _, want := range []string{"planted violation", "op sequence", "shrunk failing input"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("child output missing %q; output:\n%s", want, out)
		}
	}
}

// FuzzMachineCanaryClean is the fuzz-mode integration check: the clean
// machine holds under arbitrary inputs. Run with -fuzz to explore.
func FuzzMachineCanaryClean(f *testing.F) {
	f.Add([]byte{})
	f.Add(bytes.Repeat([]byte{0xff}, 128))
	clean.Fuzz(f)
}
