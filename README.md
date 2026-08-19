# fuzztape

Typed and stateful testing over `go test -fuzz`.

The fuzzing engine built into `go test` mutates and minimizes flat byte
slices. Fuzztape layers structure on top of those bytes without hiding
them from the engine, so coverage guidance, corpus files, and input
minimization keep working while tests operate on typed values and
operation sequences.

	import "github.com/tmc/fuzztape"

No dependencies outside the standard library.

## Tapes

A `Tape` reads a fuzz input as a sequence of typed decisions: bytes,
booleans, integers, and choices. Reads past the end of the input return
zero values, so every input decodes to a valid value and shorter inputs
decode to simpler ones. Consumption is strictly front-to-back, which
lets the engine's byte-level minimizer shrink decoded values and
operation sequences without knowing they exist.

## Machines

A `Machine` runs a stateful property test: each fuzz input decodes to a
bounded sequence of operations applied to a fresh system under test,
with an invariant checked after every step.

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

	func FuzzStack(f *testing.F) { m.Fuzz(f) }
	func TestStack(t *testing.T) { m.Run(t, 500) }

Both entry points share one corpus: a failure found by `Run` is shrunk
and saved as a seed input that both modes replay. Setting
`Machine.Bubble` runs each case inside a `testing/synctest` bubble,
which makes every case a goroutine-leak check as well.

## Subpackages

- `faults` — tape-driven fault injection: one-shot read and write
  errors, dropped writes, and sleeps that advance virtual time.
- `splice` — new seeds built by crossing corpus inputs at operation
  boundaries, which the byte-level mutator cannot find on its own.
- `stats` — per-op applied and rejected counts, so a machine that has
  silently stopped exercising an operation says so.

## Requirements

Go 1.27 or later. The package uses generic methods (`Tape.Draw`,
`Tape.Pick`, `Tape.OneOf`, `Gen.Map`) and `testing/synctest`.

## Provenance

Fuzztape was developed inside [go-iroh](https://github.com/tmc/go-iroh)
as `internal/fuzztape` and extracted here with its history. Its first
consumers were that project's QUIC stream flow-control, socket path
watcher, and content-store machines.

## API stability

The API is not yet stable. It may change without notice before a v1
tag.
