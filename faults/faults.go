// Package faults provides tape-driven fault injection for fuzztape
// machines.
//
// A [ReadWriter] wraps the I/O endpoint a system under test reads and
// writes through; the op constructors return [fuzztape.Op] values that
// arm it with errors and drops drawn from the tape. Because faults come
// off the same tape as regular ops, a shrunk failing input answers
// which single fault, at which point in which sequence, breaks the
// invariant.
package faults

import (
	"io"
	"sync"
	"time"

	"github.com/tmc/fuzztape"
)

// A ReadWriter wraps an io.ReadWriter with armed, one-shot faults.
// The zero value is not usable; set RW.
type ReadWriter struct {
	// RW is the wrapped endpoint.
	RW io.ReadWriter

	mu       sync.Mutex
	readErr  error
	writeErr error
	drop     int
	short    bool
	shortN   int
	shortErr error
}

// Read returns the armed read error, if any, and disarms it;
// otherwise it reads from RW.
func (f *ReadWriter) Read(p []byte) (int, error) {
	f.mu.Lock()
	err := f.readErr
	f.readErr = nil
	f.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return f.RW.Read(p)
}

// Write applies the armed faults, at most one per call, and disarms the
// one it applies. They are checked in this order: a drop discards the
// write and reports it as complete; an armed write error returns it with
// no bytes written; an armed short write passes a prefix to RW. With
// none armed, Write writes to RW.
func (f *ReadWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.drop > 0 {
		f.drop--
		f.mu.Unlock()
		return len(p), nil
	}
	if err := f.writeErr; err != nil {
		f.writeErr = nil
		f.mu.Unlock()
		return 0, err
	}
	if f.short {
		n, err := f.shortN, f.shortErr
		f.short, f.shortN, f.shortErr = false, 0, nil
		f.mu.Unlock()
		return f.shortWrite(p, n, err)
	}
	f.mu.Unlock()
	return f.RW.Write(p)
}

// shortWrite passes the first n bytes of p to RW and reports err. The
// count is the one RW reported, not n: a fault that lied about how much
// reached the medium would be testing the harness, not the system. An
// error from RW itself wins over err, because it is what a real caller
// would see. A nil err on a short count becomes [io.ErrShortWrite], as
// the io.Writer contract requires.
func (f *ReadWriter) shortWrite(p []byte, n int, err error) (int, error) {
	n = min(n, len(p))
	w, werr := f.RW.Write(p[:n])
	if werr != nil {
		return w, werr
	}
	if err == nil && w < len(p) {
		err = io.ErrShortWrite
	}
	return w, err
}

// FailRead arms err to be returned by the next Read.
func (f *ReadWriter) FailRead(err error) {
	f.mu.Lock()
	f.readErr = err
	f.mu.Unlock()
}

// FailWrite arms err to be returned by the next Write.
func (f *ReadWriter) FailWrite(err error) {
	f.mu.Lock()
	f.writeErr = err
	f.mu.Unlock()
}

// ShortWrite arms the next Write to pass only the first n bytes of its
// argument to RW and then return err, the partial write a filesystem
// reports when it runs out of space or is interrupted mid-record.
//
// This is the shape [ReadWriter.FailWrite] cannot express. FailWrite
// returns no bytes written, so a caller that retries repeats the whole
// record; a short write leaves a prefix on the medium, so a caller that
// retries the whole record duplicates that prefix, and one that retries
// only the remainder depends on having counted correctly. Records that
// survive a crash only as a prefix are the durability bug class write-
// ahead logs are made of.
//
// A negative n is treated as zero, and an n past the write's length is
// the whole write, which makes err a trailing error on a complete
// write — also a real outcome. A nil err is [io.ErrShortWrite] when the
// write is short, since a Write that returns fewer bytes than it was
// given must return an error, and none when it is whole.
func (f *ReadWriter) ShortWrite(n int, err error) {
	f.mu.Lock()
	f.short, f.shortN, f.shortErr = true, max(n, 0), err
	f.mu.Unlock()
}

// Drop arms the next n Writes to be silently discarded.
//
// A dropped write reported as success is the endpoint lying about
// durability. Against an oracle that requires written data to survive,
// that is a guaranteed failure rather than a discovered one, so a drop
// belongs in a model whose contract permits acknowledged data to
// vanish. It is a real fault for a lossy transport and a vacuous one
// for a log that promises otherwise.
func (f *ReadWriter) Drop(n int) {
	f.mu.Lock()
	f.drop = n
	f.mu.Unlock()
}

// FailReadOp returns an op that arms the ReadWriter selected by get
// with one of errs on its next Read. It panics if errs is empty.
func FailReadOp[S any](name string, get func(S) *ReadWriter, errs ...error) fuzztape.Op[S] {
	mustErrs(errs)
	return fuzztape.Op[S]{
		Name: name,
		Apply: func(t *fuzztape.T, s S) {
			get(s).FailRead(fuzztape.Pick(t.Tape, errs))
		},
	}
}

// FailWriteOp returns an op that arms the ReadWriter selected by get
// with one of errs on its next Write. It panics if errs is empty.
func FailWriteOp[S any](name string, get func(S) *ReadWriter, errs ...error) fuzztape.Op[S] {
	mustErrs(errs)
	return fuzztape.Op[S]{
		Name: name,
		Apply: func(t *fuzztape.T, s S) {
			get(s).FailWrite(fuzztape.Pick(t.Tape, errs))
		},
	}
}

// DropOp returns an op that arms the ReadWriter selected by get to
// silently discard its next 1 to max Writes.
func DropOp[S any](name string, get func(S) *ReadWriter, max int) fuzztape.Op[S] {
	return fuzztape.Op[S]{
		Name: name,
		Apply: func(t *fuzztape.T, s S) {
			get(s).Drop(1 + t.IntN(max))
		},
	}
}

// ShortWriteOp returns an op that arms the ReadWriter selected by get to
// pass a prefix of 0 to max bytes of its next Write to the wrapped
// endpoint and then return one of errs. Both the prefix length and the
// error come off the tape, so a shrunk failure names the record that was
// torn and where. It panics if errs is empty or max is negative.
func ShortWriteOp[S any](name string, get func(S) *ReadWriter, max int, errs ...error) fuzztape.Op[S] {
	mustErrs(errs)
	if max < 0 {
		panic("faults: ShortWriteOp with negative max")
	}
	return fuzztape.Op[S]{
		Name: name,
		Apply: func(t *fuzztape.T, s S) {
			get(s).ShortWrite(t.IntN(max+1), fuzztape.Pick(t.Tape, errs))
		},
	}
}

// SleepOp returns an op that sleeps for up to max, exercising timeout
// paths. It is intended for machines with Bubble set, where the sleep
// advances virtual time and costs nothing.
func SleepOp[S any](name string, max time.Duration) fuzztape.Op[S] {
	nap := fuzztape.Duration(0, max)
	return fuzztape.Op[S]{
		Name: name,
		Apply: func(t *fuzztape.T, s S) {
			time.Sleep(nap(t.Tape))
		},
	}
}

func mustErrs(errs []error) {
	if len(errs) == 0 {
		panic("faults: op constructed with no errors")
	}
}
