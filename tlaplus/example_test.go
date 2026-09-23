package tlaplus_test

import (
	"fmt"
	"log"

	"github.com/tmc/fuzztape/tlaplus"
)

const counter = `
---- MODULE Counter ----
EXTENDS Naturals
VARIABLES n, flushed
Init == n = 0 /\ flushed = 0
Incr == n' = n + 1 /\ UNCHANGED flushed
Flush == flushed' = n /\ UNCHANGED n
Reset == n' = flushed /\ UNCHANGED flushed
====
`

func Example() {
	spec, err := tlaplus.Parse("Counter.tla", []byte(counter))
	if err != nil {
		log.Fatal(err)
	}
	// flushed is a ghost variable: the test never observes it.
	tr, err := spec.Start("Init", tlaplus.State{"n": 0})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tr.Step(tlaplus.State{"n": 1}, "Incr"))
	fmt.Println(tr.Step(tlaplus.State{"n": 1}, "Flush"))
	fmt.Println(tr.Step(tlaplus.State{"n": 2}, "Incr"))
	fmt.Println(tr.Step(tlaplus.State{"n": 0}, "Reset"))
	// Output:
	// <nil>
	// <nil>
	// <nil>
	// step 4: Reset: Counter.tla:8:10: n' = flushed is false: 0 # 1
}
