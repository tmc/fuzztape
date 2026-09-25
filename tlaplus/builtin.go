package tlaplus

import (
	"math"
	"math/bits"
	"slices"
	"unicode/utf8"
)

// A builtin implements an operator of the core language or a standard
// module. Its arguments are evaluated, but not forced.
type builtin func(s *Spec, at pos, args []value) value

// modules lists the operators each standard module defines. A module
// sees an operator only if it extends the module that defines it; the
// standard modules instance each other LOCALly, so EXTENDS Sequences
// does not bring in +. Integers extends Naturals.
var modules = map[string][]string{
	"Naturals":   {"Nat", "+", "-", "*", "^", `\div`, "%", "..", "<", ">", "<=", ">="},
	"Integers":   {"Int", "-."},
	"Sequences":  {"Seq", "Len", `\o`, "Append", "Head", "Tail", "SubSeq", "SelectSeq"},
	"FiniteSets": {"IsFiniteSet", "Cardinality"},
	"TLC":        {"Print", "PrintT", "Assert", "ToString", ":>", "@@", "Permutations", "SortSeq", "TLCEval"},
}

var moduleExtends = map[string][]string{
	"Integers": {"Naturals"},
}

// core lists the operators every module sees.
var core = []string{"=", "#", "~", "=>", "<=>", `\in`, `\notin`, `\cup`, `\cap`, `\`,
	`\subseteq`, `\subset`, `\supseteq`, `\supset`, "SUBSET", "UNION", "DOMAIN", "BOOLEAN", "STRING"}

var builtins map[string]builtin

// arity gives the number of arguments each builtin takes: two unless
// listed here.
var arity = map[string]int{
	"SUBSET": 1, "UNION": 1, "DOMAIN": 1, "-.": 1, "Seq": 1, "Len": 1,
	"Head": 1, "Tail": 1, "IsFiniteSet": 1, "Cardinality": 1, "PrintT": 1,
	"TLCEval": 1, "ToString": 1, "Permutations": 1, "SubSeq": 3,
}

func builtinArity(name string) int {
	if n, ok := arity[name]; ok {
		return n
	}
	return 2
}

func init() {
	builtins = map[string]builtin{
		"=": func(s *Spec, at pos, a []value) value { return s.equal(at, a[0], a[1]) },
		"#": func(s *Spec, at pos, a []value) value { return !s.equal(at, a[0], a[1]) },
		"<=>": func(s *Spec, at pos, a []value) value {
			return s.boolArg(at, a[0]) == s.boolArg(at, a[1])
		},
		`\in`:    func(s *Spec, at pos, a []value) value { return s.member(at, a[0], a[1]) },
		`\notin`: func(s *Spec, at pos, a []value) value { return !s.member(at, a[0], a[1]) },
		`\cup`: func(s *Spec, at pos, a []value) value {
			x, y := s.enumerate(at, a[0]), s.enumerate(at, a[1])
			return newSet(append(slices.Clone(x.elems), y.elems...))
		},
		`\cap`: func(s *Spec, at pos, a []value) value {
			// One side may be infinite, as in S \cap Nat.
			x, y := a[0], a[1]
			if ls, ok := x.(*lazySet); ok && ls.elems == nil {
				x, y = y, x
			}
			return s.filter(at, x, func(v value) bool { return s.member(at, v, y) })
		},
		`\`: func(s *Spec, at pos, a []value) value {
			return s.filter(at, a[0], func(v value) bool { return !s.member(at, v, a[1]) })
		},
		`\subseteq`: func(s *Spec, at pos, a []value) value { return s.subset(at, a[0], a[1]) },
		`\subset`: func(s *Spec, at pos, a []value) value {
			return s.subset(at, a[0], a[1]) && !s.equal(at, a[0], a[1])
		},
		`\supseteq`: func(s *Spec, at pos, a []value) value { return s.subset(at, a[1], a[0]) },
		`\supset`: func(s *Spec, at pos, a []value) value {
			return s.subset(at, a[1], a[0]) && !s.equal(at, a[0], a[1])
		},
		"SUBSET": func(s *Spec, at pos, a []value) value { return s.powerset(at, a[0]) },
		"UNION": func(s *Spec, at pos, a []value) value {
			var elems []value
			for _, e := range s.enumerate(at, a[0]).elems {
				elems = append(elems, s.enumerate(at, e).elems...)
			}
			return newSet(elems)
		},
		"DOMAIN": func(s *Spec, at pos, a []value) value {
			switch f := a[0].(type) {
			case *fcnValue:
				return &setValue{elems: f.keys}
			case *lazyFcn:
				return f.dom
			}
			s.errorf(at, "DOMAIN of %s %s", kindName(a[0]), format(a[0]))
			return nil
		},

		"+": func(s *Spec, at pos, a []value) value {
			x, y := s.ints(at, a)
			r, overflow := addInt(x, y)
			if overflow {
				s.errorf(at, "integer overflow in %d + %d", x, y)
			}
			return r
		},
		"-": func(s *Spec, at pos, a []value) value {
			x, y := s.ints(at, a)
			r, overflow := subInt(x, y)
			if overflow {
				s.errorf(at, "integer overflow in %d - %d", x, y)
			}
			return r
		},
		"-.": func(s *Spec, at pos, a []value) value {
			x := s.intArg(at, a[0])
			if x == math.MinInt64 {
				s.errorf(at, "integer overflow in -%d", x)
			}
			return -x
		},
		"*": func(s *Spec, at pos, a []value) value {
			x, y := s.ints(at, a)
			r, overflow := mulInt(x, y)
			if overflow {
				s.errorf(at, "integer overflow in %d * %d", x, y)
			}
			return r
		},
		"^": func(s *Spec, at pos, a []value) value {
			x, y := s.ints(at, a)
			if y < 0 {
				s.errorf(at, "negative exponent in %d^%d", x, y)
			}
			if x == 0 && y == 0 {
				s.errorf(at, "0^0 is undefined")
			}
			if x == -1 {
				return 1 - 2*(y%2)
			}
			r := int64(1)
			for range y {
				var overflow bool
				r, overflow = mulInt(r, x)
				if overflow {
					s.errorf(at, "integer overflow in %d^%d", x, y)
				}
				if r == 0 || r == 1 && x == 1 {
					break
				}
			}
			return r
		},
		`\div`: func(s *Spec, at pos, a []value) value {
			x, y := s.ints(at, a)
			if y == 0 {
				s.errorf(at, "division by zero")
			}
			if x == math.MinInt64 && y == -1 {
				s.errorf(at, "integer overflow in %d \\div %d", x, y)
			}
			q := x / y
			if (x%y != 0) && ((x < 0) != (y < 0)) {
				q-- // TLA+ rounds toward negative infinity.
			}
			return q
		},
		"%": func(s *Spec, at pos, a []value) value {
			x, y := s.ints(at, a)
			if y <= 0 {
				s.errorf(at, "%d %% %d: the divisor must be positive", x, y)
			}
			r := x % y
			if r < 0 {
				r += y
			}
			return r
		},
		"..": func(s *Spec, at pos, a []value) value {
			lo, hi := s.ints(at, a)
			return interval(lo, hi)
		},
		"<":  func(s *Spec, at pos, a []value) value { x, y := s.ints(at, a); return x < y },
		">":  func(s *Spec, at pos, a []value) value { x, y := s.ints(at, a); return x > y },
		"<=": func(s *Spec, at pos, a []value) value { x, y := s.ints(at, a); return x <= y },
		">=": func(s *Spec, at pos, a []value) value { x, y := s.ints(at, a); return x >= y },

		"Seq": func(s *Spec, at pos, a []value) value {
			elem := a[0]
			return &lazySet{name: "Seq(" + format(elem) + ")", member: func(at pos, v value) bool {
				f, ok := s.force(at, v).(*fcnValue)
				if !ok || !f.isSeq() {
					return false
				}
				for _, x := range f.vals {
					if !s.member(at, x, elem) {
						return false
					}
				}
				return true
			}}
		},
		// Len and \o also apply to strings, which TLA+ defines as
		// sequences of characters.
		"Len": func(s *Spec, at pos, a []value) value {
			if str, ok := a[0].(string); ok {
				return int64(utf8.RuneCountInString(str))
			}
			return int64(len(s.seqArg(at, a[0]).vals))
		},
		`\o`: func(s *Spec, at pos, a []value) value {
			if x, ok := a[0].(string); ok {
				if y, ok := a[1].(string); ok {
					return x + y
				}
			}
			x, y := s.seqArg(at, a[0]), s.seqArg(at, a[1])
			return newSeq(append(slices.Clone(x.vals), y.vals...))
		},
		"Append": func(s *Spec, at pos, a []value) value {
			x := s.seqArg(at, a[0])
			return newSeq(append(slices.Clone(x.vals), s.force(at, a[1])))
		},
		"Head": func(s *Spec, at pos, a []value) value {
			x := s.seqArg(at, a[0])
			if len(x.vals) == 0 {
				s.errorf(at, "Head of the empty sequence")
			}
			return x.vals[0]
		},
		"Tail": func(s *Spec, at pos, a []value) value {
			x := s.seqArg(at, a[0])
			if len(x.vals) == 0 {
				s.errorf(at, "Tail of the empty sequence")
			}
			return newSeq(slices.Clone(x.vals[1:]))
		},
		"SubSeq": func(s *Spec, at pos, a []value) value {
			x := s.seqArg(at, a[0])
			m, n := s.intArg(at, a[1]), s.intArg(at, a[2])
			if m > n {
				return emptyFcn
			}
			if m < 1 || n > int64(len(x.vals)) {
				s.errorf(at, "SubSeq(%s, %d, %d): index out of range", format(x), m, n)
			}
			return newSeq(slices.Clone(x.vals[m-1 : n]))
		},
		"SelectSeq": func(s *Spec, at pos, a []value) value {
			x := s.seqArg(at, a[0])
			test := s.opArg(at, a[1], 1)
			var out []value
			for _, v := range x.vals {
				if s.boolArg(at, test.call(at, []value{v})) {
					out = append(out, v)
				}
			}
			return newSeq(out)
		},

		"IsFiniteSet": func(s *Spec, at pos, a []value) value {
			switch v := a[0].(type) {
			case *setValue:
				return true
			case *lazySet:
				return v.elems != nil
			}
			s.errorf(at, "IsFiniteSet of %s %s", kindName(a[0]), format(a[0]))
			return nil
		},
		"Cardinality": func(s *Spec, at pos, a []value) value {
			return int64(len(s.enumerate(at, a[0]).elems))
		},

		"Print":  func(s *Spec, at pos, a []value) value { return a[1] },
		"PrintT": func(s *Spec, at pos, a []value) value { return true },
		"Assert": func(s *Spec, at pos, a []value) value {
			if !s.boolArg(at, a[0]) {
				s.errorf(at, "assertion failed: %s", format(s.force(at, a[1])))
			}
			return true
		},
		"TLCEval":  func(s *Spec, at pos, a []value) value { return a[0] },
		"ToString": func(s *Spec, at pos, a []value) value { return format(s.force(at, a[0])) },
		":>": func(s *Spec, at pos, a []value) value {
			return &fcnValue{keys: []value{s.force(at, a[0])}, vals: []value{s.force(at, a[1])}}
		},
		"@@": func(s *Spec, at pos, a []value) value {
			f, g := s.fcnArg(at, a[0]), s.fcnArg(at, a[1])
			keys, vals := slices.Clone(f.keys), slices.Clone(f.vals)
			for i, k := range g.keys {
				if _, ok := f.lookup(k); !ok {
					keys = append(keys, k)
					vals = append(vals, g.vals[i])
				}
			}
			return newFcn(keys, vals)
		},
		"Permutations": func(s *Spec, at pos, a []value) value {
			elems := s.enumerate(at, a[0]).elems
			var perms []value
			var perm func(i int, used []bool, img []value)
			perm = func(i int, used []bool, img []value) {
				if i == len(elems) {
					perms = append(perms, newFcn(slices.Clone(elems), slices.Clone(img)))
					return
				}
				for j := range elems {
					if !used[j] {
						used[j] = true
						perm(i+1, used, append(img, elems[j]))
						used[j] = false
					}
				}
			}
			perm(0, make([]bool, len(elems)), nil)
			return newSet(perms)
		},
		"SortSeq": func(s *Spec, at pos, a []value) value {
			x := s.seqArg(at, a[0])
			less := s.opArg(at, a[1], 2)
			out := slices.Clone(x.vals)
			slices.SortStableFunc(out, func(p, q value) int {
				if s.boolArg(at, less.call(at, []value{p, q})) {
					return -1
				}
				if s.boolArg(at, less.call(at, []value{q, p})) {
					return 1
				}
				return 0
			})
			return newSeq(out)
		},
	}
}

// builtinConst returns the value of a built-in constant the module can
// see.
func (s *Spec) builtinConst(name string) (value, bool) {
	if !s.visible[name] {
		return nil, false
	}
	switch name {
	case "BOOLEAN":
		return newSet([]value{false, true}), true
	case "STRING":
		return &lazySet{name: "STRING", member: func(at pos, v value) bool {
			_, ok := s.kindArg(at, v, "STRING", "")
			return ok
		}}, true
	case "Nat":
		return &lazySet{name: "Nat", member: func(at pos, v value) bool {
			n, ok := s.kindArg(at, v, "Nat", int64(0))
			return ok && n.(int64) >= 0
		}}, true
	case "Int":
		return &lazySet{name: "Int", member: func(at pos, v value) bool {
			_, ok := s.kindArg(at, v, "Int", int64(0))
			return ok
		}}, true
	}
	return nil, false
}

// kindArg returns v, which must be of the same kind as like, for a
// membership test in the built-in set named set. Like TLC, it treats a
// test across kinds, such as 1 \in STRING, as an error rather than
// false, except that a model value is simply in none of the sets:
// kindArg reports false for one.
func (s *Spec) kindArg(at pos, v value, set string, like value) (value, bool) {
	if isModelValue(v) {
		return nil, false
	}
	if kind(v) != kind(like) {
		s.errorf(at, "cannot test whether %s %s is in %s", kindName(v), format(v), set)
	}
	return v, true
}

func (s *Spec) boolArg(at pos, v value) bool {
	b, ok := v.(bool)
	if !ok {
		s.errorf(at, "expected a boolean, found %s %s", kindName(v), format(v))
	}
	return b
}

func (s *Spec) intArg(at pos, v value) int64 {
	n, ok := v.(int64)
	if !ok {
		s.errorf(at, "expected an integer, found %s %s", kindName(v), format(v))
	}
	return n
}

func (s *Spec) ints(at pos, a []value) (int64, int64) {
	return s.intArg(at, a[0]), s.intArg(at, a[1])
}

func (s *Spec) fcnArg(at pos, v value) *fcnValue {
	f, ok := s.force(at, v).(*fcnValue)
	if !ok {
		s.errorf(at, "expected a function, found %s %s", kindName(v), format(v))
	}
	return f
}

func (s *Spec) seqArg(at pos, v value) *fcnValue {
	f, ok := s.force(at, v).(*fcnValue)
	if !ok || !f.isSeq() {
		s.errorf(at, "expected a sequence, found %s %s", kindName(v), format(v))
	}
	return f
}

func (s *Spec) opArg(at pos, v value, arity int) *opValue {
	op, ok := v.(*opValue)
	if !ok || op.arity != arity {
		s.errorf(at, "expected an operator of %d arguments, found %s", arity, format(v))
	}
	return op
}

func addInt(x, y int64) (int64, bool) {
	r := x + y
	return r, (x > 0 && y > 0 && r < 0) || (x < 0 && y < 0 && r >= 0)
}

func subInt(x, y int64) (int64, bool) {
	r := x - y
	return r, (y > 0 && r > x) || (y < 0 && r < x)
}

func mulInt(x, y int64) (int64, bool) {
	if x == 0 || y == 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(uint64(abs(x)), uint64(abs(y)))
	neg := (x < 0) != (y < 0)
	if hi != 0 || lo > math.MaxInt64+uint64(boolInt(neg)) {
		return 0, true
	}
	if neg {
		return -int64(lo), false
	}
	return int64(lo), false
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// maxFinite bounds the sets the evaluator will build: an interval,
// SUBSET S, [S -> T]. A trace check that needs more is almost
// certainly enumerating something it should test for membership.
const maxFinite = 1 << 20

// interval returns lo..hi.
func interval(lo, hi int64) value {
	if hi < lo {
		return emptySet
	}
	if uint64(hi-lo) < maxFinite {
		elems := make([]value, 0, hi-lo+1)
		for i := lo; i <= hi; i++ {
			elems = append(elems, i)
		}
		return &setValue{elems: elems}
	}
	return &lazySet{name: formatInterval(lo, hi), large: true, member: func(at pos, v value) bool {
		n, ok := v.(int64)
		return ok && lo <= n && n <= hi
	}}
}

func formatInterval(lo, hi int64) string {
	return format(lo) + ".." + format(hi)
}

// filter returns the elements of set for which keep reports true.
func (s *Spec) filter(at pos, set value, keep func(value) bool) value {
	var elems []value
	for _, e := range s.enumerate(at, set).elems {
		if keep(e) {
			elems = append(elems, e)
		}
	}
	return &setValue{elems: elems}
}

func (s *Spec) subset(at pos, x, y value) bool {
	for _, e := range s.enumerate(at, x).elems {
		if !s.member(at, e, y) {
			return false
		}
	}
	return true
}

func (s *Spec) powerset(at pos, set value) value {
	base := set
	return &lazySet{
		name: "SUBSET " + format(set),
		member: func(at pos, v value) bool {
			if kind(v) != kind(emptySet) {
				return false
			}
			return s.subset(at, v, base)
		},
		elems: func() *setValue {
			elems := s.enumerate(at, base).elems
			if len(elems) > 20 {
				s.errorf(at, "SUBSET of a %d-element set is too large to enumerate", len(elems))
			}
			var subsets []value
			for mask := range 1 << len(elems) {
				var sub []value
				for i, e := range elems {
					if mask&(1<<i) != 0 {
						sub = append(sub, e)
					}
				}
				subsets = append(subsets, &setValue{elems: sub})
			}
			return newSet(subsets)
		},
	}
}

// fcnSet returns [dom -> codom].
func (s *Spec) fcnSet(at pos, dom, codom value) value {
	return &lazySet{
		name: "[" + format(dom) + " -> " + format(codom) + "]",
		member: func(at pos, v value) bool {
			f, ok := s.force(at, v).(*fcnValue)
			if !ok {
				return false
			}
			d := s.enumerate(at, dom)
			if len(d.elems) != len(f.keys) {
				return false
			}
			for i, k := range f.keys {
				if compare(k, d.elems[i]) != 0 || !s.member(at, f.vals[i], codom) {
					return false
				}
			}
			return true
		},
		elems: func() *setValue {
			keys := s.enumerate(at, dom).elems
			vals := s.enumerate(at, codom).elems
			total := 1
			for range keys {
				total *= len(vals)
				if total > maxFinite {
					s.errorf(at, "[%s -> %s] is too large to enumerate", format(dom), format(codom))
				}
			}
			var fcns []value
			img := make([]value, len(keys))
			var fill func(i int)
			fill = func(i int) {
				if i == len(keys) {
					fcns = append(fcns, &fcnValue{keys: keys, vals: slices.Clone(img)})
					return
				}
				for _, v := range vals {
					img[i] = v
					fill(i + 1)
				}
			}
			fill(0)
			return newSet(fcns)
		},
	}
}

// recordSet returns [f1 : S1, f2 : S2].
func (s *Spec) recordSet(at pos, fields []string, sets []value) value {
	keys := make([]value, len(fields))
	for i, f := range fields {
		keys[i] = f
	}
	dom := newSet(slices.Clone(keys))
	byField := newFcn(keys, sets)
	return &lazySet{
		name: "[record set]",
		member: func(at pos, v value) bool {
			f, ok := s.force(at, v).(*fcnValue)
			if !ok || len(f.keys) != len(dom.elems) {
				return false
			}
			for i, k := range f.keys {
				set, ok := byField.lookup(k)
				if !ok || !s.member(at, f.vals[i], set) {
					return false
				}
			}
			return true
		},
		elems: func() *setValue {
			var recs []value
			vals := make([]value, len(byField.keys))
			var fill func(i int)
			fill = func(i int) {
				if i == len(byField.keys) {
					recs = append(recs, &fcnValue{keys: byField.keys, vals: slices.Clone(vals)})
					if len(recs) > maxFinite {
						s.errorf(at, "record set is too large to enumerate")
					}
					return
				}
				for _, v := range s.enumerate(at, byField.vals[i]).elems {
					vals[i] = v
					fill(i + 1)
				}
			}
			fill(0)
			return newSet(recs)
		},
	}
}

// product returns S1 \X S2 \X ..., a set of tuples.
func (s *Spec) product(at pos, sets []value) value {
	return &lazySet{
		name: "[product]",
		member: func(at pos, v value) bool {
			f, ok := s.force(at, v).(*fcnValue)
			if !ok || !f.isSeq() || len(f.vals) != len(sets) {
				return false
			}
			for i, x := range f.vals {
				if !s.member(at, x, sets[i]) {
					return false
				}
			}
			return true
		},
		elems: func() *setValue {
			var tuples []value
			tuple := make([]value, len(sets))
			var fill func(i int)
			fill = func(i int) {
				if i == len(sets) {
					tuples = append(tuples, newSeq(slices.Clone(tuple)))
					if len(tuples) > maxFinite {
						s.errorf(at, "product is too large to enumerate")
					}
					return
				}
				for _, v := range s.enumerate(at, sets[i]).elems {
					tuple[i] = v
					fill(i + 1)
				}
			}
			fill(0)
			return newSet(tuples)
		},
	}
}
