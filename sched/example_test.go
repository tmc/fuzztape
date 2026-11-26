package sched_test

import (
	"testing"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/sched"
)

// Scheduler interleaves goroutines deterministically from the tape inside a
// Machine.Bubble synctest bubble.
func Example() {
	type state struct {
		sched *sched.Scheduler
		n     int
	}
	m := fuzztape.Machine[*state]{
		Bubble: true,
		Init: func(t *fuzztape.T) *state {
			return &state{sched: sched.New(t)}
		},
		Ops: []fuzztape.Op[*state]{
			{Name: "inc", Apply: func(_ *fuzztape.T, s *state) {
				s.sched.Go("inc", func() {
					v := s.n
					s.sched.Yield()
					s.n = v + 1
				})
			}},
			sched.StepOp("step", func(s *state) *sched.Scheduler { return s.sched }),
			sched.SettleOp("settle", func(s *state) *sched.Scheduler { return s.sched }),
		},
	}
	var t *testing.T
	m.Run(t, 100)
}
