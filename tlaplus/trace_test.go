package tlaplus

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func parseFile(t *testing.T, name string) *Spec {
	t.Helper()
	src, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(name, src)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type kv = map[string]string

// A step is one action of a WAL trace and the contents observed after it.
type step struct {
	action string
	args   []any
	kv     kv
}

func TestWAL(t *testing.T) {
	spec := parseFile(t, "testdata/WAL.tla")
	tests := []struct {
		name  string
		steps []step
		want  string // substring of the error; "" if the trace is allowed
	}{
		{"puts", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Put", []any{"a", "2"}, kv{"a": "2"}},
			{"Delete", []any{"a"}, kv{}},
		}, ""},
		{"get", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Get", []any{"a", "1", true}, kv{"a": "1"}},
			{"Get", []any{"b", "", false}, kv{"a": "1"}},
		}, ""},
		{"get wrong value", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Get", []any{"a", "2", true}, kv{"a": "1"}},
		}, `step 2: Get("a", "2", TRUE): testdata/WAL.tla:38:8: ok => v = kv[k] is false`},
		{"put lost", []step{
			{"Put", []any{"a", "1"}, kv{}},
		}, `kv' = Apply(kv, r) is false: <<>> # [a |-> "1"]`},
		{"crash keeps synced", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Sync", nil, kv{"a": "1"}},
			{"Crash", nil, kv{"a": "1"}},
		}, ""},
		{"crash loses synced", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Sync", nil, kv{"a": "1"}},
			{"Crash", nil, kv{}},
		}, "step 3: Crash: testdata/WAL.tla:51:12: kv' = Replay(synced, SubSeq(pending, 1, n)) is false: <<>> # [a |-> \"1\"]"},
		{"crash may lose unsynced", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Sync", nil, kv{"a": "1"}},
			{"Put", []any{"b", "2"}, kv{"a": "1", "b": "2"}},
			{"Put", []any{"c", "3"}, kv{"a": "1", "b": "2", "c": "3"}},
			{"Crash", nil, kv{"a": "1", "b": "2"}},
			{"Put", []any{"d", "4"}, kv{"a": "1", "b": "2", "d": "4"}},
			{"Crash", nil, kv{"a": "1", "b": "2"}},
		}, ""},
		{"crash keeps a record but not the one before it", []step{
			{"Put", []any{"b", "2"}, kv{"b": "2"}},
			{"Put", []any{"c", "3"}, kv{"b": "2", "c": "3"}},
			{"Crash", nil, kv{"c": "3"}},
		}, "step 3: Crash"},
		{"delete then crash", []step{
			{"Put", []any{"a", "1"}, kv{"a": "1"}},
			{"Sync", nil, kv{"a": "1"}},
			{"Delete", []any{"a"}, kv{}},
			{"Crash", nil, kv{"a": "1"}},
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := spec.Start("Init", State{"kv": kv{}})
			if err != nil {
				t.Fatal(err)
			}
			for _, st := range tt.steps {
				err = tr.Step(State{"kv": st.kv}, st.action, st.args...)
				if err != nil {
					break
				}
			}
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.want != "" && err == nil:
				t.Fatalf("trace allowed, want error containing %q", tt.want)
			case tt.want != "" && !strings.Contains(err.Error(), tt.want):
				t.Fatalf("error %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

// A spec whose unobserved variable is chosen nondeterministically: the
// trace must carry every choice until an observation rules some out.
const coinSpec = `
---- MODULE Coin ----
EXTENDS Naturals
VARIABLES coin, flips
Init == coin \in {"heads", "tails"} /\ flips = 0
Flip == coin' \in {"heads", "tails"} /\ flips' = flips + 1
Peek(c) == c = coin /\ UNCHANGED <<coin, flips>>
Next == Flip \/ \E c \in {"heads", "tails"} : Peek(c)
====
`

func TestFrontier(t *testing.T) {
	spec, err := Parse("Coin.tla", []byte(coinSpec))
	if err != nil {
		t.Fatal(err)
	}
	tr, err := spec.Start("", State{"flips": 0})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(tr.frontier); n != 2 {
		t.Fatalf("after Init, %d states, want 2", n)
	}
	if err := tr.Step(State{}, "Peek", "tails"); err != nil {
		t.Fatal(err)
	}
	if n := len(tr.frontier); n != 1 {
		t.Fatalf("after Peek, %d states, want 1", n)
	}
	err = tr.Step(State{}, "Peek", "heads")
	var se *StepError
	if !errors.As(err, &se) || se.Step != 2 || se.Action != `Peek("heads")` {
		t.Fatalf("Peek(heads) after Peek(tails): %v, want *StepError at step 2", err)
	}
	// The failed step left the trace as it was.
	if err := tr.Step(State{"flips": 1}, "Flip"); err != nil {
		t.Fatal(err)
	}
	if err := tr.Step(State{"flips": 2}, ""); err != nil {
		t.Fatal(err)
	}
	if err := tr.Step(State{"flips": 4}, ""); err == nil {
		t.Fatal("Next skipping a flip count: no error")
	}
}

func TestStepErrors(t *testing.T) {
	spec, err := Parse("Coin.tla", []byte(coinSpec))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		do   func() error
		want string
	}{
		{"unknown variable", func() error {
			_, err := spec.Start("Init", State{"nope": 1})
			return err
		}, "Coin.tla: no variable nope"},
		{"unknown init", func() error {
			_, err := spec.Start("Begin", nil)
			return err
		}, "Coin.tla: no operator Begin"},
		{"bad value", func() error {
			_, err := spec.Start("Init", State{"flips": 1.5})
			return err
		}, "variable flips: cannot convert float64"},
		{"init rejected", func() error {
			_, err := spec.Start("Init", State{"coin": "edge"})
			return err
		}, `step 0: Init: Coin.tla:5:9: coin \in {"heads", "tails"}`},
		{"arity", func() error {
			tr, _ := spec.Start("Init", nil)
			return tr.Step(nil, "Peek")
		}, "Coin.tla: Peek takes 1 arguments, not 0"},
		{"type mismatch", func() error {
			tr, _ := spec.Start("Init", nil)
			return tr.Step(State{"flips": "one"}, "Flip")
		}, "cannot compare string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.do()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestUndetermined(t *testing.T) {
	src := "---- MODULE U ----\nVARIABLES x, y\nInit == x = 1\n===="
	spec, err := Parse("U.tla", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	_, err = spec.Start("Init", nil)
	if err == nil || !strings.Contains(err.Error(), "Init leaves y undetermined") {
		t.Fatalf("Start: %v, want y undetermined", err)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, src, want string
	}{
		{"constant", "---- MODULE M ----\nCONSTANT N\n====", "constants are not supported"},
		{"extends", "---- MODULE M ----\nEXTENDS Foo\n====", "EXTENDS Foo"},
		{"instance", "---- MODULE M ----\nINSTANCE Foo\n====", "M.tla:2:1: INSTANCE is not supported"},
		{"assume", "---- MODULE M ----\nEXTENDS Naturals\nASSUME 1 + 1 = 3\n====", "M.tla:3:8: assumption is false: 1 + 1 = 3"},
		{"duplicate", "---- MODULE M ----\nA == 1\nA == 2\n====", "M.tla:3:1: A defined twice"},
		{"no end", "---- MODULE M ----\nA == 1\n", "missing ===="},
		{"repeated field", "---- MODULE M ----\nA == [a |-> 1, a |-> 2]\nASSUME A = A\n====", "M.tla:2:16: field a repeated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("M.tla", []byte(tt.src))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse: %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestUnsupportedDefinitionIsLazy(t *testing.T) {
	src := `---- MODULE M ----
VARIABLE x
Init == x = 0
Next == x' = x
Spec == Init /\ [][Next]_x
Before(a, b) == a \prec b
Fair == WF_x(Next)
Big == \bigcirc x
====`
	spec, err := Parse("M.tla", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tr, err := spec.Start("Init", State{"x": 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Step(State{"x": 0}, "Next"); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Start("Spec", nil); err == nil {
		t.Fatal("Start(Spec): no error")
	}
}

func BenchmarkWALStep(b *testing.B) {
	src, err := os.ReadFile("testdata/WAL.tla")
	if err != nil {
		b.Fatal(err)
	}
	spec, err := Parse("WAL.tla", src)
	if err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		tr, err := spec.Start("Init", State{"kv": kv{}})
		if err != nil {
			b.Fatal(err)
		}
		live := kv{}
		for i := range 8 {
			k := string(rune('a' + i%4))
			live[k] = "v"
			if err := tr.Step(State{"kv": live}, "Put", k, "v"); err != nil {
				b.Fatal(err)
			}
		}
		if err := tr.Step(State{"kv": live}, "Crash"); err != nil {
			b.Fatal(err)
		}
	}
}

// Operator arguments are substituted, not evaluated first: in
// Keep(v) == UNCHANGED v, Keep(x) means UNCHANGED x.
const callSpec = `
---- MODULE Call ----
EXTENDS Integers
VARIABLES x, y
Init == x = 0 /\ y = 0
Keep(v) == UNCHANGED v
Stay(v) == v' = v
Set(v, n) == v' = n
Eq(a, b) == a = b
Grew(v) == v' > v
Do(A) == A
SetX == Set(x, 1) /\ Keep(y)
StayAll == Stay(x) /\ Stay(y)
Inc == Eq(x', x + 1) /\ Eq(y', y)
LetPrime == LET z == x IN z' = 5 /\ UNCHANGED y
Bump == x' = x + 1 /\ Grew(x) /\ Keep(y)
DoIt == Do(x' = 2) /\ Do(UNCHANGED y)
All == \A i \in {1, 2} : x' = 3 /\ y' = y
====
`

func TestCallByName(t *testing.T) {
	spec, err := Parse("Call.tla", []byte(callSpec))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		action string
		obs    State
		ok     bool
	}{
		{"SetX", State{"x": 1, "y": 0}, true},
		{"SetX", State{"x": 0, "y": 0}, false},
		{"SetX", State{"x": 1, "y": 1}, false},
		{"SetX", State{}, true},
		{"StayAll", State{"x": 0, "y": 0}, true},
		{"StayAll", State{"x": 1}, false},
		{"Inc", State{"x": 1, "y": 0}, true},
		{"Inc", State{"x": 2}, false},
		{"Inc", State{}, true},
		{"LetPrime", State{"x": 5, "y": 0}, true},
		{"LetPrime", State{"x": 0}, false},
		{"Bump", State{"x": 1, "y": 0}, true},
		{"DoIt", State{"x": 2, "y": 0}, true},
		{"DoIt", State{"y": 1}, false},
		{"All", State{"x": 3, "y": 0}, true},
		{"All", State{"x": 4}, false},
	}
	for _, tt := range tests {
		tr, err := spec.Start("", nil)
		if err != nil {
			t.Fatal(err)
		}
		err = tr.Step(tt.obs, tt.action)
		if (err == nil) != tt.ok {
			t.Errorf("%s to %v: error %v, want ok=%v", tt.action, tt.obs, err, tt.ok)
		}
	}
}

func TestFromGoErrors(t *testing.T) {
	type node struct{ Next *node }
	cycle := &node{}
	cycle.Next = cycle
	tests := []struct {
		name string
		x    any
		want string
	}{
		{"cycle", cycle, "nested more than 100 deep"},
		{"colliding keys", map[any]int{int(1): 1, int64(1): 2}, "keys that convert to the same value"},
		{"nil", nil, "cannot convert nil"},
	}
	for _, tt := range tests {
		_, err := fromGo(tt.x)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %v, want it to contain %q", tt.name, err, tt.want)
		}
	}
}
