package linear_test

import (
	"fmt"

	"github.com/tmc/fuzztape/linear"
)

// Check searches for a sequential order of overlapping operations that
// respects real-time intervals and the reference step function.
func ExampleCheck() {
	var h linear.History[int, int]
	w := h.Start("write", 1)
	r := h.Start("read", 0)
	r.Done(1)
	w.Done(0)

	step := func(state, in, out int) (int, bool) {
		if in != 0 {
			return in, out == 0
		}
		return state, out == state
	}
	order, ok := linear.Check(&h, 0, step)
	fmt.Println(ok, order)
	// Output:
	// true [write read]
}
