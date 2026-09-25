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

const lock = `
---- MODULE Lock ----
CONSTANTS Clients, Free
ASSUME Free \notin Clients
VARIABLE holder
Init == holder = Free
Acquire(c) == c \in Clients /\ holder = Free /\ holder' = c
Release(c) == holder = c /\ holder' = Free
====
`

func ExampleSpec_With() {
	spec, err := tlaplus.Parse("Lock.tla", []byte(lock))
	if err != nil {
		log.Fatal(err)
	}
	c1, c2, free := tlaplus.ModelValue("c1"), tlaplus.ModelValue("c2"), tlaplus.ModelValue("Free")
	spec, err = spec.With(map[string]any{
		"Clients": map[tlaplus.ModelValue]struct{}{c1: {}, c2: {}},
		"Free":    free,
	})
	if err != nil {
		log.Fatal(err)
	}
	tr, err := spec.Start("Init", tlaplus.State{"holder": free})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tr.Step(tlaplus.State{"holder": c1}, "Acquire", c1))
	fmt.Println(tr.Step(tlaplus.State{"holder": c2}, "Acquire", c2))
	// Output:
	// <nil>
	// step 2: Acquire(c2): Lock.tla:7:32: holder = Free is false: c1 # Free
}
