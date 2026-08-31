package fuzztape

import (
	"math/rand"
	"os"
	"strconv"
	"strings"
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
		if got.Failure == nil || *got.Failure != *want {
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
		if got.Failure == nil || *got.Failure != *want {
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
		if got.Failure == nil || *got.Failure != *want {
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
