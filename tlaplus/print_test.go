package tlaplus

import "testing"

func TestExprString(t *testing.T) {
	tests := []string{
		`x' = x + 1`,
		`a /\ (b \/ c)`,
		`(a => b) => c`,
		`a => b => c`,
		`1 - (2 - 3)`,
		`1 - 2 - 3`,
		`~a = b`, // ~ binds looser than =
		`-(1 + 2)`,
		`DOMAIN f \cup {1}`,
		`\E n \in 0..Len(p) : x' = SubSeq(p, 1, n)`,
		`[k \in DOMAIN m |-> m[k]]`,
		`[a |-> 1, b |-> "x"]`,
		`[a : Nat, b : STRING]`,
		`[f EXCEPT ![1].a = @ + 1]`,
		`{x \in S : x > 0}`,
		`{x * 2 : x \in S}`,
		`<<1, <<>>>>`,
		`r.a[2]`,
		`IF p THEN 1 ELSE 2`,
		`S \X T \X U`,
		`UNCHANGED <<x, y>>`,
		`[S -> T]`,
		`CHOOSE x \in S : TRUE`,
	}
	for _, src := range tests {
		m, err := parseModule("T.tla", []byte("---- MODULE T ----\nX == "+src+"\n===="))
		if err != nil {
			t.Errorf("%s: %v", src, err)
			continue
		}
		if got := exprString(m.defs[0].body); got != src {
			t.Errorf("exprString(%s) = %s", src, got)
		}
	}
}
