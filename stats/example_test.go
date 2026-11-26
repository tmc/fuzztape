package stats_test

import (
	"fmt"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/stats"
)

type stdoutLog struct{}

func (stdoutLog) Logf(format string, args ...any) {
	fmt.Printf(format+"\n", args...)
}

// Wrap counts applied and rejected ops without altering tape decoding, and
// Report.Log reports per-op totals and flags ops that never applied.
func ExampleWrap() {
	ops, report := stats.Wrap([]fuzztape.Op[*int]{
		{Name: "inc", Apply: func(_ *fuzztape.T, n *int) { *n++ }},
		{Name: "dec", Apply: func(t *fuzztape.T, n *int) {
			if *n == 0 {
				t.Rejectf("counter is %d", *n)
			}
			*n--
		}},
	})
	m := fuzztape.Machine[*int]{
		Init:   func(*fuzztape.T) *int { return new(int) },
		Ops:    ops,
		MaxOps: 2,
	}
	m.Exec([]byte{1, 0}) // dec rejected at 0, then inc applied
	report.Log(stdoutLog{})
	fmt.Println("missing:", report.Missing())
	// Output:
	// stats: inc                  applied      1	rejected      0
	// stats: dec                  applied      0	rejected      1	NEVER APPLIED
	// missing: [dec]
}
