package fuzztape

import (
	"fmt"
	"sync"
	"testing"
)

// A T carries the tape an op draws from and failure reporting. It is the
// fuzztape analog of [testing.T]: ops, [Machine.Init], and [Machine.Check]
// receive one, draw parameters through the embedded [Tape], and report
// failures through its own forwarding methods.
//
// T embeds *Tape but not *testing.T. Embedding the test would hand
// every op Run, Parallel, and Skip, each of which either panics inside
// a synctest bubble or abandons a sequence mid-flight; [testing.TB] is
// sealed, so a narrow struct with explicit forwarders is the only way
// to expose reporting without them. Everything T does forward reports
// the caller's line, not fuzztape's.
//
// The zero value of T is not usable; obtain one from a [Machine] or [NewT].
//
// A T is valid only for the duration of the input it was created for.
// Retaining one past the op sequence that received it, or using one
// from another goroutine after that sequence ends, is a bug.
type T struct {
	*Tape

	report reporter
	record func(string)
	reason string

	mu       sync.Mutex
	cleanups []func()
}

// reporter is the part of testing.TB that T needs. It is deliberately
// package-owned: testing.TB is sealed, so it cannot be the boundary for
// Machine.Exec.
type reporter interface {
	Cleanup(func())
	Errorf(string, ...any)
	Fatalf(string, ...any)
	Logf(string, ...any)
	Helper()
	Name() string
	TempDir() string

	// testingTB returns the test a report reaches, or nil outside a
	// test. T uses it to mark its own forwarding frames as helpers, so
	// that a failure is reported at the op that raised it rather than
	// at the frame that passed it along. Marking has to happen in the
	// frame being marked, so it cannot be done through Helper.
	testingTB() testing.TB
}

type testReporter struct{ tb testing.TB }

func (r testReporter) Cleanup(f func()) { r.tb.Cleanup(f) }
func (r testReporter) Errorf(format string, args ...any) {
	r.tb.Helper()
	r.tb.Errorf(format, args...)
}
func (r testReporter) Fatalf(format string, args ...any) {
	r.tb.Helper()
	r.tb.Fatalf(format, args...)
}
func (r testReporter) Logf(format string, args ...any) {
	r.tb.Helper()
	r.tb.Logf(format, args...)
}
func (r testReporter) Helper()               { r.tb.Helper() }
func (r testReporter) Name() string          { return r.tb.Name() }
func (r testReporter) TempDir() string       { return r.tb.TempDir() }
func (r testReporter) testingTB() testing.TB { return r.tb }

func newTestT(tb testing.TB, data []byte) *T {
	return &T{Tape: New(data), report: testReporter{tb: tb}}
}

// NewT returns a T reading data and reporting to tb.
//
// A [Machine] builds its own T for every input, so this is for
// exercising an op directly in an ordinary test, without a machine
// around it. A [T.Reject] on a T built here panics out to the caller,
// because there is no op loop to unwind into, and its [T.Cleanup]
// functions run when tb finishes rather than at the end of a sequence,
// because there is no sequence.
func NewT(tb testing.TB, data []byte) *T {
	t := newTestT(tb, data)
	t.report.Cleanup(t.runCleanups)
	return t
}

// rejected is the panic value [T.Reject] unwinds an op with. It never
// escapes runOps.
type rejected struct{}

// Reject abandons the current op: the op did not apply, the sequence
// continues with the next one, and the reason appears in the op log of
// a failing input. It does not return.
//
// Reject is for an op that turns out not to be applicable once it has
// drawn its parameters — a queue that is full, an ID that is already
// taken. It is not a failure report: use [T.Fatalf] or [T.Errorf] for
// that. Prefer [Op.When] where applicability can be decided before
// drawing anything, because an op rejected after drawing has still
// consumed tape.
//
// Like [T.Fatalf], call Reject only from the goroutine running the op
// sequence. Calling it from another goroutine races on internal state
// and crashes the process with an unrecovered panic.
func (t *T) Reject(format string, args ...any) {
	t.reason = fmt.Sprintf(format, args...)
	panic(rejected{})
}

// Errorf reports a failure and continues the sequence.
func (t *T) Errorf(format string, args ...any) {
	if tb := t.report.testingTB(); tb != nil {
		tb.Helper()
	}
	t.report.Errorf(format, args...)
}

// Fatalf reports a failure and ends the sequence. It does not return.
//
// Call it only from the goroutine running the op sequence. Under a test it
// ends the sequence with a runtime.Goexit, which unwinds the calling goroutine
// and nothing else, so from a goroutine the system under test started — one
// scheduled by the sched subpackage, say — it kills that goroutine and lets
// the sequence carry on around the hole. Under [Machine.Exec] there is no
// testing.TB to Goexit, so the sequence ends with an internal panic that Exec
// recovers into a [Result] — and a panic from another goroutine kills the
// process rather than one goroutine, so the hole becomes a crash. Report from
// another goroutine with [T.Errorf], which returns under both.
func (t *T) Fatalf(format string, args ...any) {
	if tb := t.report.testingTB(); tb != nil {
		tb.Helper()
	}
	t.report.Fatalf(format, args...)
}

// Logf writes to the test log, which go test prints only for a failing
// input.
func (t *T) Logf(format string, args ...any) {
	if tb := t.report.testingTB(); tb != nil {
		tb.Helper()
	}
	t.report.Logf(format, args...)
}

// Helper marks the calling function as a test helper.
func (t *T) Helper() {
	if tb := t.report.testingTB(); tb != nil {
		tb.Helper()
	}
}

// Cleanup registers f to run when this input's op sequence ends, after
// the last op and in last-registered-first order. It is where a
// sequence asserts what should be true once the system has settled:
// that every resource it took was returned, that it allocated no more
// than its input justified.
//
// The scope is the input, not the test. That matters twice over. A
// failure reported from a cleanup is still attributed to the op
// sequence that caused it, so the sequence is logged and shrunk like
// any other failure — a cleanup deferred to the end of the test would
// report after the machine had stopped listening. And under
// [Machine.Bubble] the cleanup runs inside the bubble, before its exit
// check, which is where a goroutine started by [Machine.Init] must be
// stopped: one stopped on the outer test's cleanup is still blocked at
// exit and fails every case.
func (t *T) Cleanup(f func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cleanups = append(t.cleanups, f)
}

// runCleanups runs the registered cleanups, last first. Each remaining
// cleanup is deferred before the current one runs, so one that panics
// or fails the test does not strand the rest.
func (t *T) runCleanups() {
	t.mu.Lock()
	if len(t.cleanups) == 0 {
		t.mu.Unlock()
		return
	}
	f := t.cleanups[len(t.cleanups)-1]
	t.cleanups = t.cleanups[:len(t.cleanups)-1]
	t.mu.Unlock()
	defer t.runCleanups()
	f()
}

// Name returns the name of the running test.
func (t *T) Name() string { return t.report.Name() }

// TempDir returns a temporary directory for the running test, removed
// when it finishes.
func (t *T) TempDir() string { return t.report.TempDir() }

// apply runs op against s, reporting whether it applied and, if not,
// the reason it gave to [T.Reject]. A panic that is not a rejection
// keeps propagating, out to runOps, which turns it into a failure with
// the stack it was raised on; a Goexit from Fatalf is not a panic at
// all. Neither is swallowed here.
func apply[S any](t *T, op *Op[S], s S) (reason string, ok bool) {
	t.reason = ""
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if _, isReject := r.(rejected); !isReject {
			panic(r)
		}
		reason, ok = t.reason, false
	}()
	op.Apply(t, s)
	return "", true
}
