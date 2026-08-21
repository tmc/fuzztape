package linear_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/linear"
	"github.com/tmc/fuzztape/sched"
)

// The system under test is a one-cell register supporting write and
// read. The reference state is its value, which is comparable, so the
// search can memoize on it directly.

type call struct {
	Write bool
	Value int
}

// step is the sequential specification: a write always succeeds and
// sets the cell; a read is legal only if it returned the current value.
func step(s int, in call, out int) (int, bool) {
	if in.Write {
		return in.Value, true
	}
	return s, out == s
}

// register is the system under test. When racy is set, a write
// publishes the *previous* write's value and holds its own for next
// time, so a write can return before its value is visible.
//
// That is the shape of bug linearizability exists to catch, and note
// what it is not. A write that merely loses a race with a concurrent
// write is legal: either order explains the result. This one is
// illegal in a way no order can repair — a read called strictly after
// a write returned observes a value that write replaced, and real time
// forbids placing the read first.
type register struct {
	sched   *sched.Scheduler
	history *linear.History[call, int]
	v       int
	held    int
	racy    bool
}

func (r *register) Write(v int) {
	op := r.history.Start("write", call{Write: true, Value: v})
	r.sched.Yield()
	if r.racy {
		r.v, r.held = r.held, v
	} else {
		r.v = v
	}
	op.Done(r.history, 0)
}

func (r *register) Read() {
	op := r.history.Start("read", call{})
	r.sched.Yield()
	op.Done(r.history, r.v)
}

func machine(racy bool) fuzztape.Machine[*register] {
	return fuzztape.Machine[*register]{
		Bubble: true,
		MaxOps: 24, // the search is exponential in overlapping ops
		Init: func(t *fuzztape.T) *register {
			r := &register{history: new(linear.History[call, int]), racy: racy}
			r.sched = sched.New(t)
			// Registered before the scheduler's own cleanup, so it runs
			// after it: the history is checked once every goroutine has
			// finished, not while some are still in flight.
			t.Cleanup(func() { linear.Assert(t, r.history, 0, step) })
			return r
		},
		Ops: []fuzztape.Op[*register]{
			{Name: "write", Weight: 2, Apply: func(t *fuzztape.T, r *register) {
				v := fuzztape.IntRange(1, 9)(t.Tape)
				r.sched.Go("write", func() { r.Write(v) })
			}},
			{Name: "read", Apply: func(t *fuzztape.T, r *register) {
				r.sched.Go("read", func() { r.Read() })
			}},
			sched.StepOp("step", func(r *register) *sched.Scheduler { return r.sched }),
		},
	}
}

// TestCorrectRegisterIsLinearizable is the negative control: an honest
// register must admit a sequential order for every schedule, or the
// canary proves only that the checker rejects valid histories.
func TestCorrectRegisterIsLinearizable(t *testing.T) {
	machine(false).Run(t, 200)
}

// TestCanary proves the checker catches a violation that no per-op
// comparison could: every individual read returns a value some write
// supplied, and only the order rules it out.
func TestCanary(t *testing.T) {
	if os.Getenv("FUZZTAPE_CANARY") == "1" {
		machine(true).Run(t, 3000)
		return
	}
	out, err := child(t, "^TestCanary$")
	if err == nil {
		t.Fatalf("planted violation not found; output:\n%s", out)
	}
	for _, want := range []string{"no sequential order explains", "op sequence", "shrunk failing input"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; output:\n%s", want, out)
		}
	}
}

// TestCheckAcceptsOverlap covers the point of the whole algorithm: two
// operations whose intervals overlap may be linearized in either order,
// so a history a strict per-op comparison would reject is accepted.
func TestCheckAcceptsOverlap(t *testing.T) {
	var h linear.History[call, int]
	// w1 is called, then w2 is called and returns, then w1 returns.
	// The only valid order is w2 after w1 — the checker must find it
	// rather than insisting on call order.
	w1 := h.Start("write1", call{Write: true, Value: 1})
	w2 := h.Start("write2", call{Write: true, Value: 2})
	w2.Done(&h, 0)
	w1.Done(&h, 0)
	r := h.Start("read", call{})
	r.Done(&h, 2)

	order, ok := linear.Check(&h, 0, step)
	if !ok {
		t.Fatalf("rejected a linearizable history:\n%s", &h)
	}
	if got := strings.Join(order, " "); got != "write1 write2 read" {
		t.Errorf("order = %q, want %q", got, "write1 write2 read")
	}
}

// TestCheckRejectsStaleRead covers the other direction: a read that
// returned a value already overwritten before the read was called
// cannot be placed anywhere.
func TestCheckRejectsStaleRead(t *testing.T) {
	var h linear.History[call, int]
	w := h.Start("write", call{Write: true, Value: 7})
	w.Done(&h, 0)
	r := h.Start("read", call{})
	r.Done(&h, 0) // 0 was overwritten by 7 before this read was called

	if _, ok := linear.Check(&h, 0, step); ok {
		t.Errorf("accepted a stale read:\n%s", &h)
	}
}

// TestCheckPendingOperation covers an operation still in flight: it may
// be linearized anywhere after its call, including not having taken
// effect yet, so a history containing one is judged on what did return.
func TestCheckPendingOperation(t *testing.T) {
	var h linear.History[call, int]
	h.Start("write", call{Write: true, Value: 5}) // never completes
	r := h.Start("read", call{})
	r.Done(&h, 0) // legal: the pending write may not have happened yet

	if _, ok := linear.Check(&h, 0, step); !ok {
		t.Errorf("rejected a history whose only conflict is a pending op:\n%s", &h)
	}
	if !strings.Contains(h.String(), "pending") {
		t.Errorf("String() does not mark the pending op:\n%s", &h)
	}
}

// zeroOnFailure is step written the way the [linear.Step] signature
// invites: it returns the zero state alongside false, because a state
// reported illegal has no successor to name. A search that used that
// state anyway would continue from a register holding 0 that nothing
// ever wrote.
func zeroOnFailure(s int, in call, out int) (int, bool) {
	if in.Write {
		return in.Value, true
	}
	if out != s {
		return 0, false
	}
	return s, true
}

// TestCheckPendingOpKeepsStateHonest covers a pending operation whose
// zero result is illegal where the search tries to place it. The two
// things it may have done are "took effect" and "did not", and neither
// is "moved the reference to whatever the Step returned when it said
// no". Here the history is genuinely unlinearizable — a read called
// after write(5) returned 0 — and it must be rejected under either
// spelling of the same specification.
func TestCheckPendingOpKeepsStateHonest(t *testing.T) {
	newHistory := func() *linear.History[call, int] {
		h := new(linear.History[call, int])
		w := h.Start("write", call{Write: true, Value: 5})
		w.Done(h, 0)
		h.Start("pending-read", call{}) // never completes
		r := h.Start("read", call{})
		r.Done(h, 0) // 0 was overwritten by 5 before this read was called
		return h
	}
	for _, tc := range []struct {
		name string
		step linear.Step[int, call, int]
	}{
		{"state on failure", step},
		{"zero on failure", zeroOnFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHistory()
			if _, ok := linear.Check(h, 0, tc.step); ok {
				t.Errorf("accepted a stale read by way of a pending op:\n%s", h)
			}
		})
	}
}

// TestCheckPanicsOnLongHistory covers the search's own bound. Reporting
// "not linearizable" for a history the search cannot represent would be
// indistinguishable from a real violation, which is the one answer a
// checker must never guess at.
func TestCheckPanicsOnLongHistory(t *testing.T) {
	var h linear.History[call, int]
	for range 65 {
		op := h.Start("write", call{Write: true, Value: 1})
		op.Done(&h, 0)
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Check did not panic on a 65-operation history")
		}
		if s, ok := r.(string); !ok || !strings.Contains(s, "over the 64") {
			t.Errorf("panic = %v, want one naming the limit", r)
		}
	}()
	linear.Check(&h, 0, step)
}

// TestCheckEmpty covers the degenerate case.
func TestCheckEmpty(t *testing.T) {
	var h linear.History[call, int]
	if _, ok := linear.Check(&h, 0, step); !ok {
		t.Error("empty history rejected")
	}
}

func child(t *testing.T, run string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", run, "-test.v")
	cmd.Env = append(os.Environ(), "FUZZTAPE_CANARY=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}
