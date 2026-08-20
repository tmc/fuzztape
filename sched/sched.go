// Package sched turns goroutine interleaving into a tape decision.
//
// [fuzztape.Machine] Bubble already makes time virtual and every case a
// goroutine-leak check, but it leaves the order in which goroutines run
// to the runtime. That order is the one dimension of a concurrent
// system a fuzzer otherwise cannot steer, and it is where the bugs that
// take longest to find live: a value read before a yield and written
// after it, a resource released on one path and not on another, a
// reader parked at the moment a late message arrives.
//
// A Scheduler makes that order part of the input. Goroutines started
// with [Scheduler.Go] begin parked; a goroutine calls [Scheduler.Yield]
// at each point where another may legally interleave; and
// [Scheduler.Step] — driven by an ordinary op — releases one parked
// goroutine, chosen by the tape. The schedule is therefore a
// front-to-back sequence of tape decisions like any other, so a race
// reproduces exactly from its corpus file and shrinks to the shortest
// interleaving that still triggers it.
//
// The cost is honest and worth stating: the system under test has to
// call Yield at the points where interleaving matters. That is the same
// bargain deterministic simulation testing always makes. A scheduler
// cannot preempt a goroutine that never offers a scheduling point, and
// nothing here weakens the case for also running under -race.
//
// A Scheduler requires Machine.Bubble, because it uses
// [testing/synctest.Wait] to know when every other goroutine has
// stopped.
package sched

import (
	"slices"
	"strings"
	"sync"
	"testing/synctest"

	"github.com/tmc/fuzztape"
)

// maxSteps bounds [Scheduler.Settle], so a goroutine that yields
// forever is reported rather than hanging the run.
const maxSteps = 10000

// A Scheduler runs registered goroutines one at a time, in an order
// drawn from the tape. Use [New] to make one; the zero value is not
// usable.
type Scheduler struct {
	t *fuzztape.T

	mu      sync.Mutex
	parked  []parked
	pending int // started by Go, not yet at its first park
	order   []string
}

// parked is a goroutine waiting for the scheduler to release it.
type parked struct {
	name string
	ch   chan struct{}
}

// New returns a Scheduler drawing its decisions from t, and registers a
// cleanup that drains the remaining goroutines when the op sequence
// ends. Call it from [fuzztape.Machine] Init.
//
// Draining matters for the diagnosis: a goroutine still parked when the
// sequence ends is one the schedule never released, and the bubble's
// exit check would report it as a leak, which it is not. A goroutine
// blocked on something the scheduler does not control is a different
// matter, and the exit check should — and still does — report it.
func New(t *fuzztape.T) *Scheduler {
	s := &Scheduler{t: t}
	t.Cleanup(s.Settle)
	return s
}

// Go starts f as a scheduled goroutine. It begins parked, so the tape
// chooses when it first runs, not only how it interleaves afterward.
func (s *Scheduler) Go(name string, f func()) {
	s.mu.Lock()
	s.pending++
	s.mu.Unlock()
	go func() {
		s.park(name, true)
		f()
	}()
}

// Yield offers a scheduling point: the calling goroutine parks, and
// runs again only when the tape selects it. Call it wherever another
// goroutine may legally observe or modify shared state — around the
// gap between a read and the write that depends on it, before and
// after a lock is released, at every send and receive worth
// interleaving.
func (s *Scheduler) Yield() { s.park("yield", false) }

// park adds the calling goroutine to the parked set and blocks it.
// initial distinguishes the first park of a goroutine started by Go,
// which settles the pending count, from a later Yield.
func (s *Scheduler) park(name string, initial bool) {
	ch := make(chan struct{})
	s.mu.Lock()
	if initial {
		s.pending--
	}
	s.parked = append(s.parked, parked{name, ch})
	s.mu.Unlock()
	<-ch
}

// Step releases one parked goroutine, chosen by the tape, and reports
// whether there was one to release. It must be called from the op
// goroutine — that is, from an [fuzztape.Op] Apply — and never from a
// scheduled goroutine.
//
// Step waits on both sides of the release, and both waits are load
// bearing.
//
// Before choosing, it waits for every other goroutine to be durably
// blocked, so the choice is made against the complete set of goroutines
// that could run rather than whichever happened to have parked already
// — a goroutine started by the immediately preceding op may not have
// reached its first park yet.
//
// After releasing, it waits again, so Step returns only once the
// released goroutine has run to its next scheduling point or finished.
// That is what makes exactly one goroutine runnable at a time. Without
// it the op goroutine would continue while the released one was still
// executing, and everything after Apply — the invariant in
// [fuzztape.Machine] Check above all — would be reading the system under
// test concurrently with it. That is a data race in the harness, not in
// the system under test, and it would be reported against whichever
// line of the system under test happened to be running.
func (s *Scheduler) Step() bool {
	synctest.Wait()

	s.mu.Lock()
	if len(s.parked) == 0 {
		s.mu.Unlock()
		return false
	}
	i := s.t.IntN(len(s.parked))
	p := s.parked[i]
	s.parked = slices.Delete(s.parked, i, i+1)
	s.order = append(s.order, p.name)
	s.mu.Unlock()

	close(p.ch)
	synctest.Wait()
	return true
}

// Settle steps until no goroutine is parked, running the schedule to
// completion. It is registered as a cleanup by [New], and is also worth
// calling from an op that must observe a quiesced system.
//
// A goroutine that parks again every time it is released never lets
// Settle finish; rather than hang, Settle gives up after a large number
// of steps and fails the test.
func (s *Scheduler) Settle() {
	for n := 0; ; n++ {
		if n >= maxSteps {
			s.t.Errorf("sched: still scheduling after %d steps; a goroutine yields without making progress\nschedule: %s",
				maxSteps, s.String())
			return
		}
		if !s.Step() {
			return
		}
	}
}

// Parked returns the number of goroutines waiting to be scheduled,
// counting those started by [Scheduler.Go] that have not yet reached
// their first park. Counting them is what makes it usable from an
// [fuzztape.Op] When gate: a goroutine started by the immediately
// preceding op has not necessarily run yet, and an uncounted one would
// disable the very op meant to release it.
//
// [Scheduler.Step] does not rely on this count. It waits for the bubble
// to quiesce first, by which point every started goroutine has parked.
func (s *Scheduler) Parked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.parked) + s.pending
}

// Order returns the names of the goroutines released so far, in order.
// It is the schedule that produced the current state, and belongs in
// the failure message of any invariant a schedule can break.
func (s *Scheduler) Order() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.order)
}

// String returns the schedule as a single line.
func (s *Scheduler) String() string {
	return strings.Join(s.Order(), " ")
}

// StepOp returns an op that releases one parked goroutine. It is the
// ordinary way to put scheduling decisions under the tape's control:
// with it in the operation set, the machine interleaves the system
// under test's goroutines between its other operations.
func StepOp[S any](name string, get func(S) *Scheduler) fuzztape.Op[S] {
	return fuzztape.Op[S]{
		Name:  name,
		When:  func(s S) bool { return get(s).Parked() > 0 },
		Apply: func(t *fuzztape.T, s S) { get(s).Step() },
	}
}

// SettleOp returns an op that runs the schedule to quiescence.
func SettleOp[S any](name string, get func(S) *Scheduler) fuzztape.Op[S] {
	return fuzztape.Op[S]{
		Name:  name,
		When:  func(s S) bool { return get(s).Parked() > 0 },
		Apply: func(t *fuzztape.T, s S) { get(s).Settle() },
	}
}
