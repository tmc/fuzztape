package fuzztape

import (
	"errors"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/tmc/fuzztape/internal/fuzzfile"
)

var seedFlag = flag.Int64("fuzztape.seed", 0, "seed for fuzztape Machine.Run case generation (0 derives one)")

// An Op is one operation of a stateful property test.
type Op[S any] struct {
	// Name labels the op in failure logs.
	Name string
	// Weight is the op's relative selection frequency; 0 means 1.
	Weight int
	// When reports whether the op is currently applicable;
	// nil means always. Ops with a false When are not selected.
	When func(s S) bool
	// Apply performs the op, drawing parameters from t. An op that
	// turns out not to apply once it has drawn calls [T.Rejectf]; an op
	// that finds a violation calls [T.Fatalf] or [T.Errorf].
	//
	// A panic out of Apply — from the system under test, most often —
	// is caught and reported as a failure with its stack, so it is
	// logged, shrunk, and saved like any other. That holds only for the
	// goroutine Apply runs on: a panic on a goroutine the op started
	// still kills the test binary, as it does anywhere in Go.
	Apply func(t *T, s S)
}

// NewOp returns an op that draws one parameter with g and passes it to
// apply. The parameter is drawn once, before apply runs, which is what
// lets two implementations of the same operation — a system under test
// and a reference model — be driven with identical input.
func NewOp[S, A any](name string, g Gen[A], apply func(t *T, s S, v A)) Op[S] {
	return Op[S]{
		Name:  name,
		Apply: func(t *T, s S) { apply(t, s, g(t.Tape)) },
	}
}

// NewRecordedOp returns an op that records the argument it draws with render.
// It panics if render is nil.
//
// NewOp records no argument value. That is useful for operations whose
// arguments have no stable external form, but callers that need to emit a
// history should use NewRecordedOp and choose that form explicitly.
func NewRecordedOp[S, A any](name string, g Gen[A], render func(A) string, apply func(t *T, s S, v A)) Op[S] {
	if render == nil {
		panic("fuzztape: NewRecordedOp called with nil render")
	}
	return Op[S]{
		Name: name,
		Apply: func(t *T, s S) {
			v := g(t.Tape)
			if t.record != nil {
				t.record(render(v))
			}
			apply(t, s, v)
		},
	}
}

// OpOver returns an op that applies to one element of candidates,
// chosen by the tape, and is enabled only while that set is non-empty.
//
// It replaces the pairing of a When that tests a filtered set against a
// Apply that rebuilds it, which is otherwise written out at every op
// that acts on "one of the currently eligible" things.
func OpOver[S, E any](name string, candidates func(s S) []E, apply func(t *T, s S, e E)) Op[S] {
	return Op[S]{
		Name:  name,
		When:  func(s S) bool { return len(candidates(s)) > 0 },
		Apply: func(t *T, s S) { apply(t, s, Pick(t.Tape, candidates(s))) },
	}
}

// A Machine describes a stateful property test: a system under test, a
// set of operations, and an invariant. Each input decodes to a bounded
// operation sequence; the invariant is checked after every applied op.
type Machine[S any] struct {
	// Init returns a fresh system under test. It may draw from the
	// tape to vary the starting state; those draws precede the first
	// op's, and [Machine.Splits] accounts for them.
	Init func(t *T) S
	// Ops is the operation set. It must be non-empty, and its order
	// is part of the corpus encoding: reordering ops changes how
	// previously saved inputs decode.
	Ops []Op[S]
	// Check asserts the invariant, failing with t.Fatalf. It must not
	// draw from the tape: its draws would be indistinguishable from an
	// op's and would shift how the rest of the input decodes.
	Check func(t *T, s S)
	// MaxOps bounds the ops decoded per input; 0 means 64. It is only an
	// upper bound: a sequence also ends when the input runs out, which
	// for short inputs happens well before MaxOps.
	MaxOps int
	// Name, if set, is the fuzz target name (e.g. "FuzzStreamMachine")
	// whose seed corpus under testdata/fuzz/ [Machine.Run] replays
	// before its random cases and saves shrunk failing inputs to, so a
	// failure found by either mode becomes a seed input replayed by
	// both.
	Name string
	// Bubble runs each input's op sequence inside a testing/synctest
	// bubble: time is virtual, and the bubble's exit check reports any
	// goroutine the sequence left durably blocked, making every case a
	// goroutine-leak check. Ops must not depend on real time or on
	// goroutines started outside the bubble.
	//
	// Init runs inside the bubble, so a goroutine it starts must be
	// stopped inside the bubble too — by an op, or by a cleanup Init
	// registers with [T.Cleanup], which the bubble runs before its exit
	// check. A goroutine stopped on the outer test's cleanup is still
	// blocked at exit and fails every case.
	//
	// A leak is reported with the stacks of the goroutines left blocked,
	// and shrinks like any other failure. The goroutines themselves stay
	// blocked for the rest of the run, in a bubble nothing else can
	// reach.
	Bubble bool
	// Stall bounds how long a run may complete no input before it is
	// reported as wedged and the process aborted. Zero means 30
	// seconds; a negative value disables the check.
	//
	// It applies only under Bubble and only to [Machine.Run], and
	// covers the one stall a bubble cannot report itself: a goroutine
	// blocked on something outside the bubble is not durably blocked,
	// so the bubble neither quiesces nor trips the runtime's deadlock
	// check, and the run hangs until go test times out. A goroutine
	// blocked inside the bubble never reaches this, being caught the
	// moment it happens. [Machine.Fuzz] is supervised by the fuzzing
	// engine instead.
	Stall time.Duration
}

// validate checks that m's configuration is well-formed before executing
// inputs against it.
func (m Machine[S]) validate() error {
	if m.Init == nil {
		return errors.New("fuzztape: Machine.Init is nil")
	}
	if len(m.Ops) == 0 {
		return errors.New("fuzztape: Machine has no Ops")
	}
	for i, op := range m.Ops {
		if op.Apply == nil {
			if op.Name != "" {
				return fmt.Errorf("fuzztape: Op %q has nil Apply", op.Name)
			}
			return fmt.Errorf("fuzztape: Op[%d] has nil Apply", i)
		}
	}
	return nil
}

// Fuzz registers the machine as the fuzz function of f. Corpus files,
// -fuzztime budgets, minimization, and seeds via f.Add behave as for
// any other fuzz target.
//
// Stall does not apply here. The fuzzing engine runs inputs in worker
// processes and already supervises them: a worker that stops responding
// is killed and its input recorded as a crasher. A watchdog in this
// process would be watching the coordinator, which runs no inputs of
// its own and would look stalled the moment it started orchestrating.
func (m Machine[S]) Fuzz(f *testing.F) {
	if err := m.validate(); err != nil {
		f.Helper()
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		m.runTape(t, data, true, nil, nil, nil)
	})
}

// Run checks the machine inside an ordinary test: first every seed
// input saved under testdata/fuzz for Name, then iters pseudo-random
// inputs; iters <= 0 means 100. The seed is printed and can be pinned
// with -fuzztape.seed. On failure Run shrinks the failing input, logs
// the minimal op sequence, and (if Name is set) saves the input to
// testdata/fuzz/ for permanent replay.
//
// Replaying the corpus here is what keeps the two modes symmetric: a
// failure found by Run is saved as a seed, and that seed is then
// checked by go test with no -fuzz flag, not only by the fuzz target.
//
// Both paths are relative to the process working directory, which go
// test sets to the directory of the package under test, so they land
// in that package's testdata/fuzz/<Name>. A test that changes the
// working directory, or a Run reached from another package, reads and
// writes somewhere else.
func (m Machine[S]) Run(t *testing.T, iters int) {
	if err := m.validate(); err != nil {
		t.Helper()
		t.Fatal(err)
	}
	w := m.startWatchdog()
	defer w.stop()
	if !m.replaySeeds(t, w) {
		return
	}
	if iters <= 0 {
		iters = 100
	}
	seed := *seedFlag
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	t.Logf("fuzztape: seed %d (rerun with -fuzztape.seed=%d)", seed, seed)
	rng := rand.New(rand.NewSource(seed))
	for i := range iters {
		data := make([]byte, 16+rng.Intn(496))
		rng.Read(data)
		if t.Run(fmt.Sprintf("case%04d", i), func(t *testing.T) { m.runTape(t, data, true, nil, nil, w) }) {
			continue
		}
		m.reportFailure(t, data, w)
		return
	}
}

// reportFailure shrinks a failing input, logs the result, and saves it
// as a seed for permanent replay.
func (m Machine[S]) reportFailure(t *testing.T, data []byte, w *watchdog) {
	data = m.shrink(t, data, w)
	t.Logf("fuzztape: shrunk failing input to %d bytes: %x", len(data), data)
	if m.Name == "" {
		return
	}
	if path, err := writeCorpusFile(m.Name, data); err != nil {
		t.Logf("fuzztape: save corpus file: %v", err)
	} else {
		t.Logf("fuzztape: saved failing input to %s", path)
	}
}

// replaySeeds runs every seed input saved for the machine's Name and
// reports whether all of them passed. It is a no-op when Name is unset
// or the corpus directory does not exist.
func (m Machine[S]) replaySeeds(t *testing.T, w *watchdog) bool {
	if m.Name == "" {
		return true
	}
	dir := filepath.Join("testdata", "fuzz", m.Name)
	entries, err := corpusEntries(dir)
	if err != nil {
		t.Errorf("fuzztape: %v", err)
		return false
	}
	ok := true
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := readCorpusFile(path)
		if err != nil {
			t.Errorf("fuzztape: %v", err)
			ok = false
			continue
		}
		if !t.Run("seed/"+e.Name(), func(t *testing.T) { m.runTape(t, data, true, nil, nil, w) }) {
			t.Logf("fuzztape: seed %s still fails; it is already minimal, so it is not reshrunk", path)
			ok = false
		}
	}
	return ok
}

// corpusEntries lists the seed files in dir. A missing directory is an
// empty corpus, which is not an error: a machine need not have one. Every
// other failure is reported, because it means the seeds that should have
// replayed did not — a regular file where the directory belongs, say, which
// go test itself refuses with "not a directory". Silently reporting success
// there loses the regression the corpus exists to keep.
func corpusEntries(dir string) ([]os.DirEntry, error) {
	return fuzzfile.ReadDir(dir)
}

// runTape runs one input, inside a synctest bubble if Bubble is set,
// and logs the op sequence if the input fails. The bubble goes here
// rather than around Run or Fuzz because both run each case as a
// subtest, and t.Run panics inside a bubble.
//
// A bubble reports a goroutine the sequence left blocked by panicking
// out of synctest.Test, after the sequence itself has returned. Turning
// that back into an ordinary failure is what keeps the rest of the
// package working on it: without the recover the test binary dies on
// the spot, with no shrinking, no op sequence, and no corpus file. A
// panic from an op never gets this far — runOps converts those, for
// bubbled and unbubbled machines alike, so that whether a panic shrinks
// does not depend on Bubble.
// Recovering is safe because the blocked goroutines stay in their
// abandoned bubble, where they can no longer affect later inputs. The
// stacks are worth printing only for the input Run failed on, not for
// the shrink attempts that follow, which by then are reporting the
// leaks of every attempt before them.
func (m Machine[S]) runTape(t *testing.T, data []byte, stacks bool, splits *[]int, out *Result, w *watchdog) {
	// Registered first, so it runs last: the input has completed, for
	// the watchdog's purposes, once everything else here has run.
	defer w.tick()
	var applied []string
	defer func() {
		if m.Bubble {
			if r := recover(); r != nil {
				if stacks {
					t.Errorf("fuzztape: %v\n\n%s", r, bubbleStacks())
				} else {
					t.Errorf("fuzztape: %v", r)
				}
			}
		}
		if t.Failed() {
			t.Logf("fuzztape: op sequence (%d ops):\n\t%s", len(applied), strings.Join(applied, "\n\t"))
		}
	}()
	if !m.Bubble {
		m.runOps(t, data, &applied, splits, out)
		return
	}
	synctest.Test(t, func(t *testing.T) { m.runOps(t, data, &applied, splits, out) })
}

// Replay runs one input against a fresh system, as a subtest of t, and
// reports whether it passed. It is what a saved corpus file needs to be
// re-run by hand, and what a generated reproduction case calls.
func (m Machine[S]) Replay(t *testing.T, data []byte) bool {
	return t.Run("replay", func(t *testing.T) { m.runTape(t, data, true, nil, nil, nil) })
}

// Trace replays data against a fresh system and returns the names of
// the ops it applied, in order, with rejected ops marked. It is the
// decoded meaning of an input: the same thing a failing case logs,
// available without failing.
//
// The replay applies the sequence for real, as [Machine.Splits] does,
// and reports on a subtest of t if the input fails the machine. An op
// that panics is reported and marked in the trace, not swallowed: an
// input that crashes the machine is the most broken thing a corpus can
// hold, and a trace that hid it would report it as decoding to nothing.
func (m Machine[S]) Trace(t *testing.T, data []byte) []string {
	var applied []string
	t.Run("trace", func(t *testing.T) {
		// Under Bubble a goroutine the sequence left blocked panics out
		// of synctest.Test after runOps has returned; recovering it here
		// is what runTape does for the same reason. Op panics do not
		// reach this: runOps reports them itself.
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("fuzztape: %v", r)
			}
		}()
		m.runOpsMaybeBubbled(t, data, &applied)
	})
	return applied
}

// runOpsMaybeBubbled runs one input for its op names alone, without the
// failure reporting runTape adds.
func (m Machine[S]) runOpsMaybeBubbled(t *testing.T, data []byte, applied *[]string) {
	if !m.Bubble {
		m.runOps(t, data, applied, nil, nil)
		return
	}
	synctest.Test(t, func(t *testing.T) { m.runOps(t, data, applied, nil, nil) })
}

// Splits reports the tape offsets at which the ops decoded from data
// begin, by replaying data against a fresh system in a subtest of t, so
// that data[splits[i]:splits[i+1]] holds the bytes of op i. Cutting or
// splicing inputs at these offsets edits whole operations.
//
// The replay applies the sequence for real: Init runs, ops run, and if
// the input fails the machine the failure is reported on the subtest.
// Under Bubble each replay that fails its exit check leaves another
// set of blocked goroutines behind in an abandoned bubble — bounded
// and harmless, as for shrink attempts, but visible in goroutine
// dumps.
//
// A terminal offset, just past the last byte the sequence consumed, is
// appended only when the sequence ended on its own terms. If the input
// ran out inside the last operation, that operation decoded part of
// itself from zeros past the end and there is no trailing offset:
// appending bytes there would be read by that operation rather than
// starting a new one, so it is not a boundary a caller can graft onto.
func (m Machine[S]) Splits(t *testing.T, data []byte) []int {
	var splits []int
	t.Run("splits", func(t *testing.T) { m.runTape(t, data, false, &splits, nil, nil) })
	return splits
}

// bubbleStacks returns the stacks of the goroutines running in a
// synctest bubble, which for a bubble that just failed its exit check
// are the goroutines it left blocked.
func bubbleStacks() string {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	var keep []string
	for g := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(g, "synctest bubble") {
			keep = append(keep, g)
		}
	}
	return strings.Join(keep, "\n\n")
}

// runOps runs one input under a test. It appends the name of each op it
// applies to *applied, which outlives it so that the caller can report the
// sequence even when the failure surfaces after runOps has returned. If
// splits is non-nil it records each op's starting tape offset, plus one final
// offset past the last byte consumed.
//
// The execution itself is exec, shared with [Machine.Exec]; runOps only
// supplies the test to report through and renders the history it accumulates.
func (m Machine[S]) runOps(tb *testing.T, data []byte, applied *[]string, splits *[]int, out *Result) {
	var result Result
	e := &execution[S]{
		reporter: testReporter{tb: tb},
		result:   &result,
		stacks:   true,
		phase:    Init,
		op:       -1,
	}
	// Registered before exec runs, so the history and the result are
	// both delivered even when Fatalf ends the sequence with a Goexit.
	defer func() {
		*applied = e.applied()
		if out != nil {
			*out = result
		}
	}()
	m.exec(&T{Tape: New(data), report: e}, e, splits)
}

// shrink reduces a failing input while it keeps failing the same way:
// by truncation, then by deleting the bytes of whole middle ops, then
// by bisecting individual bytes toward zero. Because reads past the end
// of the input and zero bytes both decode to the simplest choice,
// byte-level edits shrink the decoded op sequence and its values.
// Attempts run as subtests of t (which is already failing).
//
// "The same way" is the part that makes the result trustworthy. A
// smaller input that fails for an unrelated reason is not a smaller
// case of the original bug, and reporting it as one sends the reader
// after the wrong defect while the original goes unfixed. Shrinking
// touches every byte, so it is well placed to wander into a second bug.
// Attempts are therefore accepted only when they reproduce the original
// failure's identity (see Failure.sameAs), which is more than Go's own
// minimizer requires of a crasher.
//
// If the original failure has no identity to preserve — the machine
// failed in a way that recorded none — shrinking falls back to
// accepting any failure, which is the previous behavior.
func (m Machine[S]) shrink(t *testing.T, data []byte, w *watchdog) []byte {
	attempts := 0
	// The identity to preserve, read from the input as it arrived.
	var want *Failure
	if m.Bubble {
		var r Result
		t.Run("shrink-origin", func(t *testing.T) { m.runTape(t, data, false, nil, &r, w) })
		want = r.Failure
	} else {
		want = m.exec1(data, nil).Failure
	}
	// The oracle is a Result, not a subtest result, for every machine
	// that can run outside a test: shrinking asks "does this still
	// fail" a few hundred times, and routing that through t.Run made
	// every attempt a named subtest in the output of an already-failing
	// test. A bubbled machine still needs one, because synctest.Test
	// takes a testing.T and a bubble that never runs cannot fail its
	// exit check.
	// keeps reports whether got is the failure shrinking set out to
	// preserve. With no identity to compare against, any failure will
	// do; that is the old behavior, and it is still better than none.
	keeps := func(got *Failure) bool {
		if got == nil {
			return false
		}
		if want == nil {
			return true
		}
		return want.sameAs(got)
	}
	attempt := func(d []byte, splits *[]int) bool {
		attempts++
		if m.Bubble {
			var r Result
			t.Run(fmt.Sprintf("shrink%03d", attempts), func(t *testing.T) { m.runTape(t, d, false, splits, &r, w) })
			return keeps(r.Failure)
		}
		// runTape ticks the watchdog for the paths that go through it;
		// this one has to do it itself, or a long shrink looks stalled.
		defer w.tick()
		return keeps(m.exec1(d, splits).Failure)
	}
	fails := func(d []byte) bool { return attempt(d, nil) }

	// Truncation: halve, then trim single bytes.
	for len(data) > 0 && attempts < 200 {
		cut := data[:len(data)/2]
		if !fails(cut) {
			break
		}
		data = cut
	}
	for len(data) > 0 && attempts < 200 && fails(data[:len(data)-1]) {
		data = data[:len(data)-1]
	}

	// Chunk deletion: drop whole middle ops, last to first, so a
	// failure needing only its first and final ops shrinks past what
	// truncation alone can reach. Deleting a chunk changes how the rest
	// decodes, so the op boundaries are recomputed after each success.
	// The boundary replay itself runs as a shrink attempt. This phase
	// gets its own budget rather than the tail of truncation's: it is
	// the one that reaches failures truncation cannot.
	for stop := attempts + 80; attempts < stop; {
		var splits []int
		attempt(data, &splits)
		deleted := false
		for i := len(splits) - 2; i >= 0 && attempts < stop; i-- {
			cut := slices.Concat(data[:splits[i]], data[splits[i+1]:])
			if len(cut) < len(data) && fails(cut) {
				data = cut
				deleted = true
				break
			}
		}
		if !deleted {
			break
		}
	}

	// Value bisection: walk each byte toward zero while the input
	// keeps failing, trying zero first and then repeated halving down
	// to 1 (zero itself was just proven to pass, so halving stops
	// above it).
	stop := attempts + 100
	for i := 0; i < len(data) && attempts < stop; i++ {
		try := func(v byte) bool {
			edited := slices.Clone(data)
			edited[i] = v
			if fails(edited) {
				data = edited
				return true
			}
			return false
		}
		if data[i] == 0 || try(0) {
			continue
		}
		for data[i] > 1 && attempts < stop && try(data[i]/2) {
		}
	}
	return data
}

// writeCorpusFile saves data as a seed corpus file for the named fuzz
// target, in the format go test replays from testdata/fuzz.
func writeCorpusFile(target string, data []byte) (string, error) {
	return fuzzfile.Write(filepath.Join("testdata", "fuzz", target), data)
}

// readCorpusFile reads a seed corpus file holding the single []byte
// value a Machine target takes.
func readCorpusFile(path string) ([]byte, error) {
	return fuzzfile.Read(path)
}
