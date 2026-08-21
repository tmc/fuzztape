// Package linear checks that a concurrent history is linearizable.
//
// The model subpackage compares a system under test against a reference
// one operation at a time, which settles correctness only while
// operations do not overlap. Once they do, "what should this have
// returned?" stops having a single answer: two concurrent withdrawals
// may legitimately observe either order, and a test that demands one of
// them reports failures that are not bugs.
//
// Linearizability is the property that replaces it. A history is
// linearizable if there is some sequential order of its operations that
// the reference implementation accepts, and that respects real time:
// an operation that returned before another was called must come first.
// Anything else the implementation is free to do.
//
// A [History] records when each operation was called and when it
// returned; [Assert] searches for a valid order and fails the test with
// the recorded history when none exists. The search is the Wing and
// Gong algorithm — depth-first over the operations that could go next,
// memoized on the set of operations remaining and the reference state
// reached, which is what keeps an exponential search tractable at the
// sizes a bounded op sequence produces.
//
// Use it with the sched subpackage. Overlapping operations only arise
// if something interleaves them, and a tape-driven scheduler makes that
// interleaving reproducible, so a linearizability violation shrinks to
// the shortest schedule that causes it rather than appearing once and
// never again.
package linear

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/tmc/fuzztape"
)

// maxOps bounds a history, because the search tracks the remaining
// operations as a bit set and its cost is exponential in the number
// that genuinely overlap.
const maxOps = 64

// An Op is one recorded operation: its arguments, its result, and the
// interval over which it ran.
type Op[I, O any] struct {
	Name string
	In   I
	Out  O

	start, end int
}

// A History records the operations a system under test performed and
// the intervals over which they ran. The zero History is ready to use.
// Methods are safe for concurrent use.
type History[I, O any] struct {
	mu    sync.Mutex
	clock int
	ops   []*Op[I, O]
}

// Start records the call of an operation and returns it. The caller
// must record the result with [Op.Done] when the operation returns.
//
// An operation that is never completed — a goroutine still in flight
// when the sequence ends — may be linearized anywhere after its call.
// That is what the definition requires: it may have taken effect and it
// may not, and no result was observed, so nothing about it can be
// contradicted. The search tries it both ways; see [Step].
func (h *History[I, O]) Start(name string, in I) *Op[I, O] {
	h.mu.Lock()
	defer h.mu.Unlock()
	op := &Op[I, O]{Name: name, In: in, start: h.clock, end: math.MaxInt}
	h.clock++
	h.ops = append(h.ops, op)
	return op
}

// Done records the result of an operation and the moment it returned.
func (op *Op[I, O]) Done(h *History[I, O], out O) {
	h.mu.Lock()
	defer h.mu.Unlock()
	op.Out = out
	op.end = h.clock
	h.clock++
}

// pending reports whether the operation never returned.
func (op *Op[I, O]) pending() bool { return op.end == math.MaxInt }

// Len returns the number of operations recorded.
func (h *History[I, O]) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.ops)
}

// String returns the history as one line per operation, in call order,
// marking any that never returned.
func (h *History[I, O]) String() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, op := range h.ops {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if op.pending() {
			fmt.Fprintf(&b, "\t[%d, pending) %s(%+v)", op.start, op.Name, op.In)
			continue
		}
		fmt.Fprintf(&b, "\t[%d, %d) %s(%+v) = %+v", op.start, op.end, op.Name, op.In, op.Out)
	}
	return b.String()
}

// A Step applies one operation to the reference state, reporting
// whether the recorded result was legal there and, if so, the state
// that follows. Returning false means this operation cannot be
// linearized at this point; the search will try it elsewhere. The
// returned state is read only when ok is true, so a Step is free to
// return anything alongside false.
//
// An operation still pending at the end of the history is offered to
// step with the zero Out, because no result was observed for it. A
// step that rejects that is not taken to mean the operation cannot go
// there: the search also tries the same point with the operation
// having had no effect at all.
type Step[S, I, O any] func(s S, in I, out O) (next S, ok bool)

// Check reports whether the history has a linearization under step
// starting from init, and returns the order it found. The state type
// must be comparable, because the search memoizes on it; a state that
// is not naturally comparable should be projected onto one that is —
// a string, an array, a hash.
//
// It panics on a history of more than 64 operations, which the search
// cannot represent. Returning false for one would be indistinguishable
// from a real violation, and the length of a history is a property of
// the test rather than of the system under test: lower Machine.MaxOps.
// [Assert] reports the same condition as a test failure instead.
func Check[S comparable, I, O any](h *History[I, O], init S, step Step[S, I, O]) (order []string, ok bool) {
	h.mu.Lock()
	ops := append([]*Op[I, O](nil), h.ops...)
	h.mu.Unlock()

	if len(ops) == 0 {
		return nil, true
	}
	if len(ops) > maxOps {
		panic(fmt.Sprintf("linear: history has %d operations, over the %d the search supports", len(ops), maxOps))
	}
	var mask uint64 = 1<<len(ops) - 1
	found := make([]string, 0, len(ops))
	if !search(ops, mask, init, step, map[memo[S]]bool{}, &found) {
		return nil, false
	}
	return found, true
}

// memo keys the search on what is left to linearize and the reference
// state reached, which is the whole of the remaining subproblem.
type memo[S comparable] struct {
	mask  uint64
	state S
}

// search is the Wing and Gong depth-first search: at each point it
// tries every operation that could legally be linearized next, which
// is every remaining operation called before the earliest remaining
// return. Anything called later must wait, because an operation that
// returned before it was called has to precede it.
func search[S comparable, I, O any](ops []*Op[I, O], mask uint64, state S, step Step[S, I, O], seen map[memo[S]]bool, order *[]string) bool {
	if mask == 0 {
		return true
	}
	k := memo[S]{mask, state}
	if seen[k] {
		return false
	}
	seen[k] = true

	earliestReturn := math.MaxInt
	for i, op := range ops {
		if mask&(1<<i) != 0 && op.end < earliestReturn {
			earliestReturn = op.end
		}
	}
	for i, op := range ops {
		if mask&(1<<i) == 0 || op.start > earliestReturn {
			continue
		}
		rest := mask &^ (1 << i)
		*order = append(*order, op.Name)
		if next, ok := step(state, op.In, op.Out); ok && search(ops, rest, next, step, seen, order) {
			return true
		}
		// A pending operation was never observed to return, so the
		// definition allows two things, and both have to be tried. It
		// may have taken effect — the branch above, which asks step
		// about the zero Out, the only result a Step can be asked
		// about for an operation that reported none. Or it may not
		// have taken effect at all, leaving the state untouched, which
		// is what this branch tries. Neither uses the state a step
		// that reported false returned: [Step] leaves that undefined.
		if op.pending() && search(ops, rest, state, step, seen, order) {
			return true
		}
		*order = (*order)[:len(*order)-1]
	}
	return false
}

// Assert fails the test unless the history is linearizable under step.
// The failure carries the whole history, because the operation that
// cannot be placed is rarely the operation at fault.
//
// Call it from a [fuzztape.Machine] Check, or from a cleanup registered
// in Init so that it runs once the sequence has settled.
func Assert[S comparable, I, O any](t *fuzztape.T, h *History[I, O], init S, step Step[S, I, O]) {
	t.Helper()
	if h.Len() > maxOps {
		t.Fatalf("linear: history has %d operations, over the %d the search supports; lower Machine.MaxOps",
			h.Len(), maxOps)
	}
	if _, ok := Check(h, init, step); !ok {
		t.Fatalf("linear: no sequential order explains this history:\n%s", h)
	}
}
