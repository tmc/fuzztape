package fuzztape

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
)

// An Outcome reports what happened when an op was selected.
type Outcome uint8

const (
	// Applied reports an op that returned without reporting a failure.
	Applied Outcome = iota
	// Rejected reports an op that called [T.Rejectf].
	Rejected
	// Failed reports an op whose Apply reported a failure or panicked.
	Failed
)

// String returns the name of the outcome ("applied", "rejected", or "failed").
func (o Outcome) String() string {
	switch o {
	case Applied:
		return "applied"
	case Rejected:
		return "rejected"
	case Failed:
		return "failed"
	default:
		return fmt.Sprintf("Outcome(%d)", o)
	}
}

// A Record is one selected operation in a [Machine.Exec] result.
type Record struct {
	// Name is the selected operation's name.
	Name string
	// Args is the value rendered by [NewRecordedOp]. It is empty for an
	// op that did not supply a renderer.
	Args string
	// Outcome reports whether the selected operation applied, rejected,
	// or failed.
	Outcome Outcome
	// Reason is the reason a [Rejected] op gave to [T.Rejectf], or
	// "panicked: v" for an op that panicked. It is empty otherwise; in
	// particular, an op that failed through [T.Errorf] leaves it empty,
	// because that failure is reported in [Result.Failure].
	Reason string
}

// A Phase identifies the part of a machine that reported a failure.
type Phase string

const (
	// Init is the phase that constructs the system under test.
	Init Phase = "init"
	// Apply is the phase that applies a selected op.
	Apply Phase = "apply"
	// Check is the phase that checks the invariant after an op.
	Check Phase = "check"
	// Cleanup is the phase that runs input cleanups.
	Cleanup Phase = "cleanup"
)

// A Failure is the first failure reported while executing a machine.
//
// Op is -1 for [Init]. For [Apply], it is the index of the failed record.
// For [Check], it is the index of the record after which the check ran. For
// [Cleanup], it is the last record's index, or -1 when no op applied. A panic
// is reported in the phase in which it was raised.
type Failure struct {
	Phase   Phase
	Op      int
	Message string

	// format and site identify the failure without its values: the
	// unexpanded format string and the file:line that reported it.
	// Message cannot serve, because the values in it are exactly what
	// shrinking is trying to change. They are unexported because they
	// are a shrinking heuristic, not a contract about failure identity.
	format string
	site   string
}

// sameAs reports whether g is, for shrinking's purposes, the failure f
// is. Two failures match when they were reported from the same place
// with the same format string in the same phase; the op index is not
// part of it, because moving the failure earlier in the sequence is
// what shrinking does.
//
// This is a heuristic. Two genuinely different bugs reported through
// one shared helper — a table-driven checker that reports every
// mismatch with the same format — are indistinguishable to it, and
// shrinking may cross from one to the other. It is strictly better than
// accepting any failure at all, which is what Go's own minimizer does
// (internal/fuzz/worker.go accepts a candidate if it returns an error
// when one was expected, whatever the error).
func (f *Failure) sameAs(g *Failure) bool {
	if f == nil || g == nil {
		return f == nil && g == nil
	}
	return f.Phase == g.Phase && f.format == g.format && f.site == g.site
}

// callsite returns the file:line that reported a failure: the first
// frame below the execution's Errorf or Fatalf that is not one of this
// package's reporting entry points.
//
// A fixed skip count cannot find it, because the path from T.Errorf and
// the path from an internal report are different depths. Skipping every
// frame in this package cannot either: a machine defined in fuzztape's
// own tests would then resolve to somewhere inside testing. So the rule
// names the functions that forward a report and stops at whatever
// called them.
func callsite() string {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if !forwardsReports(f.Function) {
			return fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		if !more {
			return ""
		}
	}
}

// forwardsReports reports whether name is one of this package's
// functions that passes a failure along rather than raising it.
func forwardsReports(name string) bool {
	if !strings.HasPrefix(name, "github.com/tmc/fuzztape.") {
		return false
	}
	return strings.HasSuffix(name, ".Errorf") || strings.HasSuffix(name, ".Fatalf")
}

// panicSite returns the file:line that raised the panic being recovered:
// the first frame past the deepest runtime.gopanic that is not in the
// runtime, which skips the runtime's own raisers such as panicIndex and
// sigpanic. The deepest, because a deferred function that re-panics —
// apply does, for every panic that is not a rejection — raises a second
// panic above the first while the first's frames are still below it. It
// must be called from the deferred function that recovers, while the
// panicking frames are still on the stack.
//
// A panic is reported through one format string from one line here, so
// without its own site every panic would be the same failure to
// shrinking, and a nil dereference could shrink into an index out of
// range.
func panicSite() string {
	var pcs [64]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	site, raised := "", false
	for {
		f, more := frames.Next()
		switch {
		case f.Function == "runtime.gopanic":
			raised = true
		case raised && !strings.HasPrefix(f.Function, "runtime."):
			site, raised = fmt.Sprintf("%s:%d", f.File, f.Line), false
		}
		if !more {
			return site
		}
	}
}

// A Result is the history, op tape offsets, and first failure from
// [Machine.Exec]. Failure is nil when the machine passed.
//
// Splits holds the tape offsets at which the decoded ops begin, plus a
// terminal offset past the last byte consumed when the sequence ended
// without reading past the end of the input (matching [Machine.Splits]).
type Result struct {
	Records []Record
	Splits  []int
	Failure *Failure
}

// Exec runs data against a fresh system and returns its decoded history,
// op tape offsets, and first failure. It does not require a testing.T.
//
// Exec drives the same execution as [Machine.Run] and [Machine.Fuzz] — one
// decoder, so an input means the same sequence however it is run — but it
// creates no synctest bubble. Machines that set [Machine.Bubble] must run
// through Run or Fuzz for the bubble's exit check to happen at all.
func (m Machine[S]) Exec(data []byte) Result {
	var splits []int
	res := m.exec1(data, &splits)
	res.Splits = splits
	return res
}

// exec1 runs one input outside a test, recording tape offsets in splits if it
// is non-nil. It is Exec's body and the failure oracle shrink uses for an
// unbubbled machine.
func (m Machine[S]) exec1(data []byte, splits *[]int) Result {
	var result Result
	r := new(execReporter)
	e := &execution[S]{reporter: r, result: &result, phase: Init, op: -1}
	defer r.removeDirs()
	m.exec(&T{Tape: New(data), report: e}, e, splits)
	return result
}

// fatal ends a sequence after T.Fatalf reports its failure outside a test,
// where there is no testing.TB to end it with a Goexit.
type fatal struct{}

// An execution accumulates the history and first failure of one input. It is
// the reporter T reports through, so that every failure is attributed to a
// phase and a record before being forwarded to the underlying reporter —
// a testing.TB under a test, and a temp-dir keeper under [Machine.Exec].
type execution[S any] struct {
	reporter

	// mu guards the fields a failure touches. T.Errorf is the
	// documented way for a goroutine the op started to report, so fail
	// runs concurrently with the sequence goroutine advancing to the
	// next op.
	mu     sync.Mutex
	result *Result
	// stacks reports whether a panic is reported with the stack it was
	// raised on. Only a test has anywhere to print one.
	stacks bool
	phase  Phase
	op     int
	// failed reports whether the current phase reported a failure. It is
	// reset per phase, unlike Result.Failure, which keeps the first.
	failed bool
	record *Record
	// note is the trailing history line for a panic raised outside an
	// op, which has no record of its own to carry it.
	note string
}

// fail records a failure raised at site against the current phase without
// reporting it. Result.Failure keeps the first one; failed is per phase.
func (e *execution[S]) fail(site, format string, args ...any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.failed = true
	if e.phase == Apply && e.record != nil {
		e.record.Outcome = Failed
	}
	if e.result.Failure == nil {
		e.result.Failure = &Failure{
			Phase:   e.phase,
			Op:      e.op,
			Message: fmt.Sprintf(format, args...),
			format:  format,
			site:    site,
		}
	}
}

func (e *execution[S]) Errorf(format string, args ...any) {
	if tb := e.testingTB(); tb != nil {
		tb.Helper()
	}
	e.fail(callsite(), format, args...)
	e.reporter.Errorf(format, args...)
}

func (e *execution[S]) Fatalf(format string, args ...any) {
	if tb := e.testingTB(); tb != nil {
		tb.Helper()
	}
	e.fail(callsite(), format, args...)
	e.reporter.Fatalf(format, args...)
}

// where names the part of the machine currently running, for a panic message
// and for the history line that reports it.
func (e *execution[S]) where() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.whereLocked()
}

func (e *execution[S]) whereLocked() string {
	switch e.phase {
	case Apply:
		if e.record != nil {
			return e.record.Name
		}
		return "Apply"
	case Check:
		if e.record != nil {
			return "Check after " + e.record.Name
		}
		return "Check"
	case Cleanup:
		return "cleanup"
	default:
		return "Init"
	}
}

// panicked reports a panic raised at site as an ordinary failure of the
// current phase.
func (e *execution[S]) panicked(r any, site string) {
	e.mu.Lock()
	where := e.whereLocked()
	if e.phase == Apply && e.record != nil {
		e.record.Outcome = Failed
		e.record.Reason = fmt.Sprintf("panicked: %v", r)
	} else {
		e.note = fmt.Sprintf("%s (panicked: %v)", where, r)
	}
	// Cleanups run while the panic is still unwinding, so a failure they
	// reported has already claimed Result.Failure even though the panic
	// happened first. The panic is the primary occurrence: drop the
	// cleanup's claim so the first failure is the one that caused the
	// rest. A panic raised inside a cleanup keeps its own attribution,
	// because then this is the cleanup phase too.
	if f := e.result.Failure; f != nil && f.Phase == Cleanup && e.phase != Cleanup {
		e.result.Failure = nil
	}
	e.mu.Unlock()
	format, args := "fuzztape: %s panicked: %v", []any{where, r}
	if e.stacks {
		// The stack is captured here, at the recover, where the
		// panicking frames are still on it.
		format, args = format+"\n\n%s", append(args, debug.Stack())
	}
	e.fail(site, format, args...)
	e.reporter.Errorf(format, args...)
}

// applied renders the history as the op-sequence lines a failing input logs.
func (e *execution[S]) applied() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.result.Records)+1)
	for _, r := range e.result.Records {
		switch {
		case r.Outcome == Rejected:
			out = append(out, r.Name+" (rejected: "+r.Reason+")")
		case r.Reason != "":
			out = append(out, r.Name+" ("+r.Reason+")")
		default:
			out = append(out, r.Name)
		}
	}
	if e.note != "" {
		out = append(out, e.note)
	}
	return out
}

// exec decodes data into one operation sequence and applies it, checking the
// invariant after every applied op and recording each selection in e. If
// splits is non-nil it records each op's starting tape offset, plus a final
// offset past the last byte consumed — that last one only when the sequence
// ended without reading past the end of the input.
//
// It is the whole execution: [Machine.Run], [Machine.Fuzz], [Machine.Trace],
// [Machine.Splits], and [Machine.Exec] all reach it, differing only in the
// reporter e wraps and in what they do with the result.
func (m Machine[S]) exec(t *T, e *execution[S], splits *[]int) {
	// A panic in the system under test — an index out of range, a nil
	// dereference, a closed channel — is the most common thing a fuzz
	// target finds, so it is converted here into an ordinary failure
	// rather than allowed to kill the process. Everything the rest of
	// the package does with a failure then applies to it: the op
	// sequence is logged, the input is shrunk, and the shrunk input is
	// saved as a seed. Under a test Fatalf ends a sequence with a
	// Goexit, which is not a panic, so it is unaffected; outside one it
	// unwinds with fatal, which is swallowed here because Errorf has
	// already recorded the failure. Rejectf's panic is unwound by apply
	// below this.
	//
	// This is registered before every other defer here, so it runs
	// after them: the panic is recovered only once the cleanups have
	// run and, under Bubble, only inside the bubble, where a recover
	// still leaves the exit check to report anything left blocked.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		if _, isFatal := r.(fatal); isFatal {
			return
		}
		if _, isReject := r.(rejected); isReject {
			e.Errorf("fuzztape: Rejectf called outside an op, in %s", e.where())
			return
		}
		e.panicked(r, panicSite())
	}()
	// Cleanups run before this function returns, so a failure they
	// report still lands inside the op-sequence logging in runTape and
	// is shrunk like any other. They are registered first, and so run
	// last, after the final tape offset is recorded.
	defer func() {
		// A panic raised while the cleanups run belongs to them, not to
		// the last op; one that was already unwinding keeps its own
		// attribution, because runCleanups returns normally under it
		// and the phase is restored below.
		e.mu.Lock()
		phase, op := e.phase, e.op
		e.phase, e.op = Cleanup, len(e.result.Records)-1
		e.mu.Unlock()
		t.runCleanups()
		e.mu.Lock()
		e.phase, e.op = phase, op
		e.mu.Unlock()
	}()
	if splits != nil {
		// The terminal offset is recorded only when the sequence ended
		// without running off the end of the input. If the tape ran
		// out inside the last operation, that operation decoded part
		// of itself from zeros past the end, so its "end" is not a
		// boundary: bytes appended there are read by that operation,
		// not by a new one, and a caller grafting at it would silently
		// mutate the operation before the graft rather than extend the
		// sequence.
		defer func() {
			if !t.Tape.overrun() {
				*splits = append(*splits, t.Pos())
			}
		}()
	}
	if err := m.validate(); err != nil {
		e.Errorf("%s", err)
		return
	}
	t.record = func(args string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.record != nil {
			e.record.Args = args
		}
	}

	e.mu.Lock()
	e.phase, e.op = Init, -1
	e.mu.Unlock()
	s := m.Init(t)
	maxOps := m.MaxOps
	if maxOps <= 0 {
		maxOps = 64
	}
	for range maxOps {
		if t.Done() {
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
		if splits != nil {
			*splits = append(*splits, t.Pos())
		}
		w := t.IntN(total)
		var op *Op[S]
		for _, candidate := range enabled {
			w -= max(candidate.Weight, 1)
			if w < 0 {
				op = candidate
				break
			}
		}

		e.mu.Lock()
		e.result.Records = append(e.result.Records, Record{Name: op.Name, Outcome: Applied})
		e.record = &e.result.Records[len(e.result.Records)-1]
		e.phase, e.op, e.failed = Apply, len(e.result.Records)-1, false
		e.mu.Unlock()
		reason, ok := apply(t, op, s)
		e.mu.Lock()
		if !ok {
			if !e.failed {
				e.record.Outcome = Rejected
			}
			e.record.Reason = reason
		}
		e.mu.Unlock()
		if !ok {
			continue
		}
		if m.Check != nil {
			e.mu.Lock()
			e.phase, e.failed = Check, false
			e.mu.Unlock()
			m.Check(t, s)
		}
	}
}

// execReporter is the reporter an execution wraps outside a test. It reports
// nothing itself: the execution has already recorded the failure, and there
// is no test log to write to. Its one job is the temporary directories
// [T.TempDir] hands out, which no testing.TB is present to remove.
type execReporter struct {
	mu   sync.Mutex
	dirs []string
}

func (r *execReporter) Cleanup(func()) {}

func (r *execReporter) Errorf(string, ...any) {}

func (r *execReporter) Fatalf(string, ...any) { panic(fatal{}) }

func (r *execReporter) Logf(string, ...any) {}

func (r *execReporter) Helper() {}

func (r *execReporter) Name() string { return "" }

func (r *execReporter) testingTB() testing.TB { return nil }

func (r *execReporter) TempDir() string {
	dir, err := os.MkdirTemp("", "fuzztape-")
	if err != nil {
		// Reported through the reporter rather than the execution: this
		// is the reporter's own failure, not the phase's.
		panic(fmt.Sprintf("fuzztape: create temp dir: %v", err))
	}
	r.mu.Lock()
	r.dirs = append(r.dirs, dir)
	r.mu.Unlock()
	return dir
}

func (r *execReporter) removeDirs() {
	r.mu.Lock()
	dirs := r.dirs
	r.dirs = nil
	r.mu.Unlock()
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.RemoveAll(dirs[i])
	}
}
