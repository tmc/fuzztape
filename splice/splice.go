// Package splice builds new fuzz seeds by cutting corpus inputs at
// operation boundaries.
//
// The byte-level mutator inside go test -fuzz cannot do semantic
// crossover — take the setup from one interesting input and the
// trigger from another — because it does not know where operations
// begin. Machine.Splits recovers those offsets by replaying an input;
// this package combines inputs at them. Seed the results with f.Add:
//
//	func FuzzQueue(f *testing.F) {
//		for _, s := range splice.Cross(a, aSplits, b, bSplits) {
//			f.Add(s)
//		}
//		machine.Fuzz(f)
//	}
//
// A spliced input is a valid tape by construction — every byte string
// is — but the ops decoded after the cut only match b's if the machine
// state happens to line up; the value of splicing is that whole-op
// prefixes are preserved exactly.
package splice

import (
	"fmt"
	"slices"
)

// Cross returns every crossover of a and b: for each op boundary i of
// a and j of b, the input a[:i] + b[j:]. Splits come from
// Machine.Splits; a terminal offset is included only when the sequence
// ended cleanly rather than running out mid-op, so Cross only grafts
// onto genuine op boundaries. Degenerate combinations — empty, all of
// a, all of b — are omitted, as are duplicates.
func Cross(a []byte, aSplits []int, b []byte, bSplits []int) [][]byte {
	var out [][]byte
	seen := make(map[string]bool)
	for _, i := range aSplits {
		for _, j := range bSplits {
			if i == 0 && j == 0 {
				continue // all of b
			}
			if i >= len(a) && j >= len(b) {
				continue // all of a
			}
			s := slices.Concat(a[:min(i, len(a))], b[min(j, len(b)):])
			if len(s) == 0 || seen[string(s)] {
				continue
			}
			seen[string(s)] = true
			out = append(out, s)
		}
	}
	return out
}

// Delete returns data with the ops in [i, j) removed, where i and j
// index splits. It panics if i < 0, j < i, or j >= len(splits).
func Delete(data []byte, splits []int, i, j int) []byte {
	if i < 0 || j < i || j >= len(splits) {
		panic(fmt.Sprintf("splice: Delete range [%d, %d) out of bounds for %d splits", i, j, len(splits)))
	}
	return slices.Concat(data[:splits[i]], data[splits[j]:])
}
