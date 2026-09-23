package tlaplus

import (
	"strings"
	"testing"
)

// evalString evaluates the TLA+ expression src in a module extending
// every supported standard module, and returns the result in TLA+
// syntax.
func evalString(defs, src string) (out string, err error) {
	mod := "---- MODULE T ----\nEXTENDS Integers, Sequences, FiniteSets, TLC\n" + defs + "\nX == " + src + "\n===="
	s, err := Parse("T.tla", []byte(mod))
	if err != nil {
		return "", err
	}
	defer catch(&err)
	depth := 0
	c := &ctx{fail: &failure{}, depth: &depth}
	d := s.defs["X"]
	return format(s.force(d.p, s.eval(d.body, c))), nil
}

var evalTests = []struct {
	name string
	defs string
	in   string
	want string
}{
	{"int", "", "1 + 2 * 3", "7"},
	{"minus assoc", "", "10 - 3 - 2", "5"},
	{"unary minus", "", "-3 + 5", "2"},
	{"power", "", "2^10", "1024"},
	{"div floor", "", `(-7) \div 2`, "-4"},
	{"unary minus binds looser than div", "", `-7 \div 2`, "-3"},
	{"mod", "", "-7 % 3", "2"},
	{"compare", "", "1 < 2 /\\ 2 <= 2 /\\ 3 > 2 /\\ 2 >= 3", "FALSE"},
	{"string", "", `"a\"b"`, `"a\"b"`},
	{"bool ops", "", "TRUE => FALSE", "FALSE"},
	{"implies short circuit", "", `FALSE => (1 = "a")`, "TRUE"},
	{"equiv", "", "FALSE <=> FALSE", "TRUE"},
	{"not", "", "~TRUE \\/ ~FALSE", "TRUE"},
	{"neq", "", "1 # 2 /\\ 1 /= 2", "TRUE"},
	{"interval", "", "1..3", "{1, 2, 3}"},
	{"empty interval", "", "3..1", "{}"},
	{"set dedup", "", "{3, 1, 2, 1}", "{1, 2, 3}"},
	{"union", "", `{1, 2} \cup {2, 3}`, "{1, 2, 3}"},
	{"union alias", "", `{1} \union {2}`, "{1, 2}"},
	{"intersect", "", `{1, 2} \cap {2, 3}`, "{2}"},
	{"intersect infinite", "", `{-1, 0, 5} \cap Nat`, "{0, 5}"},
	{"difference", "", `{1, 2, 3} \ {2}`, "{1, 3}"},
	{"subseteq", "", `{1} \subseteq {1, 2}`, "TRUE"},
	{"in Nat", "", `3 \in Nat /\ -1 \notin Nat /\ -1 \in Int`, "TRUE"},
	{"filter", "", `{x \in 1..6 : x % 2 = 0}`, "{2, 4, 6}"},
	{"map", "", `{x * x : x \in 1..3}`, "{1, 4, 9}"},
	{"map two", "", `{x + y : x \in 1..2, y \in {10, 20}}`, "{11, 12, 21, 22}"},
	{"subset", "", "SUBSET {1, 2}", "{{}, {1}, {2}, {1, 2}}"},
	{"union of sets", "", "UNION {{1}, {2, 3}}", "{1, 2, 3}"},
	{"cardinality", "", "Cardinality({1, 2, 2, 3})", "3"},
	{"forall", "", `\A x \in 1..3 : x > 0`, "TRUE"},
	{"exists", "", `\E x \in 1..3 : x > 2`, "TRUE"},
	{"exists empty", "", `\E x \in {} : TRUE`, "FALSE"},
	{"forall tuple", "", `\A <<a, b>> \in {<<1, 2>>, <<3, 4>>} : a < b`, "TRUE"},
	{"choose", "", `CHOOSE x \in 1..10 : x * x > 20`, "5"},
	{"if", "", "IF 1 > 2 THEN 1 ELSE 2", "2"},
	{"case", "", "CASE 1 > 2 -> 1 [] 2 > 1 -> 2", "2"},
	{"case other", "", "CASE 1 > 2 -> 1 [] OTHER -> 3", "3"},
	{"let", "", "LET a == 2 b == a + 1 IN a * b", "6"},
	{"let op", "", "LET Sq(n) == n * n IN Sq(4)", "16"},
	{"tuple", "", `<<1, "a", TRUE>>`, `<<1, "a", TRUE>>`},
	{"seq eq fcn", "", `<<1, 2>> = [i \in 1..2 |-> i]`, "TRUE"},
	{"record", "", `[b |-> 2, a |-> 1]`, "[a |-> 1, b |-> 2]"},
	{"record field", "", `[a |-> 1, b |-> 2].b`, "2"},
	{"fcn", "", `[x \in {2, 3} |-> x * 10]`, "(2 :> 20 @@ 3 :> 30)"},
	{"fcn on 1..n is a seq", "", `[x \in {1, 2} |-> x * 10]`, "<<10, 20>>"},
	{"fcn apply", "", `[x \in {1, 2} |-> x * 10][2]`, "20"},
	{"fcn two args", "", `[x \in 1..2, y \in 1..2 |-> x + y][2, 1]`, "3"},
	{"domain", "", `DOMAIN [a |-> 1, b |-> 2]`, `{"a", "b"}`},
	{"except", "", `[[a |-> 1, b |-> 2] EXCEPT !.a = 5]`, "[a |-> 5, b |-> 2]"},
	{"except at", "", `[<<1, 2, 3>> EXCEPT ![2] = @ * 10]`, "<<1, 20, 3>>"},
	{"except nested", "", `[[a |-> <<1, 2>>] EXCEPT !.a[1] = 9]`, "[a |-> <<9, 2>>]"},
	{"except two", "", `[<<1, 2>> EXCEPT ![1] = 5, ![2] = 6]`, "<<5, 6>>"},
	{"fcn set member", "", `[a |-> 1] \in [{"a"} -> Nat]`, "TRUE"},
	{"fcn set enumerate", "", `[{1, 2} -> {TRUE}]`, "{<<TRUE, TRUE>>}"},
	{"record set", "", `[a : {1, 2}, b : {"x"}]`, `{[a |-> 1, b |-> "x"], [a |-> 2, b |-> "x"]}`},
	{"record set member", "", `[a |-> 3] \in [a : Nat]`, "TRUE"},
	{"product", "", `{1, 2} \X {"a"}`, `{<<1, "a">>, <<2, "a">>}`},
	{"product member", "", `<<1, 2, 3>> \in Nat \X Nat \X Nat`, "TRUE"},
	{"len", "", "Len(<<1, 2, 3>>)", "3"},
	{"append", "", "Append(<<1>>, 2)", "<<1, 2>>"},
	{"concat", "", `<<1>> \o <<2, 3>>`, "<<1, 2, 3>>"},
	{"head tail", "", "<<Head(<<1, 2>>)>> \\o Tail(<<1, 2, 3>>)", "<<1, 2, 3>>"},
	{"subseq", "", "SubSeq(<<1, 2, 3, 4>>, 2, 3)", "<<2, 3>>"},
	{"subseq empty", "", "SubSeq(<<1, 2>>, 3, 2)", "<<>>"},
	{"selectseq", "Even(x) == x % 2 = 0", "SelectSeq(<<1, 2, 3, 4>>, Even)", "<<2, 4>>"},
	{"seq member", "", `<<1, 2>> \in Seq(Nat) /\ <<-1>> \notin Seq(Nat)`, "TRUE"},
	{"colon gt", "", `(1 :> "a") @@ (2 :> "b") @@ (1 :> "c")`, `<<"a", "b">>`},
	{"tostring", "", `ToString(<<1, "a">>)`, `"<<1, \"a\">>"`},
	{"sortseq", "Less(a, b) == a < b", "SortSeq(<<3, 1, 2>>, Less)", "<<1, 2, 3>>"},
	{"permutations", "", `Cardinality(Permutations({1, 2, 3}))`, "6"},
	{"print", "", `Print("hi", 5)`, "5"},
	{"recursive op", "RECURSIVE F(_)\nF(n) == IF n = 0 THEN 1 ELSE n * F(n - 1)", "F(5)", "120"},
	{"recursive fcn", `f[n \in Nat] == IF n = 0 THEN 0 ELSE n + f[n - 1]`, "f[4]", "10"},
	{"op arg", "Twice(G(_), x) == G(G(x))\nInc(n) == n + 1", "Twice(Inc, 1)", "3"},
	{"higher order param call", "Ap(G(_, _)) == G(1, 2)\nAdd(a, b) == a + b", "Ap(Add)", "3"},
	{"string set", "", `"a" \in STRING`, "TRUE"},
	{"boolean set", "", "BOOLEAN", "{FALSE, TRUE}"},
	{"junction list", "", "\n  /\\ 1 = 1\n  /\\ \\/ 1 = 2\n     \\/ 2 = 2\n  /\\ TRUE", "TRUE"},
	{"junction list ends", "", "\n  \\/ FALSE\n  \\/ TRUE", "TRUE"},
	{"set of records sorted", "", `{[a |-> 2], [a |-> 1]}`, "{[a |-> 1], [a |-> 2]}"},
	{"nested set", "", "{{2}, {1}}", "{{1}, {2}}"},
	{"parenthesized in is a map", "", `{(v \in {1}) : v \in {1, 2}}`, "{FALSE, TRUE}"},
	{"string len", "", `Len("abc")`, "3"},
	{"string concat", "", `"ab" \o "c"`, `"abc"`},
	{"minus one power", "", "<<(-1)^3, (-1)^4, (-1)^0>>", "<<-1, 1, 1>>"},
	{"large interval member", "", `5 \in 1..2000000 /\ 0 \notin 1..2000000`, "TRUE"},
}

func TestEval(t *testing.T) {
	for _, tt := range evalTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evalString(tt.defs, tt.in)
			if err != nil {
				t.Fatalf("%s: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("%s = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

var evalErrorTests = []struct {
	name string
	defs string
	in   string
	want string // substring of the error
}{
	{"type mismatch", "", `1 = "a"`, "cannot compare integer 1 with string"},
	{"not bool", "", "1 /\\ TRUE", "expected a boolean"},
	{"domain", "", "<<1>>[2]", "not in the domain"},
	{"undefined", "", "Foo", "undefined: Foo"},
	{"overflow", "", "9223372036854775807 + 1", "integer overflow"},
	{"mul overflow", "", "4611686018427387904 * 2", "integer overflow"},
	{"div zero", "", `1 \div 0`, "division by zero"},
	{"head empty", "", "Head(<<>>)", "Head of the empty sequence"},
	{"infinite", "", `{x \in Nat : x < 3}`, "cannot enumerate the infinite set Nat"},
	{"choose none", "", `CHOOSE x \in {1} : x > 1`, "CHOOSE found no value"},
	{"case none", "", "CASE FALSE -> 1", "no CASE arm applies"},
	{"arity", "F(a) == a", "F(1, 2)", "F takes 1 arguments, got 2"},
	{"recursion", "RECURSIVE F(_)\nF(n) == F(n + 1)", "F(0)", "recursion too deep"},
	{"assert", "", `Assert(FALSE, "boom")`, `assertion failed: "boom"`},
	{"member across kinds", "", `1 \in STRING`, "cannot test whether integer 1 is in STRING"},
	{"member across kinds finite", "", `1 \in {"a"}`, "cannot test whether integer 1 is in a set of strings"},
	{"large interval", "", `{x \in 1..2000000 : x < 3}`, "cannot enumerate 1..2000000: more than 1048576 elements"},
	{"div overflow", "", `(-9223372036854775807 - 1) \div -1`, "integer overflow"},
	{"sub overflow", "", `(-9223372036854775807 - 1) - 1`, "integer overflow"},
	{"unknown operator", "", `1 \prec 2`, "unexpected"},
}

// TestIntEdges checks int64 arithmetic at its limits, where TLC, whose
// integers are 32 bits, cannot serve as the reference.
func TestIntEdges(t *testing.T) {
	tests := []struct{ in, want string }{
		{"-1 - (-9223372036854775807 - 1)", "9223372036854775807"},
		{"0 - 9223372036854775807", "-9223372036854775807"},
		{`(-9223372036854775807 - 1) \div 1`, "-9223372036854775808"},
	}
	for _, tt := range tests {
		got, err := evalString("", tt.in)
		if err != nil || got != tt.want {
			t.Errorf("%s = %s, %v; want %s", tt.in, got, err, tt.want)
		}
	}
}

func TestEvalError(t *testing.T) {
	for _, tt := range evalErrorTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evalString(tt.defs, tt.in)
			if err == nil {
				t.Fatalf("%s = %s, want error containing %q", tt.in, got, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("%s: error %q, want it to contain %q", tt.in, err, tt.want)
			}
		})
	}
}

func TestModuleGating(t *testing.T) {
	// Sequences instances Naturals LOCALly, so + is not visible.
	src := "---- MODULE T ----\nEXTENDS Sequences\nASSUME 1 + 1 = 2\n===="
	_, err := Parse("T.tla", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "undefined: +") {
		t.Fatalf("Parse: %v, want undefined: +", err)
	}
	src = "---- MODULE T ----\nEXTENDS Integers\nASSUME -1 + 1 = 0 /\\ 2 \\in Nat\n===="
	if _, err := Parse("T.tla", []byte(src)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}
