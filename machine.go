package fuzztape

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var seedFlag = flag.Int64("fuzztape.seed", 0, "seed for fuzztape Machine.Run case generation (0 derives one)")

// An Op is one operation of a stateful property test.
type Op[S any] struct {
	// Name labels the op in failure logs.
	Name string
	// Weight is the op's relative selection frequency; 0 means 1.
	Weight int
	// When reports whether the op is currently applicable;
	// nil means always. Ops with a false When are not selected.
	When func(s S) bool
	// Apply performs the op, drawing parameters from the tape.
	// A non-nil error rejects the op — the sequence continues — and
	// must not be used for failures; fail with t.Fatalf in Check or
	// via the *testing.T captured by Init.
	Apply func(s S, t *Tape) error
}

// A Machine describes a stateful property test: a system under test, a
// set of operations, and an invariant. Each input decodes to a bounded
// operation sequence; the invariant is checked after every applied op.
type Machine[S any] struct {
	// Init returns a fresh system under test.
	Init func(t *testing.T) S
	// Ops is the operation set. It must be non-empty, and its order
	// is part of the corpus encoding: reordering ops changes how
	// previously saved inputs decode.
	Ops []Op[S]
	// Check asserts the invariant, failing with t.Fatalf.
	Check func(t *testing.T, s S)
	// MaxOps bounds the ops decoded per input; 0 means 64.
	MaxOps int
	// Name, if set, is the fuzz target name (e.g. "FuzzStreamMachine")
	// under which Run saves shrunk failing inputs to testdata/fuzz/,
	// so a failure found by Run becomes a seed input replayed by both
	// Run and the fuzz target.
	Name string
}

// Fuzz registers the machine as the fuzz function of f. Corpus files,
// -fuzztime budgets, minimization, and seeds via f.Add behave as for
// any other fuzz target.
func (m Machine[S]) Fuzz(f *testing.F) {
	f.Fuzz(func(t *testing.T, data []byte) {
		m.runTape(t, data)
	})
}

// Run checks the machine against iters pseudo-random inputs inside an
// ordinary test; iters <= 0 means 100. The seed is printed and can be
// pinned with -fuzztape.seed. On failure Run shrinks the failing input,
// logs the minimal op sequence, and (if Name is set) saves the input to
// testdata/fuzz/ for permanent replay.
func (m Machine[S]) Run(t *testing.T, iters int) {
	if iters <= 0 {
		iters = 100
	}
	seed := *seedFlag
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	t.Logf("fuzztape: seed %d (rerun with -fuzztape.seed=%d)", seed, seed)
	rng := rand.New(rand.NewSource(seed))
	for i := range iters {
		data := make([]byte, 16+rng.Intn(496))
		rng.Read(data)
		if t.Run(fmt.Sprintf("case%04d", i), func(t *testing.T) { m.runTape(t, data) }) {
			continue
		}
		data = m.shrink(t, data)
		t.Logf("fuzztape: shrunk failing input to %d bytes: %x", len(data), data)
		if m.Name != "" {
			if path, err := writeCorpusFile(m.Name, data); err != nil {
				t.Logf("fuzztape: save corpus file: %v", err)
			} else {
				t.Logf("fuzztape: saved failing input to %s", path)
			}
		}
		return
	}
}

// runTape decodes data into one operation sequence and applies it,
// checking the invariant after every applied op. The decoded sequence
// is logged if the test fails.
func (m Machine[S]) runTape(t *testing.T, data []byte) {
	if len(m.Ops) == 0 {
		t.Fatal("fuzztape: Machine has no Ops")
	}
	tape := New(data)
	s := m.Init(t)
	maxOps := m.MaxOps
	if maxOps <= 0 {
		maxOps = 64
	}
	var applied []string
	defer func() {
		if t.Failed() {
			t.Logf("fuzztape: op sequence (%d ops):\n\t%s", len(applied), strings.Join(applied, "\n\t"))
		}
	}()
	for range maxOps {
		if tape.Done() {
			return
		}
		enabled := make([]*Op[S], 0, len(m.Ops))
		total := 0
		for i := range m.Ops {
			op := &m.Ops[i]
			if op.When == nil || op.When(s) {
				enabled = append(enabled, op)
				total += max(op.Weight, 1)
			}
		}
		if len(enabled) == 0 {
			return
		}
		w := tape.IntN(total)
		var op *Op[S]
		for _, o := range enabled {
			w -= max(o.Weight, 1)
			if w < 0 {
				op = o
				break
			}
		}
		if err := op.Apply(s, tape); err != nil {
			applied = append(applied, op.Name+" (rejected: "+err.Error()+")")
			continue
		}
		applied = append(applied, op.Name)
		if m.Check != nil {
			m.Check(t, s)
		}
	}
}

// shrink reduces a failing input while it keeps failing: first by
// truncation, then by zeroing individual bytes. Because reads past the
// end of the input and zero bytes both decode to the simplest choice,
// byte-level edits shrink the decoded op sequence and its values.
// Attempts run as subtests of t (which is already failing).
func (m Machine[S]) shrink(t *testing.T, data []byte) []byte {
	attempts := 0
	fails := func(d []byte) bool {
		attempts++
		return !t.Run(fmt.Sprintf("shrink%03d", attempts), func(t *testing.T) { m.runTape(t, d) })
	}
	for len(data) > 0 && attempts < 200 {
		cut := data[:len(data)/2]
		if !fails(cut) {
			break
		}
		data = cut
	}
	for len(data) > 0 && attempts < 200 && fails(data[:len(data)-1]) {
		data = data[:len(data)-1]
	}
	for i := 0; i < len(data) && attempts < 250; i++ {
		if data[i] == 0 {
			continue
		}
		zeroed := append([]byte(nil), data...)
		zeroed[i] = 0
		if fails(zeroed) {
			data = zeroed
		}
	}
	return data
}

// writeCorpusFile saves data as a seed corpus file for the named fuzz
// target, in the format go test replays from testdata/fuzz.
func writeCorpusFile(target string, data []byte) (string, error) {
	dir := filepath.Join("testdata", "fuzz", target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	content := fmt.Sprintf("go test fuzz v1\n[]byte(%q)\n", data)
	path := filepath.Join(dir, fmt.Sprintf("fuzztape-%x", sha256.Sum256(data))[:24])
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
