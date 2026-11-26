package splice_test

import (
	"fmt"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/splice"
)

// Cross combines two corpus inputs at the operation boundaries reported by
// Machine.Exec in Result.Splits.
func ExampleCross() {
	m := fuzztape.Machine[*int]{
		Init: func(*fuzztape.T) *int { return new(int) },
		Ops: []fuzztape.Op[*int]{
			{Name: "inc", Apply: func(_ *fuzztape.T, n *int) { *n++ }},
			{Name: "dec", Apply: func(_ *fuzztape.T, n *int) { *n-- }},
		},
		MaxOps: 4,
	}
	a, b := []byte{0, 0}, []byte{1, 1}
	for _, s := range splice.Cross(a, m.Exec(a).Splits, b, m.Exec(b).Splits) {
		fmt.Printf("%x\n", s)
	}
	// Output:
	// 01
	// 000101
	// 0001
	// 00
	// 00000101
	// 000001
}
