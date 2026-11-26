package faults_test

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/tmc/fuzztape"
	"github.com/tmc/fuzztape/faults"
)

// ShortWriteOp arms a ReadWriter to pass a tape-chosen prefix to the
// underlying writer and then return an error, exercising torn-write paths.
func ExampleShortWriteOp() {
	type state struct {
		buf bytes.Buffer
		rw  *faults.ReadWriter
	}
	errFull := errors.New("disk full")
	m := fuzztape.Machine[*state]{
		Init: func(*fuzztape.T) *state {
			s := &state{}
			s.rw = &faults.ReadWriter{RW: &s.buf}
			return s
		},
		Ops: []fuzztape.Op[*state]{
			faults.ShortWriteOp("short", func(s *state) *faults.ReadWriter { return s.rw }, 4, errFull),
			{Name: "write", Apply: func(_ *fuzztape.T, s *state) {
				n, err := s.rw.Write([]byte("record"))
				fmt.Printf("wrote %d (%q), err=%v\n", n, s.buf.String(), err)
			}},
		},
		MaxOps: 2,
	}
	m.Exec([]byte{0, 3, 0, 1})
	// Output:
	// wrote 4 ("reco"), err=disk full
}
