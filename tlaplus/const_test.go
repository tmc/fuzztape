package tlaplus

import (
	"strings"
	"testing"
)

// constModule declares the constants constValues sets.
const constModule = "---- MODULE Diff ----\nEXTENDS Integers, Sequences, FiniteSets, TLC\nCONSTANTS a, b, N, S, Names, Nums\n"

var constValues = map[string]any{
	"a":     ModelValue("a"),
	"b":     ModelValue("b"),
	"N":     3,
	"S":     map[ModelValue]struct{}{"a": {}, "b": {}},
	"Names": map[string]struct{}{"x": {}, "y": {}},
	"Nums":  []int{1, 2},
}

// evalConst evaluates src in constModule with constValues bound.
func evalConst(src string) (out string, err error) {
	s, err := Parse("C.tla", []byte(constModule+"X == "+src+"\n===="))
	if err != nil {
		return "", err
	}
	if s, err = s.With(constValues); err != nil {
		return "", err
	}
	defer catch(&err)
	depth := 0
	c := &ctx{fail: &failure{}, depth: &depth}
	d := s.defs["X"]
	return format(s.force(d.p, s.eval(d.body, c))), nil
}

// constTests are also checked against TLC, by TestTLCConstants. A model
// value equals only itself; comparing one with any other value is
// false, not an error, as it is in TLC.
var constTests = []struct{ in, want string }{
	{"N + 1", "4"},
	{"S", "{a, b}"},
	{"a", "a"},
	{"a = a", "TRUE"},
	{"a = b", "FALSE"},
	{"a = 1", "FALSE"},
	{`a = "a"`, "FALSE"},
	{"a # b", "TRUE"},
	{"a \\in S", "TRUE"},
	{"a \\in {1, 2}", "FALSE"},
	{"a \\in Nat", "FALSE"},
	{"a \\in Int", "FALSE"},
	{"a \\in STRING", "FALSE"},
	{"a \\in 1..2000000", "FALSE"},
	{"{a, 1}", "{1, a}"},
	{"S \\cup {N}", "{3, a, b}"},
	{"Cardinality(S)", "2"},
	{"CHOOSE v \\in S : TRUE", "a"},
	{"<<a, b>> = <<a, 1>>", "FALSE"},
	{"[v \\in S |-> N]", "(a :> 3 @@ b :> 3)"},
	{`{n \in Names : n # "x"}`, `{"y"}`},
	{"Len(Nums) + Nums[2]", "4"},
}

func TestConstants(t *testing.T) {
	for _, tt := range constTests {
		got, err := evalConst(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("%s = %s, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

func TestConstantErrors(t *testing.T) {
	tests := []struct{ in, want string }{
		{"a < b", "expected an integer, found model value a"},
		{`1 = "a"`, "cannot compare integer 1 with string"},
		{"N' = N", ""},
	}
	for _, tt := range tests {
		got, err := evalConst(tt.in)
		if tt.want == "" {
			// A constant is the same in every state, so priming it is
			// allowed and changes nothing.
			if err != nil || got != "TRUE" {
				t.Errorf("%s = %s, %v; want TRUE", tt.in, got, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s = %s, %v; want error containing %q", tt.in, got, err, tt.want)
		}
	}
}

func TestWith(t *testing.T) {
	const src = "---- MODULE M ----\nEXTENDS Naturals\nCONSTANTS N, Nodes\nASSUME N > 0\n===="
	nodes := map[ModelValue]struct{}{"n1": {}, "n2": {}}
	tests := []struct {
		name   string
		consts map[string]any
		want   string // substring of the error; "" for none
	}{
		{"ok", map[string]any{"N": 2, "Nodes": nodes}, ""},
		{"missing", map[string]any{"N": 2}, "M.tla: constant Nodes has no value"},
		{"extra", map[string]any{"N": 2, "Nodes": nodes, "K": 1}, "M.tla: no constant K"},
		{"assume", map[string]any{"N": 0, "Nodes": nodes}, "M.tla:4:8: assumption is false: N > 0"},
		{"nil", map[string]any{"N": nil, "Nodes": nodes}, "constant N: cannot convert nil"},
		{"space", map[string]any{"N": 1, "Nodes": ModelValue("n 1")}, `model value "n 1" is not an identifier`},
		{"digits", map[string]any{"N": 1, "Nodes": ModelValue("12")}, `model value "12" is not an identifier`},
		{"keyword", map[string]any{"N": 1, "Nodes": ModelValue("TRUE")}, `model value "TRUE" is not an identifier`},
		{"empty", map[string]any{"N": 1, "Nodes": ModelValue("")}, `model value "" is not an identifier`},
	}
	spec, err := Parse("M.tla", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := spec.With(tt.consts)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("With: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("With: %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestStartWithoutConstants(t *testing.T) {
	src := "---- MODULE M ----\nCONSTANT N\nVARIABLE x\nInit == x = N\n===="
	spec, err := Parse("M.tla", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Start("Init", nil); err == nil || !strings.Contains(err.Error(), "constant N has no value") {
		t.Fatalf("Start: %v, want constant N has no value", err)
	}
	bound, err := spec.With(map[string]any{"N": 7})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Start("Init", State{"x": 7}); err != nil {
		t.Fatalf("Start after With: %v", err)
	}
	// With does not change the Spec it was called on.
	if _, err := spec.Start("Init", nil); err == nil {
		t.Fatal("Start on the unbound Spec: no error")
	}
}

// lockSpec is a lock over a set of model-value clients: a trace
// passes the clients, and observes the holder, as ModelValues.
const lockSpec = `
---- MODULE Lock ----
CONSTANTS Clients, Free
ASSUME Free \notin Clients
VARIABLE holder
Init == holder = Free
Acquire(c) == c \in Clients /\ holder = Free /\ holder' = c
Release(c) == holder = c /\ holder' = Free
====
`

func TestLockTrace(t *testing.T) {
	spec, err := Parse("Lock.tla", []byte(lockSpec))
	if err != nil {
		t.Fatal(err)
	}
	spec, err = spec.With(map[string]any{
		"Clients": map[ModelValue]struct{}{"c1": {}, "c2": {}},
		"Free":    ModelValue("Free"),
	})
	if err != nil {
		t.Fatal(err)
	}
	c1, c2, free := ModelValue("c1"), ModelValue("c2"), ModelValue("Free")
	type step struct {
		action string
		client ModelValue
		holder ModelValue
	}
	tests := []struct {
		name  string
		steps []step
		want  string
	}{
		{"handoff", []step{{"Acquire", c1, c1}, {"Release", c1, free}, {"Acquire", c2, c2}}, ""},
		{"double acquire", []step{{"Acquire", c1, c1}, {"Acquire", c2, c2}}, `step 2: Acquire(c2): Lock.tla:7:32: holder = Free is false: c1 # Free`},
		{"release by other", []step{{"Acquire", c1, c1}, {"Release", c2, free}}, `step 2: Release(c2)`},
		{"unknown client", []step{{"Acquire", "c3", "c3"}}, `step 1: Acquire(c3): Lock.tla:7:15: c \in Clients is false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := spec.Start("Init", State{"holder": free})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tt.steps {
				if err = tr.Step(State{"holder": s.holder}, s.action, s.client); err != nil {
					break
				}
			}
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
