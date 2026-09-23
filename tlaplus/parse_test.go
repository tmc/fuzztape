package tlaplus

import (
	"os"
	"testing"
)

// FuzzParse checks that Parse, and evaluating what it accepts, report
// errors rather than panicking.
func FuzzParse(f *testing.F) {
	wal, err := os.ReadFile("testdata/WAL.tla")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(string(wal))
	f.Add(coinSpec)
	for _, tt := range evalTests {
		f.Add("---- MODULE T ----\nEXTENDS Integers, Sequences, FiniteSets, TLC\n" + tt.defs + "\nX == " + tt.in + "\n====")
	}
	f.Fuzz(func(t *testing.T, src string) {
		s, err := Parse("F.tla", []byte(src))
		if err != nil {
			return
		}
		for _, d := range s.defs {
			if len(d.params) == 0 && !d.fcn {
				evalDef(s, d)
			}
		}
	})
}

func evalDef(s *Spec, d *opDef) (err error) {
	defer catch(&err)
	depth := 0
	c := &ctx{cur: make([]value, len(s.vars)), nxt: make([]value, len(s.vars)), fail: &failure{}, depth: &depth}
	s.force(d.p, s.eval(d.body, c))
	return nil
}
