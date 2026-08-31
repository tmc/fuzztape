package fuzztape

import (
	"fmt"
	"os"
	"runtime/debug"
	"testing"
)

// An Outcome reports what happened when an op was selected.
type Outcome uint8

const (
	// Applied reports an op that returned without reporting a failure.
	Applied Outcome = iota
	// Rejected reports an op that called [T.Reject].
	Rejected
	// Failed reports an op whose Apply reported a failure or panicked.
	Failed
)

// A Record is one selected operation in an [Exec] result.
type Record struct {
	// Name is the selected operation's name.
	Name string
	// Args is the value rendered by [NewRecordedOp]. It is empty for an
	// op that did not supply a renderer.
	Args string
	// Outcome reports whether the selected operation applied, rejected,
	// or failed.
	Outcome Outcome
	// Reason is the reason a [Rejected] op gave to [T.Reject], or
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
}

// A Result is the history and first failure from [Machine.Exec]. Failure is
// nil when the machine passed.
type Result struct {
	Records []Record
	Failure *Failure
}

// Exec runs data against a fresh system and returns its decoded history and
// first failure. It does not require a testing.T.
//
// Exec drives the same execution as [Machine.Run] and [Machine.Fuzz] — one
// decoder, so an input means the same sequence however it is run — but it
// creates no synctest bubble. Machines that set [Machine.Bubble] must run
// through Run or Fuzz for the bubble's exit check to happen at all.
func (m Machine[S]) Exec(data []byte) Result {
	return m.exec1(data, nil)
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

// fail records a failure against the current phase without reporting it.
// Result.Failure keeps the first one; failed is per phase.
func (e *execution[S]) fail(format string, args ...any) {
	e.failed = true
	if e.phase == Apply && e.record != nil {
		e.record.Outcome = Failed
	}
	if e.result.Failure == nil {
		e.result.Failure = &Failure{
			Phase:   e.phase,
			Op:      e.op,
			Message: fmt.Sprintf(format, args...),
		}
	}
}

func (e *execution[S]) Errorf(format string, args ...any) {
	if tb := e.testingTB(); tb != nil {
		tb.Helper()
	}
	e.fail(format, args...)
	e.reporter.Errorf(format, args...)
}

func (e *execution[S]) Fatalf(format string, args ...any) {
	if tb := e.testingTB(); tb != nil {
		tb.Helper()
	}
	e.fail(format, args...)
	e.reporter.Fatalf(format, args...)
}

// where names the part of the machine currently running, for a panic message
// and for the history line that reports it.
func (e *execution[S]) where() string {
	switch e.phase {
	case Apply:
		return e.record.Name
	case Check:
		return "Check after " + e.record.Name
	case Cleanup:
		return "cleanup"
	default:
		return "Init"
	}
}

// panicked reports a panic as an ordinary failure of the current phase.
func (e *execution[S]) panicked(r any) {
	where := e.where()
	if e.phase == Apply && e.record != nil {
		e.record.Outcome = Failed
		e.record.Reason = fmt.Sprintf("panicked: %v", r)
	} else {
		e.note = fmt.Sprintf("%s (panicked: %v)", where, r)
	}
	if e.stacks {
		// The stack is captured here, at the recover, where the
		// panicking frames are still on it.
		e.Errorf("fuzztape: %s panicked: %v\n\n%s", where, r, debug.Stack())
		return
	}
	e.Errorf("fuzztape: %s panicked: %v", where, r)
}

// applied renders the history as the op-sequence lines a failing input logs.
func (e *execution[S]) applied() []string {
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
// splits is non-nil it records each op's starting tape offset, plus one final
// offset past the last byte consumed.
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
	// already recorded the failure. Reject's panic is unwound by apply
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
			e.Errorf("fuzztape: Reject called outside an op, in %s", e.where())
			return
		}
		e.panicked(r)
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
		phase, op := e.phase, e.op
		e.phase, e.op = Cleanup, len(e.result.Records)-1
		t.runCleanups()
		e.phase, e.op = phase, op
	}()
	if splits != nil {
		defer func() { *splits = append(*splits, t.Pos()) }()
	}
	if len(m.Ops) == 0 {
		e.Errorf("fuzztape: Machine has no Ops")
		return
	}
	t.record = func(args string) {
		if e.record != nil {
			e.record.Args = args
		}
	}

	e.phase, e.op = Init, -1
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

		e.result.Records = append(e.result.Records, Record{Name: op.Name, Outcome: Applied})
		e.record = &e.result.Records[len(e.result.Records)-1]
		e.phase, e.op, e.failed = Apply, len(e.result.Records)-1, false
		reason, ok := apply(t, op, s)
		if !ok {
			if !e.failed {
				e.record.Outcome = Rejected
			}
			e.record.Reason = reason
			continue
		}
		if m.Check != nil {
			e.phase, e.failed = Check, false
			m.Check(t, s)
		}
	}
}

// execReporter is the reporter an execution wraps outside a test. It reports
// nothing itself: the execution has already recorded the failure, and there
// is no test log to write to. Its one job is the temporary directories
// [T.TempDir] hands out, which no testing.TB is present to remove.
type execReporter struct {
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
	r.dirs = append(r.dirs, dir)
	return dir
}

func (r *execReporter) removeDirs() {
	for i := len(r.dirs) - 1; i >= 0; i-- {
		_ = os.RemoveAll(r.dirs[i])
	}
}
