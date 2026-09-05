package fuzztape

import (
	"bytes"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestMachineExecRecordsRenderedArgs(t *testing.T) {
	type state struct{ values []int }
	m := Machine[*state]{
		Init: func(*T) *state { return new(state) },
		Ops: []Op[*state]{
			NewRecordedOp("set", func(t *Tape) int { return int(t.Byte()) }, func(v int) string {
				return "value=" + strconv.Itoa(v)
			}, func(_ *T, s *state, v int) {
				s.values = append(s.values, v)
			}),
		},
		MaxOps: 2,
	}
	got := m.Exec([]byte{0, 7, 0, 9})
	if got.Failure != nil {
		t.Fatalf("Exec failed: %+v", got.Failure)
	}
	if len(got.Records) != 2 {
		t.Fatalf("Exec records = %d, want 2", len(got.Records))
	}
	for i, want := range []Record{
		{Name: "set", Args: "value=7", Outcome: Applied},
		{Name: "set", Args: "value=9", Outcome: Applied},
	} {
		if got.Records[i] != want {
			t.Errorf("record %d = %+v, want %+v", i, got.Records[i], want)
		}
	}
}

// execWithoutTesting calls Exec without constructing or accepting a testing.T.
func execWithoutTesting() Result {
	m := Machine[int]{
		Init: func(*T) int { return 0 },
		Ops: []Op[int]{
			{Name: "step", Apply: func(_ *T, s int) { s++ }},
		},
		MaxOps: 1,
	}
	return m.Exec([]byte{0})
}

func TestMachineExecWithoutTestingT(t *testing.T) {
	got := execWithoutTesting()
	if got.Failure != nil {
		t.Fatalf("Exec failed: %+v", got.Failure)
	}
	if len(got.Records) != 1 || got.Records[0].Name != "step" {
		t.Fatalf("Exec records = %+v, want one step", got.Records)
	}
}

func TestMachineExecAttribution(t *testing.T) {
	t.Run("apply", func(t *testing.T) {
		m := Machine[*int]{
			Init: func(*T) *int { return new(int) },
			Ops: []Op[*int]{
				{Name: "fail", Apply: func(t *T, s *int) {
					*s++
					if *s == 1 {
						t.Errorf("apply failure")
					}
				}},
			},
			MaxOps: 2,
		}
		got := m.Exec([]byte{0, 0})
		want := &Failure{Phase: Apply, Op: 0, Message: "apply failure"}
		if !sameFailure(got.Failure, want) {
			t.Errorf("Exec failure = %+v, want %+v", got.Failure, want)
		}
		if len(got.Records) != 2 || got.Records[0].Outcome != Failed || got.Records[1].Outcome != Applied {
			t.Errorf("Exec records = %+v, want failed then applied", got.Records)
		}
	})

	t.Run("check", func(t *testing.T) {
		m := Machine[*int]{
			Init: func(*T) *int { return new(int) },
			Ops: []Op[*int]{
				{Name: "step", Apply: func(_ *T, s *int) { *s++ }},
			},
			Check: func(t *T, s *int) {
				if *s == 2 {
					t.Fatalf("check failure")
				}
			},
			MaxOps: 2,
		}
		got := m.Exec([]byte{0, 0})
		want := &Failure{Phase: Check, Op: 1, Message: "check failure"}
		if !sameFailure(got.Failure, want) {
			t.Errorf("Exec failure = %+v, want %+v", got.Failure, want)
		}
		if len(got.Records) != 2 || got.Records[1].Outcome != Applied {
			t.Errorf("Exec records = %+v, want two applied records", got.Records)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		m := Machine[int]{
			Init: func(*T) int { return 0 },
			Ops: []Op[int]{
				{Name: "skip", Apply: func(t *T, _ int) { t.Reject("not now") }},
			},
			MaxOps: 1,
		}
		got := m.Exec([]byte{0})
		if got.Failure != nil {
			t.Errorf("Exec failure = %+v, want nil", got.Failure)
		}
		if len(got.Records) != 1 || got.Records[0].Outcome != Rejected {
			t.Errorf("Exec records = %+v, want one rejected record", got.Records)
		}
	})

	t.Run("init", func(t *testing.T) {
		m := Machine[int]{
			Init: func(t *T) int {
				t.Fatalf("init failure")
				return 0
			},
			Ops: []Op[int]{{Name: "step", Apply: func(*T, int) {}}},
		}
		got := m.Exec(nil)
		want := &Failure{Phase: Init, Op: -1, Message: "init failure"}
		if !sameFailure(got.Failure, want) {
			t.Errorf("Exec failure = %+v, want %+v", got.Failure, want)
		}
		if len(got.Records) != 0 {
			t.Errorf("Exec records = %+v, want none", got.Records)
		}
	})
}

// TestExecMatchesTrace is the guard on the reason exec is shared: Exec and
// the test-driven path are one decoder, so an input must mean the same
// sequence however it is run. A second selection loop that drifted from this
// one would make every corpus seed, shrink result, and trace stop being
// portable between them, and nothing else here would notice.
func TestExecMatchesTrace(t *testing.T) {
	type state struct{ n int }
	m := Machine[*state]{
		Init: func(*T) *state { return new(state) },
		Ops: []Op[*state]{
			{Name: "inc", Weight: 3, Apply: func(_ *T, s *state) { s.n++ }},
			{Name: "dec", Weight: 1, When: func(s *state) bool { return s.n > 0 },
				Apply: func(_ *T, s *state) { s.n-- }},
			{Name: "skip", Apply: func(t *T, _ *state) { t.Reject("never") }},
			NewRecordedOp("set", func(t *Tape) int { return t.IntN(10) },
				strconv.Itoa, func(_ *T, s *state, v int) { s.n = v }),
		},
		MaxOps: 12,
	}
	rng := rand.New(rand.NewSource(1))
	for i := range 200 {
		data := make([]byte, 1+rng.Intn(48))
		rng.Read(data)
		want := m.Trace(t, data)
		got := m.Exec(data)
		if len(got.Records) != len(want) {
			t.Fatalf("input %d (%x): Exec %d records, Trace %d ops\n%+v\n%v", i, data, len(got.Records), len(want), got.Records, want)
		}
		for j, r := range got.Records {
			if !strings.HasPrefix(want[j], r.Name) {
				t.Fatalf("input %d (%x): record %d = %q, Trace = %q", i, data, j, r.Name, want[j])
			}
			if (r.Outcome == Rejected) != strings.Contains(want[j], "(rejected:") {
				t.Fatalf("input %d (%x): record %d outcome %v, Trace = %q", i, data, j, r.Outcome, want[j])
			}
		}
	}
}

// TestExecTempDirRemoved proves Exec cleans up after T.TempDir, which outside
// a test has no testing.TB to remove it.
func TestExecTempDirRemoved(t *testing.T) {
	var dir string
	m := Machine[int]{
		Init: func(t *T) int {
			dir = t.TempDir()
			return 0
		},
		Ops:    []Op[int]{{Name: "step", Apply: func(*T, int) {}}},
		MaxOps: 1,
	}
	if got := m.Exec([]byte{0}); got.Failure != nil {
		t.Fatalf("Exec failed: %+v", got.Failure)
	}
	if dir == "" {
		t.Fatal("Exec gave no temp dir")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s still exists after Exec (err %v)", dir, err)
	}
}

// TestExecPanicIsAFailure proves a panicking op is reported as a failure
// rather than escaping Exec, as it is under a test.
func TestExecPanicIsAFailure(t *testing.T) {
	m := Machine[int]{
		Init:   func(*T) int { return 0 },
		Ops:    []Op[int]{{Name: "boom", Apply: func(*T, int) { panic("kaboom") }}},
		MaxOps: 1,
	}
	got := m.Exec([]byte{0})
	if got.Failure == nil || got.Failure.Phase != Apply || got.Failure.Op != 0 {
		t.Fatalf("Exec failure = %+v, want an apply failure at op 0", got.Failure)
	}
	if !strings.Contains(got.Failure.Message, "kaboom") {
		t.Errorf("Exec failure message = %q, want it to name the panic", got.Failure.Message)
	}
	if len(got.Records) != 1 || got.Records[0].Outcome != Failed || got.Records[0].Reason != "panicked: kaboom" {
		t.Errorf("Exec records = %+v, want one panicked record", got.Records)
	}
}

// TestConcurrentErrorf reports from two goroutines joined inside one Apply.
// T.Errorf is the documented reporting route for a goroutine an op started,
// so two of them racing in execution.fail is a real use, not a misuse. Run
// this under -race; without the lock it reports a write/write race on
// execution.failed.
func TestConcurrentErrorf(t *testing.T) {
	m := Machine[int]{
		Init: func(*T) int { return 0 },
		Ops: []Op[int]{{
			Name: "report",
			Apply: func(ft *T, _ int) {
				var wg sync.WaitGroup
				for i := range 2 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						ft.Errorf("worker %d", i)
					}()
				}
				wg.Wait()
			},
		}},
		MaxOps: 1,
	}
	got := m.Exec([]byte{0})
	if got.Failure == nil {
		t.Fatal("Exec Failure = nil, want the first worker's report")
	}
	if got.Failure.Phase != Apply {
		t.Errorf("Failure.Phase = %v, want %v", got.Failure.Phase, Apply)
	}
	if got.Records[0].Outcome != Failed {
		t.Errorf("record Outcome = %v, want %v", got.Records[0].Outcome, Failed)
	}
}

// TestPrimaryFailureBeforeCleanup checks that a panic keeps the primary
// failure even though the cleanups report first. Cleanups run while the
// panic is still unwinding, so without the fix a cleanup's Errorf claims
// Result.Failure and the panic that caused it is lost.
func TestPrimaryFailureBeforeCleanup(t *testing.T) {
	machine := func(cleanup func(*T)) Machine[int] {
		return Machine[int]{
			Init: func(ft *T) int {
				if cleanup != nil {
					ft.Cleanup(func() { cleanup(ft) })
				}
				return 0
			},
			Ops:    []Op[int]{{Name: "boom", Apply: func(*T, int) { panic("op exploded") }}},
			MaxOps: 1,
		}
	}
	for _, tt := range []struct {
		name    string
		cleanup func(*T)
		want    Phase
	}{
		{"panic only", nil, Apply},
		{"panic then reporting cleanup", func(ft *T) { ft.Errorf("cleanup noticed") }, Apply},
		{"panic inside cleanup", func(ft *T) { panic("cleanup exploded") }, Cleanup},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := machine(tt.cleanup).Exec([]byte{0})
			if got.Failure == nil {
				t.Fatal("Exec Failure = nil, want a failure")
			}
			if got.Failure.Phase != tt.want {
				t.Errorf("Failure.Phase = %v, want %v (message %q)",
					got.Failure.Phase, tt.want, got.Failure.Message)
			}
		})
	}
}

// sameFailure compares the reported parts of a failure. The identity
// fields shrinking uses are unexported and hold absolute paths, so a
// test cannot spell them; TestFailureIdentity covers those directly.
func sameFailure(got, want *Failure) bool {
	return got != nil && got.Phase == want.Phase && got.Op == want.Op && got.Message == want.Message
}

// TestFailureIdentity pins what shrinking treats as "the same failure".
// Values differ, format and site do not, so the two are one failure;
// a different format string, or the same format from another line, is
// a different one.
func TestFailureIdentity(t *testing.T) {
	report := func(m Machine[int]) *Failure { return m.Exec([]byte{0, 0, 0}).Failure }
	machine := func(apply func(t *T, s int)) Machine[int] {
		return Machine[int]{
			MaxOps: 1,
			Init:   func(t *T) int { return 0 },
			Ops:    []Op[int]{{Name: "op", Apply: apply}},
		}
	}
	// The same line and format, different values.
	countTo := func(n int) func(*T, int) {
		return func(t *T, s int) { t.Errorf("count is %d", n) }
	}
	a, b := report(machine(countTo(1))), report(machine(countTo(2)))
	if a.Message == b.Message {
		t.Fatal("the two failures should differ in message")
	}
	if !a.sameAs(b) {
		t.Errorf("same format and site should be one failure: %+v vs %+v", a, b)
	}
	// A different format from a different line.
	other := report(machine(func(t *T, s int) { t.Errorf("something else entirely") }))
	if a.sameAs(other) {
		t.Errorf("different format should be a different failure: %+v vs %+v", a, other)
	}
	if !a.sameAs(a) {
		t.Error("a failure should be itself")
	}
	var nilf *Failure
	if nilf.sameAs(a) || a.sameAs(nilf) {
		t.Error("no failure is not the same as a failure")
	}

	// Panics are reported through one format from one line in exec, so
	// the site must be where each was raised: two panics from different
	// lines are different failures, one line with different values is one.
	var m map[int]int
	var s []int
	nilMap := report(machine(func(t *T, n int) { m[n] = n }))
	index := report(machine(func(t *T, n int) { _ = s[n+3] }))
	panicWith := func(v int) func(*T, int) {
		return func(t *T, n int) { panic(v) }
	}
	p1, p2 := report(machine(panicWith(1))), report(machine(panicWith(2)))
	for _, f := range []*Failure{nilMap, index, p1, p2} {
		if f == nil || !strings.Contains(f.Message, "panicked") {
			t.Fatalf("every op should panic, got %+v", f)
		}
	}
	if nilMap.sameAs(index) {
		t.Errorf("panics from different lines should be different failures: %s vs %s", nilMap.site, index.site)
	}
	if !p1.sameAs(p2) {
		t.Errorf("panics from one line should be one failure: %s vs %s", p1.site, p2.site)
	}
	if !strings.Contains(index.site, "exec_test.go:") {
		t.Errorf("panic site should be in the op that panicked, got %s", index.site)
	}
}

// TestShrinkPreservesIdentity is the point of identity-preserving
// shrinking. The machine has two independent bugs at opposite ends of
// one value: a big value is one bug, a zero value is another. Value
// bisection walks bytes toward zero, so it runs straight from the first
// into the second. A shrinker that accepts any failure takes the trade
// and reports a bug the fuzzer never found, with the original left
// unreported; measured, the old oracle shrinks this input to [0] and
// reports "zero value" instead of "big value 255".
func TestShrinkPreservesIdentity(t *testing.T) {
	m := Machine[*int]{
		MaxOps: 1,
		Init:   func(t *T) *int { return new(int) },
		Ops:    []Op[*int]{{Name: "set", Apply: func(t *T, s *int) { *s = int(t.Tape.Byte()) }}},
		Check: func(t *T, s *int) {
			if *s > 100 {
				t.Errorf("big value %d", *s)
			}
			if *s == 0 {
				t.Errorf("zero value")
			}
		},
	}
	data := bytes.Repeat([]byte{0xff}, 32)
	want := m.Exec(data).Failure
	if want == nil || !strings.HasPrefix(want.Message, "big value") {
		t.Fatalf("the original input must hit the big-value bug, got %+v", want)
	}

	got := m.Exec(m.shrink(t, data, nil)).Failure
	if got == nil {
		t.Fatal("shrink returned an input that does not fail")
	}
	// The assertion deliberately does not use sameAs. sameAs is the
	// function under test, so an oracle built from it weakens exactly
	// when the code does: gutting sameAs to compare only Phase leaves
	// this test passing on a shrink that crossed to "zero value". The
	// message prefix is an independent statement of which bug this is.
	if !strings.HasPrefix(got.Message, "big value") {
		t.Errorf("shrink crossed to a different bug: started at %q, ended at %q", want.Message, got.Message)
	}
	// It must still shrink. Preserving identity is worthless if it is
	// bought by refusing every reduction.
	if got.Message == want.Message {
		t.Errorf("shrink did not reduce the value at all: still %q", got.Message)
	}
}
