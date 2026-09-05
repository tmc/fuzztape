package faults_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/faults"
)

var errInjected = errors.New("injected")

type buffer struct{ bytes.Buffer }

func newRW() *faults.ReadWriter {
	return &faults.ReadWriter{RW: new(buffer)}
}

func TestReadWriterPassthrough(t *testing.T) {
	f := newRW()
	if _, err := f.Write([]byte("hi")); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err != nil || string(got) != "hi" {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
}

func TestFailReadOnce(t *testing.T) {
	f := newRW()
	f.RW.(*buffer).WriteString("data")
	f.FailRead(errInjected)
	if _, err := f.Read(make([]byte, 4)); !errors.Is(err, errInjected) {
		t.Fatalf("first Read err = %v, want injected", err)
	}
	n, err := f.Read(make([]byte, 4))
	if err != nil || n != 4 {
		t.Fatalf("second Read = %d, %v; fault not disarmed", n, err)
	}
}

func TestFailWriteOnce(t *testing.T) {
	f := newRW()
	f.FailWrite(errInjected)
	if _, err := f.Write([]byte("x")); !errors.Is(err, errInjected) {
		t.Fatalf("first Write err = %v, want injected", err)
	}
	if _, err := f.Write([]byte("y")); err != nil {
		t.Fatalf("second Write err = %v; fault not disarmed", err)
	}
	if got := f.RW.(*buffer).String(); got != "y" {
		t.Fatalf("buffer = %q, want %q", got, "y")
	}
}

func TestDrop(t *testing.T) {
	f := newRW()
	f.Drop(2)
	for _, s := range []string{"a", "b", "c"} {
		if n, err := f.Write([]byte(s)); n != 1 || err != nil {
			t.Fatalf("Write(%q) = %d, %v", s, n, err)
		}
	}
	if got := f.RW.(*buffer).String(); got != "c" {
		t.Fatalf("buffer = %q, want %q (first two writes dropped)", got, "c")
	}
}

type sut struct{ rw *faults.ReadWriter }

func TestOps(t *testing.T) {
	s := &sut{rw: newRW()}
	get := func(s *sut) *faults.ReadWriter { return s.rw }

	// A zero tape picks the first error.
	other := errors.New("other")
	op := faults.FailReadOp("fail-read", get, errInjected, other)
	if op.Name != "fail-read" {
		t.Errorf("Name = %q", op.Name)
	}
	op.Apply(fuzztape.NewT(t, nil), s)
	if _, err := s.rw.Read(nil); !errors.Is(err, errInjected) {
		t.Fatalf("armed err = %v, want first of errs on zero tape", err)
	}

	// Selector 2 forces IntN's n-1 boundary: the last error.
	op.Apply(fuzztape.NewT(t, []byte{2}), s)
	if _, err := s.rw.Read(nil); !errors.Is(err, other) {
		t.Fatalf("armed err = %v, want last of errs", err)
	}

	faults.FailWriteOp("fail-write", get, errInjected).Apply(fuzztape.NewT(t, nil), s)
	if _, err := s.rw.Write(nil); !errors.Is(err, errInjected) {
		t.Fatalf("write err = %v", err)
	}

	// DropOp arms 1 to max drops; selector 2 forces max.
	faults.DropOp("drop", get, 3).Apply(fuzztape.NewT(t, []byte{2}), s)
	for range 3 {
		s.rw.Write([]byte("z"))
	}
	s.rw.Write([]byte("kept"))
	if got := s.rw.RW.(*buffer).String(); got != "kept" {
		t.Fatalf("buffer = %q, want %q", got, "kept")
	}
}

func TestFailReadOpNoErrsPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("FailReadOp with no errors did not panic")
		}
	}()
	faults.FailReadOp("bad", func(s *sut) *faults.ReadWriter { return nil })
}

// TestSleepOpBubble runs SleepOp inside a Bubble machine: hour-scale
// sleeps must cost no real time and leave nothing blocked.
func TestSleepOpBubble(t *testing.T) {
	m := fuzztape.Machine[*sut]{
		Bubble: true,
		Init:   func(t *fuzztape.T) *sut { return &sut{rw: newRW()} },
		Ops: []fuzztape.Op[*sut]{
			faults.SleepOp[*sut]("sleep", 3600e9),
		},
	}
	m.Run(t, 20)
}

// shortSink records everything actually written to it, so a test can ask
// what reached the medium rather than what the caller believed.
type shortSink struct{ b []byte }

func (s *shortSink) Read([]byte) (int, error) { return 0, io.EOF }
func (s *shortSink) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}

func TestShortWrite(t *testing.T) {
	errFull := errors.New("no space left on device")
	for _, tc := range []struct {
		name    string
		arm     int
		armErr  error
		want    string
		wantN   int
		wantErr error
	}{
		{"prefix", 3, errFull, "abc", 3, errFull},
		{"nothing", 0, errFull, "", 0, errFull},
		{"negative is zero", -5, errFull, "", 0, errFull},
		{"past the end is a whole write", 99, errFull, "abcde", 5, errFull},
		{"nil error is a short write error", 3, nil, "abc", 3, io.ErrShortWrite},
		{"nil error on a whole write is none", 99, nil, "abcde", 5, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &shortSink{}
			f := &faults.ReadWriter{RW: sink}
			f.ShortWrite(tc.arm, tc.armErr)
			n, err := f.Write([]byte("abcde"))
			if n != tc.wantN || !errors.Is(err, tc.wantErr) {
				t.Errorf("Write = %d, %v; want %d, %v", n, err, tc.wantN, tc.wantErr)
			}
			if string(sink.b) != tc.want {
				t.Errorf("reached the medium %q, want %q", sink.b, tc.want)
			}
			// One-shot: the next write is whole and clean.
			if n, err := f.Write([]byte("xy")); n != 2 || err != nil {
				t.Errorf("second Write = %d, %v; want 2, nil", n, err)
			}
		})
	}
}

// TestShortWriteUnderlyingError checks that an error from the wrapped
// endpoint wins over the armed one: it is what a real caller would see.
func TestShortWriteUnderlyingError(t *testing.T) {
	errReal := errors.New("real")
	f := &faults.ReadWriter{RW: errWriter{errReal}}
	f.ShortWrite(3, errors.New("armed"))
	if _, err := f.Write([]byte("abcde")); !errors.Is(err, errReal) {
		t.Errorf("Write err = %v, want the underlying %v", err, errReal)
	}
}

type errWriter struct{ err error }

func (w errWriter) Read([]byte) (int, error)    { return 0, io.EOF }
func (w errWriter) Write(p []byte) (int, error) { return 0, w.err }

// TestShortWriteFindsTornRecord is the point of the arm. The log appends
// length-prefixed records and trusts Write to be all-or-nothing, so a
// short write leaves a truncated record that reads back as garbage.
//
// The control matters as much as the finding: the same machine armed
// only with the pre-existing faults never reports it. FailWrite writes
// no bytes, and Drop writes none while reporting that it wrote them all,
// so neither can leave a partial record on the medium. That is the gap
// ShortWrite closes.
func TestShortWriteFindsTornRecord(t *testing.T) {
	type state struct {
		f   *faults.ReadWriter
		log *shortSink
	}
	errShort := errors.New("short write")
	get := func(s *state) *faults.ReadWriter { return s.f }
	appendOp := fuzztape.Op[*state]{Name: "append", Apply: func(ft *fuzztape.T, s *state) {
		s.f.Write([]byte{3, 'a', 'b', 'c'}) // length prefix, payload; return values ignored
	}}
	machine := func(fault fuzztape.Op[*state]) fuzztape.Machine[*state] {
		return fuzztape.Machine[*state]{
			MaxOps: 6,
			Init: func(ft *fuzztape.T) *state {
				log := &shortSink{}
				return &state{f: &faults.ReadWriter{RW: log}, log: log}
			},
			Ops: []fuzztape.Op[*state]{appendOp, fault},
			Check: func(ft *fuzztape.T, s *state) {
				for b := s.log.b; len(b) > 0; {
					n := int(b[0])
					if len(b) < 1+n {
						ft.Errorf("torn record: %d payload bytes present, want %d", len(b)-1, n)
						return
					}
					b = b[1+n:]
				}
			},
		}
	}

	// found reports whether m fails on any tape in a fixed sweep. The
	// tapes come from a seeded PRNG rather than the fuzzing engine, so
	// this test cannot flake in either direction: the same 2000 tapes
	// are tried every run.
	found := func(m fuzztape.Machine[*state]) bool {
		rng := rand.New(rand.NewPCG(1, 2))
		tape := make([]byte, 32)
		for range 2000 {
			for i := range tape {
				tape[i] = byte(rng.UintN(256))
			}
			if m.Exec(tape).Failure != nil {
				return true
			}
		}
		return false
	}

	short := faults.ShortWriteOp("short", get, 4, errShort)
	if !found(machine(short)) {
		t.Error("ShortWriteOp did not produce a torn record")
	}
	for _, tc := range []struct {
		name  string
		fault fuzztape.Op[*state]
	}{
		{"FailWrite", faults.FailWriteOp("fail", get, errShort)},
		{"Drop", faults.DropOp("drop", get, 3)},
	} {
		if found(machine(tc.fault)) {
			t.Errorf("%s reached a torn record; the control is no longer a control", tc.name)
		}
	}
}
