package fuzztape_test

import (
	"fmt"
	"strconv"

	"github.com/tmc/fuzztape"
)

// A Tape decodes a fuzz input front to back; reads past the end return
// zero values, so any prefix of an input is itself a valid input.
func Example() {
	tape := fuzztape.New([]byte{0x2a, 0x03})
	fmt.Println(tape.Byte())
	fmt.Println(tape.Bool())
	fmt.Println(tape.Byte()) // exhausted: zero value
	// Output:
	// 42
	// true
	// 0
}

// Generators compose over a tape; a zero (or exhausted) tape decodes to
// the simplest value each generator offers.
func ExampleGen() {
	size := fuzztape.IntRange(1, 8)
	sizes := fuzztape.SliceOf(size, 4)
	fmt.Println(sizes(fuzztape.New(nil)))
	fmt.Println(size(fuzztape.New([]byte{2}))) // selector 2 forces the upper boundary
	// Output:
	// []
	// 8
}

func ExamplePick() {
	tape := fuzztape.New(nil)
	fmt.Println(fuzztape.Pick(tape, []string{"open", "close", "reset"}))
	// Output:
	// open
}

// Weighted selects proportionally; a zero tape takes the first option
// with a positive weight.
func ExampleWeighted() {
	g := fuzztape.Weighted([]string{"read", "write"}, []int{1, 9})
	fmt.Println(g(fuzztape.New(nil)))
	// Output:
	// read
}

// Float64 skews toward the values that break arithmetic. Selector 6
// draws NaN, which a uniform bit pattern reaches vanishingly rarely.
func ExampleFloat64() {
	g := fuzztape.Float64()
	fmt.Println(g(fuzztape.New(nil)))
	fmt.Println(g(fuzztape.New([]byte{6})))
	// Output:
	// 0
	// NaN
}

// An op can draw its parameter through a generator, which keeps the
// value typed and lets the same draw drive a reference implementation.
func ExampleNewOp() {
	type queue struct{ xs []int }
	op := fuzztape.NewOp("push", fuzztape.IntRange(1, 100),
		func(t *fuzztape.T, q *queue, v int) { q.xs = append(q.xs, v) })
	fmt.Println(op.Name)
	// Output:
	// push
}

// NewRecordedOp renders the argument an op drew, so that a history says
// which value was used and not only which op ran. NewOp records no
// argument, because most argument types have no stable external form.
func ExampleNewRecordedOp() {
	type queue struct{ xs []int }
	m := fuzztape.Machine[*queue]{
		Init: func(*fuzztape.T) *queue { return new(queue) },
		Ops: []fuzztape.Op[*queue]{
			fuzztape.NewRecordedOp("push",
				func(t *fuzztape.Tape) int { return int(t.Byte()) },
				strconv.Itoa,
				func(_ *fuzztape.T, q *queue, v int) { q.xs = append(q.xs, v) }),
		},
		MaxOps: 2,
	}
	for _, r := range m.Exec([]byte{1, 5, 1, 8}).Records {
		fmt.Println(r.Name, r.Args)
	}
	// Output:
	// push 5
	// push 8
}

// Exec runs an input outside a test: it returns the decoded history and
// the first failure rather than reporting to a testing.T, which is what
// a corpus audit, a benchmark harness, or any other library consumer
// needs. It is the same execution Run and Fuzz drive, so an input means
// the same sequence either way.
func ExampleMachine_Exec() {
	m := fuzztape.Machine[*int]{
		Init: func(*fuzztape.T) *int { return new(int) },
		Ops: []fuzztape.Op[*int]{
			{Name: "inc", Apply: func(_ *fuzztape.T, n *int) { *n++ }},
		},
		Check: func(t *fuzztape.T, n *int) {
			if *n == 2 {
				t.Errorf("counter reached %d", *n)
			}
		},
		MaxOps: 3,
	}
	result := m.Exec([]byte{0, 0, 0})
	fmt.Println(len(result.Records), "ops")
	fmt.Printf("%s after op %d: %s\n", result.Failure.Phase, result.Failure.Op, result.Failure.Message)
	// Output:
	// 3 ops
	// check after op 1: counter reached 2
}
