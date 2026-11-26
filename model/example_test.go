package model_test

import (
	"fmt"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/model"
)

// Pair runs a system under test beside a reference model, applying each
// drawn argument to both and failing when their results or states diverge.
func Example() {
	m := fuzztape.Machine[model.Pair[*int, *int]]{
		Init: model.Init(
			func(*fuzztape.T) *int { return new(int) },
			func(*fuzztape.T) *int { return new(int) },
		),
		Ops: []fuzztape.Op[model.Pair[*int, *int]]{
			model.Op("add", fuzztape.IntRange(1, 3),
				func(s *int, v int) int { *s = min(*s+v, 2); return *s }, // buggy cap
				func(ref *int, v int) int { *ref += v; return *ref },
			),
		},
		Check:  model.Equal(func(s *int) int { return *s }, func(ref *int) int { return *ref }),
		MaxOps: 2,
	}
	res := m.Exec([]byte{0, 2}) // selector 2 draws upper bound 3
	fmt.Println(res.Failure.Message)
	// Output:
	// model: add(3) = 2, model says 3
}
