# Changelog

## Unreleased

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

  - faults.ShortWriteOp and (*faults.ReadWriter).ShortWrite inject
    partial writes: the prefix reaches the underlying writer and the
    caller sees the short count with an error. This finds torn records
    that dropped or failed writes cannot.
