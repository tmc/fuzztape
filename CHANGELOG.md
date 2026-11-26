# Changelog

## Unreleased

Breaking changes:

  - T.Reject is renamed to T.Rejectf to match Go's Printf-style naming
    convention alongside T.Errorf, T.Fatalf and T.Logf.

  - Phase constants are prefixed with Phase: PhaseInit, PhaseApply,
    PhaseCheck and PhaseCleanup replace Init, Apply, Check and Cleanup.

  - Package trace is renamed to repro, and trace.Write is renamed to
    repro.WriteFile.

  - corpus.WriteFile is renamed to corpus.Save.

  - (*budget.Ledger).Balanced is renamed to (*budget.Ledger).Check.

  - (*linear.Op).Done no longer takes a *linear.History parameter. Each
    Op is bound to the History that created it via History.Start, and
    Op.Done(out) panics if called on an unstarted Op or more than once.

Fixes:

  - Fixed data races in Machine execution, execReporter temp-dir
    tracking and T.Cleanup registration, and documented T's unusable
    zero value and T.Rejectf's single-goroutine constraint.

  - Machine.Run, Machine.Fuzz and Machine.Exec validate Machine.Init,
    Machine.Ops and Op.Apply eagerly so misconfigurations fail
    immediately without shrinking or writing corpus files.

  - Weighted clones its input slices so later caller mutations do not
    alias the generator.

  - linear.Check, linear.Assert and (*linear.History).String snapshot
    recorded ops by value under the history mutex, eliminating data
    races with concurrent Start/Done calls and the TOCTOU check on the
    64-op limit.

  - sched.Scheduler preserves a goroutine's name across Yield so Order
    reports the goroutine's name instead of "yield", and documents LIFO
    T.Cleanup ordering on sched.New.

  - faults.DropOp panics eagerly when max <= 0, faults.ShortWriteOp
    panics when max == math.MaxInt, and splice.Delete panics on
    out-of-range [i, j) indices instead of duplicating bytes.

  - repro.WriteFile checks go/format.Source errors rather than writing
    unformatted invalid Go, and history and model consistently prefix
    returned errors and failure messages with their package names.

  - Package tlaplus fixes crashes, hangs, bounds and determinism bugs:
    guards ASSUME against variables, primes and UNCHANGED; terminates
    malformed RECURSIVE parameter lists at EOF; avoids int64
    wrap-around at MaxInt64 in interval iteration; bounds parser
    nesting and solveUnchanged recursion; caps Permutations and guards
    function-set size multiplication against 32-bit int overflow;
    tracks explicit finiteness on lazy sets for IsFiniteSet; fixes
    large interval membership; sorts map keys for deterministic errors
    in Spec.With and Spec.observe; defers unsupported LOCAL
    definitions; and uses binary search in Spec.except.

  - cmd/fuzztape forwards -tags to go test, matches cmd/go's Fuzz
    target naming rules (rejecting lowercase after Fuzz, generic
    functions and multi-name parameter lists), parses space-separated
    -tags/--tags in GOFLAGS, runs go test relative to target
    directories so external paths work, and validates subcommands and
    flags before target discovery.

New:

  - (*stats.Report).Log now accepts interface{ Logf(string, ...any) }
    instead of testing.TB, so *fuzztape.T satisfies it alongside
    *testing.T, *testing.F and testing.TB.

  - Result.Splits exposes the op byte offsets from Machine.Exec for use
    with splice, and (Outcome).String formats record outcomes.

## v0.3.0 (2026-11-20)

Behavior changes:

  - Machine.Splits no longer appends a terminal offset when the input
    ran out inside the last operation. That offset was not a boundary:
    bytes appended there were consumed by the truncated operation
    instead of starting a new one, so splice and Cross grafted onto a
    position that decoded differently on replay. Callers that indexed
    the last element unconditionally now see one fewer offset for a
    truncated tape; the per-operation offsets are unchanged.

  - T.IntN(n) for n not a power of two decodes a byte to the largest
    power of two below n rather than to n-1 in one of its cases, which
    changes which operation some existing corpus inputs select. Corpus
    files stay valid; they may decode to different sequences.

  - Shrinking keeps the failure it started from. A candidate is
    accepted only when its phase, message format and reporting call
    site match the original, so a shrink can no longer wander into an
    unrelated bug the fuzzer never found. A panic's site is the line
    that raised it.

  - sched.Scheduler.Step indexes the parked goroutines in the order
    Go started them rather than the order they reached the scheduler.
    Goroutines that park in the same quiescent interval race to the
    scheduler, so the old order varied from run to run: one input
    replayed in 100 fresh bubbles produced over 50 different
    schedules. Schedules now reproduce. Corpus files stay valid, but
    an input whose parked set was ever in a different order from
    spawn order — any schedule after a Yield — may now release a
    different goroutine for the same byte, as may the end-of-sequence
    drain, which now releases in spawn order.

  - trace.Repro keeps each line of a multi-line op inside the comment.
    An op whose text held a newline, such as a panic value, used to
    produce a file that did not compile.

  - IntRange panics when hi-lo+1 overflows int instead of silently
    returning values outside [lo, hi].

  - cmd/fuzztape honors build constraints and GOFLAGS tags when
    discovering targets, resolves renamed "testing" imports, skips
    nested modules, and reports files it could not classify on stderr,
    exiting non-zero after doing the rest of the work.

New:

  - Machine.Exec runs an input without a testing.T and returns a
    Result: the decoded history as Records, each with its Outcome, and
    the first Failure with its Phase. It shares one decoder with Run,
    Fuzz and Trace, so an input names the same sequence under each.
    NewRecordedOp records an op's drawn argument in its Record.

  - Int8, Int16, Int32, Int64, Uint8, Uint16, Uint32 and Uint64 are
    generators of fixed-width integers skewed toward the boundaries of
    fixed-width arithmetic: 0, ±1, MinInt, MaxInt and their inner
    neighbors, the bit width and its neighbors, and powers of two with
    their ±1 neighbors. In 1,000 draws from random bytes they produce
    both MinInt64 followed by -1 and a Uint64 shift count of 64. They
    are new functions reading their own bytes; no existing generator
    decodes differently.

  - Package history writes a Result's records as newline-delimited
    JSON for TLA+ trace validation — one event per record with a
    sequence number, op name, rendered args and outcome, then an end
    marker with the event count and per-op counts — and reads one
    back, rejecting a history that is truncated, has lost or
    reordered an event, or disagrees with its end marker.

  - faults.ShortWriteOp and (*faults.ReadWriter).ShortWrite inject
    partial writes: the prefix reaches the underlying writer and the
    caller sees the short count with an error. This finds torn records
    that dropped or failed writes cannot.

  - Package tlaplus checks a recorded execution against a TLA+
    specification, in pure Go, as a machine's reference. A Trace
    carries every state consistent with what the test observed, so a
    contract with more than one correct answer — the contents a
    write-ahead log may hold after a crash — is stated in the spec
    rather than hand-coded as a search over candidates. Spec.With sets
    a module's CONSTANTS from Go, as a TLC model does, and ModelValue
    stands for a TLC model value, so a spec written for TLC runs
    unchanged.
