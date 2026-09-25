package tlaplus

import (
	"cmp"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// A value is a TLA+ value: bool, int64, string, ModelValue, *setValue,
// *fcnValue, or one of the lazy forms below (*lazySet, *lazyFcn, *opValue).
//
// Sequences, tuples and records are functions, as they are in TLA+:
// <<a, b>> is the function with domain 1..2, and [x |-> 1] the function
// with domain {"x"}. One representation makes <<1, 2>> = [i \in 1..2 |-> i]
// true without a special case.
type value = any

// A setValue is a finite set, its elements sorted by compare and
// distinct.
type setValue struct {
	elems []value
}

// A fcnValue is a function with a finite domain. keys is sorted by
// compare and distinct; vals[i] is the value at keys[i].
type fcnValue struct {
	keys []value
	vals []value
}

// A lazySet is a set given by a membership test, such as Nat or
// [S -> T]. elems enumerates it, and is nil for a set that is infinite
// or, if large is set, too large to enumerate.
type lazySet struct {
	name   string
	member func(at pos, v value) bool
	elems  func() *setValue
	large  bool
}

// A lazyFcn is a function whose domain may be infinite, such as one
// defined recursively: fact[n \in Nat] == ...
type lazyFcn struct {
	name  string
	dom   value
	apply func(v value) value
}

// An opValue is an operator passed as an argument, as in SelectSeq(s, Test).
type opValue struct {
	name  string
	arity int
	call  func(at pos, args []value) value
}

// A ModelValue is a TLC model value: an uninterpreted value, written as
// its bare name, that equals itself and no other value. A model's
// CONSTANT block uses them for sets whose elements matter only by
// identity, such as Nodes = {n1, n2, n3}. A ModelValue may appear in the
// constants given to [Spec.With], in a [State], and in an action's
// arguments. Its name must be a TLA+ identifier.
type ModelValue string

func isModelValue(v value) bool {
	_, ok := v.(ModelValue)
	return ok
}

var emptySet = &setValue{}
var emptyFcn = &fcnValue{}

// newSet returns the set of elems, sorting and deduplicating them.
// It may reorder elems.
func newSet(elems []value) *setValue {
	if len(elems) == 0 {
		return emptySet
	}
	slices.SortFunc(elems, compare)
	elems = slices.CompactFunc(elems, func(a, b value) bool { return compare(a, b) == 0 })
	return &setValue{elems: elems}
}

// newFcn returns the function mapping keys[i] to vals[i]. The keys must
// be distinct.
func newFcn(keys, vals []value) *fcnValue {
	if len(keys) == 0 {
		return emptyFcn
	}
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(a, b int) int { return compare(keys[a], keys[b]) })
	f := &fcnValue{keys: make([]value, len(keys)), vals: make([]value, len(keys))}
	for i, j := range idx {
		f.keys[i], f.vals[i] = keys[j], vals[j]
	}
	return f
}

// newSeq returns the sequence <<elems...>>.
func newSeq(elems []value) *fcnValue {
	if len(elems) == 0 {
		return emptyFcn
	}
	keys := make([]value, len(elems))
	for i := range elems {
		keys[i] = int64(i + 1)
	}
	return &fcnValue{keys: keys, vals: elems}
}

// lookup returns f[k].
func (f *fcnValue) lookup(k value) (value, bool) {
	i, ok := slices.BinarySearchFunc(f.keys, k, compare)
	if !ok {
		return nil, false
	}
	return f.vals[i], true
}

// isSeq reports whether f's domain is 1..n for some n.
func (f *fcnValue) isSeq() bool {
	for i, k := range f.keys {
		if n, ok := k.(int64); !ok || n != int64(i+1) {
			return false
		}
	}
	return true
}

// isRecord reports whether f's domain is a nonempty set of strings
// that read as field names.
func (f *fcnValue) isRecord() bool {
	if len(f.keys) == 0 {
		return false
	}
	for _, k := range f.keys {
		s, ok := k.(string)
		if !ok || !isFieldName(s) {
			return false
		}
	}
	return true
}

func isFieldName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

// kind ranks values for compare. Lazy sets rank with sets and lazy
// functions with functions, but compare them only after forcing.
func kind(v value) int {
	switch v.(type) {
	case bool:
		return 0
	case int64:
		return 1
	case string:
		return 2
	case *setValue, *lazySet:
		return 3
	case *fcnValue, *lazyFcn:
		return 4
	case ModelValue:
		return 5
	}
	return 6
}

func kindName(v value) string {
	switch v := v.(type) {
	case bool:
		return "boolean"
	case int64:
		return "integer"
	case string:
		return "string"
	case *setValue, *lazySet:
		return "set"
	case *fcnValue:
		switch {
		case len(v.keys) == 0 || v.isSeq():
			return "sequence"
		case v.isRecord():
			return "record"
		}
		return "function"
	case *lazyFcn:
		return "function"
	case ModelValue:
		return "model value"
	case *opValue:
		return "operator"
	}
	return fmt.Sprintf("%T", v)
}

// compare is a total order on finite values, used to keep sets and
// function domains canonical. It orders values of different kinds by
// kind; TLA+ itself never compares them, and eval's equality reports
// an error instead of consulting this order. compare panics on a lazy
// value that has not been forced.
func compare(a, b value) int {
	ka, kb := kind(a), kind(b)
	if ka != kb {
		return cmp.Compare(ka, kb)
	}
	switch a := a.(type) {
	case bool:
		b := b.(bool)
		switch {
		case a == b:
			return 0
		case !a:
			return -1
		}
		return 1
	case int64:
		return cmp.Compare(a, b.(int64))
	case string:
		return strings.Compare(a, b.(string))
	case ModelValue:
		return strings.Compare(string(a), string(b.(ModelValue)))
	case *setValue:
		b := b.(*setValue)
		if c := cmp.Compare(len(a.elems), len(b.elems)); c != 0 {
			return c
		}
		for i := range a.elems {
			if c := compare(a.elems[i], b.elems[i]); c != 0 {
				return c
			}
		}
		return 0
	case *fcnValue:
		b := b.(*fcnValue)
		if c := cmp.Compare(len(a.keys), len(b.keys)); c != 0 {
			return c
		}
		for i := range a.keys {
			if c := compare(a.keys[i], b.keys[i]); c != 0 {
				return c
			}
		}
		for i := range a.vals {
			if c := compare(a.vals[i], b.vals[i]); c != 0 {
				return c
			}
		}
		return 0
	}
	panic(fmt.Sprintf("tlaplus: compare of unforced %T", a))
}

// format returns v in TLA+ syntax.
func format(v value) string {
	var b strings.Builder
	writeValue(&b, v)
	return b.String()
}

func writeValue(b *strings.Builder, v value) {
	switch v := v.(type) {
	case bool:
		if v {
			b.WriteString("TRUE")
		} else {
			b.WriteString("FALSE")
		}
	case int64:
		b.WriteString(strconv.FormatInt(v, 10))
	case string:
		writeString(b, v)
	case ModelValue:
		b.WriteString(string(v))
	case *setValue:
		b.WriteByte('{')
		for i, e := range v.elems {
			if i > 0 {
				b.WriteString(", ")
			}
			writeValue(b, e)
		}
		b.WriteByte('}')
	case *fcnValue:
		switch {
		case v.isSeq():
			b.WriteString("<<")
			for i, e := range v.vals {
				if i > 0 {
					b.WriteString(", ")
				}
				writeValue(b, e)
			}
			b.WriteString(">>")
		case v.isRecord():
			b.WriteByte('[')
			for i, k := range v.keys {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(k.(string))
				b.WriteString(" |-> ")
				writeValue(b, v.vals[i])
			}
			b.WriteByte(']')
		default:
			b.WriteByte('(')
			for i, k := range v.keys {
				if i > 0 {
					b.WriteString(" @@ ")
				}
				writeValue(b, k)
				b.WriteString(" :> ")
				writeValue(b, v.vals[i])
			}
			b.WriteByte(')')
		}
	case *lazySet:
		b.WriteString(v.name)
	case *lazyFcn:
		b.WriteString(v.name)
	case *opValue:
		b.WriteString(v.name)
	default:
		fmt.Fprintf(b, "<%T>", v)
	}
}

func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		case '\f':
			b.WriteString(`\f`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
}

// fromGo converts a Go value to a TLA+ value, following the mapping in
// the package documentation.
func fromGo(x any) (value, error) {
	if x == nil {
		return nil, fmt.Errorf("cannot convert nil to a TLA+ value")
	}
	return fromReflect(reflect.ValueOf(x), 0)
}

// maxGoDepth bounds the nesting of a Go value, which may be cyclic.
const maxGoDepth = 100

var (
	emptyStruct    = reflect.TypeFor[struct{}]()
	modelValueType = reflect.TypeFor[ModelValue]()
)

// isModelName reports whether s is a TLA+ identifier that is not a
// reserved word: at least one letter among letters, digits and
// underscores.
func isModelName(s string) bool {
	letter := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isIdentByte(c) {
			return false
		}
		if !isDigit(c) && c != '_' {
			letter = true
		}
	}
	return letter && !keywords[s]
}

func fromReflect(rv reflect.Value, depth int) (value, error) {
	if depth > maxGoDepth {
		return nil, fmt.Errorf("value nested more than %d deep", maxGoDepth)
	}
	depth++
	if rv.Type() == modelValueType {
		name := rv.String()
		if !isModelName(name) {
			return nil, fmt.Errorf("model value %q is not an identifier", name)
		}
		return ModelValue(name), nil
	}
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		u := rv.Uint()
		if u > math.MaxInt64 {
			return nil, fmt.Errorf("integer %d out of range", u)
		}
		return int64(u), nil
	case reflect.String:
		return rv.String(), nil
	case reflect.Slice, reflect.Array:
		elems := make([]value, rv.Len())
		for i := range elems {
			v, err := fromReflect(rv.Index(i), depth)
			if err != nil {
				return nil, err
			}
			elems[i] = v
		}
		return newSeq(elems), nil
	case reflect.Map:
		set := rv.Type().Elem() == emptyStruct
		keys := make([]value, 0, rv.Len())
		vals := make([]value, 0, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			k, err := fromReflect(it.Key(), depth)
			if err != nil {
				return nil, err
			}
			keys = append(keys, k)
			if set {
				continue
			}
			v, err := fromReflect(it.Value(), depth)
			if err != nil {
				return nil, err
			}
			vals = append(vals, v)
		}
		if set {
			return newSet(keys), nil
		}
		if len(newSet(slices.Clone(keys)).elems) < len(keys) {
			return nil, fmt.Errorf("map %s has keys that convert to the same value", rv.Type())
		}
		return newFcn(keys, vals), nil
	case reflect.Struct:
		t := rv.Type()
		var keys, vals []value
		for i := range t.NumField() {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			v, err := fromReflect(rv.Field(i), depth)
			if err != nil {
				return nil, fmt.Errorf("field %s: %w", f.Name, err)
			}
			keys = append(keys, f.Name)
			vals = append(vals, v)
		}
		return newFcn(keys, vals), nil
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil, fmt.Errorf("cannot convert nil %s to a TLA+ value", rv.Type())
		}
		return fromReflect(rv.Elem(), depth)
	}
	return nil, fmt.Errorf("cannot convert %s to a TLA+ value", rv.Type())
}
