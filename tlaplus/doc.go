// Package tlaplus checks recorded executions against a TLA+
// specification.
//
// A fuzztape machine compares its system under test with a reference,
// and a reference written in Go returns one answer. Some contracts do
// not have one: after a crash, a write-ahead log may hold any prefix of
// its unsynced records. TLA+ states such a contract directly, as a set
// of allowed next states, and a [Trace] checks that each state the
// system reaches is one the specification allows.
//
// The package evaluates a subset of TLA+ in pure Go, so the check runs
// inside go test on every fuzz input, and a violation shrinks like any
// other failure. It is not a model checker: it never explores states
// the execution did not reach. Use TLC for that.
//
// States are partially observed. An observation names the variables
// the test can see; the specification computes the rest (ghost
// variables such as "the contents as of the last Sync") from its own
// actions. Where an action leaves an unobserved variable open, a Trace
// keeps every value consistent with the execution so far and fails only
// when none remain.
//
// An action determines a primed variable through a conjunct x' = e or
// x' \in S, or through UNCHANGED. The conjunct binds x' if it is still
// open and tests it otherwise, so conjuncts that mention x' must follow
// the one that determines it, as TLC also requires.
//
// Go values map to TLA+ values as follows: bool to BOOLEAN; any integer
// type to an integer; string to a string; [ModelValue] to a TLC model
// value; a slice or array to a sequence; map[K]struct{} to a set; any
// other map to a function; a struct to a record, keyed by field name.
// Values printed in errors use TLA+ syntax.
//
// A module's CONSTANTS are set from Go with [Spec.With], which plays
// the part of a TLC model's CONSTANT block:
//
//	spec, err = spec.With(map[string]any{
//		"Clients": map[tlaplus.ModelValue]struct{}{"c1": {}, "c2": {}},
//		"MaxLen":  3,
//	})
//
// As in TLC, a model value equals itself and no other value, and is in
// none of the built-in sets such as Nat and STRING.
//
// Supported: MODULE, EXTENDS of Naturals, Integers, Sequences,
// FiniteSets and TLC, CONSTANT(S), VARIABLE(S), ASSUME, operator and
// function definitions (including RECURSIVE and LOCAL), and expressions
// over booleans, integers, strings, model values, sets, functions,
// records and tuples. Not supported: operator constants such as
// CONSTANT F(_), INSTANCE, user-module EXTENDS, temporal
// operators, ENABLED, LAMBDA, action composition, real numbers, and
// enumerating infinite sets. A definition outside the subset, such as
// Spec == Init /\ [][Next]_vars, is an error only if it is used.
//
// Integers are 64 bits, and overflow is an error. CHOOSE picks the
// least value satisfying its predicate, ordering strings bytewise; TLC
// orders strings differently, so a CHOOSE over strings may pick a
// different value than TLC does.
//
// In a fuzztape machine's op:
//
//	tr.Step(tlaplus.State{"kv": s.Snapshot()}, "Put", key, value)
package tlaplus
