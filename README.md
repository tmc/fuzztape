# fuzztape

[![Go Reference](https://pkg.go.dev/badge/github.com/tmc/fuzztape.svg)](https://pkg.go.dev/github.com/tmc/fuzztape)

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
		Init: func(t *fuzztape.T) *Stack { return new(Stack) },
		Ops: []fuzztape.Op[*Stack]{
			{Name: "push", Apply: func(t *fuzztape.T, s *Stack) {
				s.Push(t.Byte())
			}},
			{Name: "pop", When: func(s *Stack) bool { return s.Len() > 0 },
				Apply: func(t *fuzztape.T, s *Stack) {
					s.Pop()
				}},
		},
		Check: func(t *fuzztape.T, s *Stack) {
			if s.Len() < 0 {
				t.Fatalf("negative length %d", s.Len())
			}
		},
		Name: "FuzzStack",
	}

	func FuzzStack(f *testing.F) { m.Fuzz(f) }
	func TestStack(t *testing.T) { m.Run(t, 500) }

Both entry points share one corpus: a failure found by `Run` is shrunk
and saved as a seed input, and `Run` replays the saved corpus before its
random cases, so a bug found either way is checked by both. Setting
`Machine.Bubble` runs each case inside a `testing/synctest` bubble,
which makes every case a goroutine-leak check as well.

Ops receive a `*fuzztape.T`: the tape they draw from and the failure
reporting of the test running them. Report a violation with `t.Fatalf`,
abandon an op that turns out not to apply with `t.Reject`, and assert
what should hold once the sequence has settled from `t.Cleanup`.

## Subpackages

Each addresses one way a stateful test can pass while testing nothing.

- `budget` — an allocation ceiling tied to input size, and a ledger
  requiring every acquire to be matched by a release. Catches the
  decoder that allocates a gigabyte without panicking, and the resource
  leak that only wedges thousands of operations later.
- `corpus` — seed loading, and an audit naming the saved inputs that no
  longer decode to a distinct op sequence.
- `faults` — tape-driven fault injection: one-shot read and write
  errors, dropped writes, and sleeps that advance virtual time.
- `linear` — linearizability checking for overlapping operations, where
  no single answer is the right one and only the existence of *some*
  valid order settles correctness.
- `model` — comparison against a reference implementation, or against a
  second real one. Catches the wrong answer that leaves a plausible
  state, which no invariant can see.
- `sched` — goroutine interleaving as a tape decision, so a race
  reproduces exactly from its corpus file and shrinks to the shortest
  schedule that triggers it.
- `splice` — new seeds built by crossing corpus inputs at operation
  boundaries, which the byte-level mutator cannot find on its own.
- `stats` — per-op applied and rejected counts, so a machine that has
  silently stopped exercising an operation says so.
- `trace` — a failing input turned into a test you can paste.

## Command

`go test -fuzz` fuzzes exactly one target, in one package, per
invocation; `go test -fuzz ./...` does not report that as an error, it
silently fuzzes one arbitrary target forever. The `fuzztape` command is
the loop that fixes it.

	go install github.com/tmc/fuzztape/cmd/fuzztape@latest

	fuzztape list             # every target in the module
	fuzztape run -time 5m     # each one, in turn, with its own budget
	fuzztape matrix           # JSON for a GitHub Actions matrix

## Requirements

Go 1.26 or later, for `testing/synctest`.

Under Go 1.27 or later the package additionally provides method
spellings of four of its functions — `Tape.Draw`, `Tape.Pick`,
`Tape.OneOf`, and `Gen.Map` — for left-to-right composition. They are
one-line delegations to the free functions, which remain the portable
spelling; code written against the methods does not build under 1.26.

Formatting the repository requires a 1.27 or later `gofmt`, which is
the only toolchain that can parse the file those methods live in, tag
or no tag.

## Provenance

Fuzztape was developed inside [go-iroh](https://github.com/tmc/go-iroh)
as `internal/fuzztape` and extracted here with its history. Its first
consumers were that project's QUIC stream flow-control, socket path
watcher, and content-store machines. The subpackages are shaped by the
bugs that project actually hit: a stream-credit leak invisible until the
pool ran dry, a decoder that allocated gigabytes on an 11-byte frame, a
data-loss bug found by differential fuzzing against another
implementation.

## API stability

The API is not yet stable. It may change without notice before a v1
tag. v0.2.0 changed `Op.Apply`, `Machine.Init`, and `Machine.Check` to
take a `*fuzztape.T`, and `Apply` no longer returns an error — use
`t.Reject`.
