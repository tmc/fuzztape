package budget_test

import (
	"fmt"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/budget"
)

// Ledger.Check verifies at sequence cleanup that every acquired resource
// was released and no release went unmatched.
func ExampleLedger() {
	m := fuzztape.Machine[*budget.Ledger]{
		Init: func(t *fuzztape.T) *budget.Ledger {
			l := &budget.Ledger{}
			l.Check(t)
			return l
		},
		Ops: []fuzztape.Op[*budget.Ledger]{
			{Name: "open", Apply: func(_ *fuzztape.T, l *budget.Ledger) { l.Acquire("conn") }},
			{Name: "close", Apply: func(_ *fuzztape.T, l *budget.Ledger) { l.Release("conn") }},
		},
		MaxOps: 1,
	}
	res := m.Exec([]byte{0}) // runs "open" without a matching "close"
	fmt.Printf("%s: %s\n", res.Failure.Phase, res.Failure.Message)
	// Output:
	// cleanup: budget: resources outstanding at end of sequence: conn x1
}
