/*
Package fuzztape adapts go test -fuzz for typed and stateful testing.

The fuzzing engine built into go test mutates and minimizes flat byte
slices. Fuzztape layers structure on top of those bytes without hiding
them from the engine, so coverage guidance, corpus files, and input
minimization keep working while tests operate on typed values and
operation sequences.

# Tapes

A [Tape] reads a fuzz input as a sequence of typed decisions: bytes,
booleans, integers, and choices. Reads past the end of the input return
zero values, so every input decodes to a valid value and shorter inputs
decode to simpler ones. Consumption is strictly front-to-back, which
lets the engine's byte-level minimizer shrink decoded values and
operation sequences without knowing they exist: truncating an input
truncates the decoded behavior.

[Tape.IntN] deliberately skews toward boundary values — 0, 1, n-1, and
powers of two — because most bugs live at boundaries and uniform random
bytes rarely land there.

# Generators

A [Gen] composes typed generators over a Tape. Generators are plain
functions, combined with [Const], [IntRange], [OneOf], [Map], and
[SliceOf]; every generator bottoms out in Tape reads, so the tape's
decoding and shrinking properties hold for generated values
automatically.

# Machines

A [Machine] runs a stateful property test: each fuzz input decodes to a
bounded sequence of operations applied to a fresh system under test,
with an invariant checked after every step. A machine runs either under
go test -fuzz ([Machine.Fuzz]) or as a bounded number of pseudo-random
cases inside an ordinary test ([Machine.Run]), sharing one corpus: a
failure found by Run is shrunk and saved as a seed input that both modes
replay.

A typical machine and its two entry points:

	var m = fuzztape.Machine[*Stack]{
		Init: func(t *testing.T) *Stack { return new(Stack) },
		Ops: []fuzztape.Op[*Stack]{
			{Name: "push", Apply: func(s *Stack, t *fuzztape.Tape) error {
				s.Push(t.Byte())
				return nil
			}},
			{Name: "pop", When: func(s *Stack) bool { return s.Len() > 0 },
				Apply: func(s *Stack, t *fuzztape.Tape) error {
					s.Pop()
					return nil
				}},
		},
		Check: func(t *testing.T, s *Stack) {
			if s.Len() < 0 {
				t.Fatalf("negative length %d", s.Len())
			}
		},
		Name: "FuzzStack",
	}

	func FuzzStack(f *testing.F)  { m.Fuzz(f) }
	func TestStack(t *testing.T)  { m.Run(t, 500) }

Setting [Machine.Bubble] runs each case inside a [testing/synctest]
bubble: time is virtual, and the bubble's exit check turns every case
into a goroutine-leak check.

# Subpackages

Subpackage faults injects tape-driven I/O faults, so a shrunk input
names the single fault that breaks an invariant. Subpackage splice
builds seeds by crossing corpus inputs at the operation boundaries
[Machine.Splits] recovers. Subpackage stats counts how often each op
actually runs, turning a machine that has silently stopped exercising
an operation into a visible gap.

# Go version

The module builds with Go 1.26 and later. Under Go 1.27 and later it
additionally provides method spellings of four of its functions —
[Tape.Draw], [Tape.Pick], [Tape.OneOf], and [Gen.Map] — for
left-to-right composition. Those methods are compiled only by a 1.27
or later toolchain, and this documentation is built by one, so it
lists them unconditionally: code that must build under 1.26 has to use
the free functions [Pick], [OneOf], and [Map] instead.

# API stability

This package is pre-v1 and its API is not yet stable. It may change
without notice.
*/
package fuzztape
