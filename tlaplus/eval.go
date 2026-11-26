package tlaplus

import (
	"fmt"
	"slices"
	"strings"
)

// An evalError is an error in evaluating an expression: a type error,
// an application outside a function's domain, an undefined name. It
// travels up the evaluator as a panic and is recovered at the API
// boundary.
type evalError struct {
	err error
}

func (s *Spec) errorf(at pos, format string, args ...any) {
	panic(evalError{&posError{file: s.file, pos: at, msg: fmt.Sprintf(format, args...)}})
}

// catch recovers an evalError into *err.
func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(evalError)
		if !ok {
			panic(r)
		}
		*err = e.err
	}
}

// A scope is a chain of bound names: quantifier variables, operator
// parameters and LET definitions.
type scope struct {
	name string
	val  value  // a value, or nil for an operator
	op   *opDef // a LET definition or an operator parameter's operator
	env  *scope // the scope op was defined in
	next *scope
}

func (sc *scope) bind(name string, v value) *scope {
	return &scope{name: name, val: v, next: sc}
}

func (sc *scope) lookup(name string) *scope {
	for ; sc != nil; sc = sc.next {
		if sc.name == name {
			return sc
		}
	}
	return nil
}

// A ctx is the state an expression is evaluated in.
type ctx struct {
	env *scope
	// cur holds the values of the variables in the current state, by
	// index; nxt the values in the next state, nil where not yet
	// determined. When evaluating Init, cur is nil and nxt holds the
	// state being determined, which unprimed variables then name.
	cur, nxt []value
	init     bool
	// assume is set while evaluating an ASSUME statement, where
	// variables, primes and UNCHANGED are not allowed.
	assume bool
	// primed is set while evaluating e', so that unprimed variables
	// name the next state.
	primed bool
	// at is the value @ names inside an EXCEPT update.
	at value
	// fail records the most promising way the action failed, for the
	// error message, and progress counts the conjuncts solved so far.
	fail     *failure
	progress int
	// depth counts nested operator calls, shared by every ctx derived
	// from one evaluation.
	depth *int
}

func (c *ctx) with(env *scope) *ctx {
	d := *c
	d.env = env
	return &d
}

// A failure explains why an action did not reach an observed state.
// progress counts the conjuncts that held before it, so that the
// failure reported is the one that got furthest.
type failure struct {
	progress int
	at       pos
	msg      string
}

// maxDepth bounds operator recursion.
const maxDepth = 2000

// eval evaluates e.
func (s *Spec) eval(e expr, c *ctx) value {
	switch e := e.(type) {
	case *numExpr:
		return e.n
	case *strExpr:
		return e.s
	case *boolExpr:
		return e.b
	case *identExpr:
		return s.evalIdent(e, c)
	case *applyExpr:
		return s.evalApply(e, c)
	case *junctionExpr:
		for _, item := range e.items {
			b := s.evalBool(item, c)
			if b != e.and {
				return b
			}
		}
		return e.and
	case *primeExpr:
		if c.assume {
			s.errorf(e.p, "prime not allowed in ASSUME")
		}
		if c.init || c.primed {
			s.errorf(e.p, "prime not allowed here")
		}
		d := *c
		d.primed = true
		return s.eval(e.x, &d)
	case *unchangedExpr:
		if c.assume {
			s.errorf(e.p, "UNCHANGED not allowed in ASSUME")
		}
		if c.init {
			s.errorf(e.p, "UNCHANGED not allowed in an initial predicate")
		}
		d := *c
		d.primed = true
		return s.equal(e.p, s.eval(e.x, c), s.eval(e.x, &d))
	case *quantExpr:
		// \A holds unless some binding falsifies the body; \E holds
		// if some binding satisfies it.
		result := e.forall
		s.forEach(e.binds, c, func(c *ctx) bool {
			if s.evalBool(e.body, c) != e.forall {
				result = !e.forall
				return false
			}
			return true
		})
		return result
	case *chooseExpr:
		var found value
		s.forEach([]binding{e.bind}, c, func(c *ctx) bool {
			if s.evalBool(e.body, c) {
				found = s.bindingValue(e.bind, c)
				return false
			}
			return true
		})
		if found == nil {
			s.errorf(e.p, "CHOOSE found no value satisfying its predicate")
		}
		return found
	case *ifExpr:
		if s.evalBool(e.cond, c) {
			return s.eval(e.then, c)
		}
		return s.eval(e.els, c)
	case *caseExpr:
		for _, arm := range e.arms {
			if s.evalBool(arm.cond, c) {
				return s.eval(arm.val, c)
			}
		}
		if e.other == nil {
			s.errorf(e.p, "no CASE arm applies")
		}
		return s.eval(e.other, c)
	case *letExpr:
		return s.eval(e.body, c.with(letScope(e.defs, c.env)))
	case *setEnumExpr:
		elems := make([]value, len(e.elems))
		for i, x := range e.elems {
			elems[i] = s.force(x.position(), s.eval(x, c))
		}
		return newSet(elems)
	case *setFilterExpr:
		var elems []value
		s.forEach([]binding{e.bind}, c, func(c *ctx) bool {
			if s.evalBool(e.pred, c) {
				elems = append(elems, s.bindingValue(e.bind, c))
			}
			return true
		})
		return newSet(elems)
	case *setMapExpr:
		var elems []value
		s.forEach(e.binds, c, func(c *ctx) bool {
			elems = append(elems, s.force(e.elem.position(), s.eval(e.elem, c)))
			return true
		})
		return newSet(elems)
	case *tupleExpr:
		elems := make([]value, len(e.elems))
		for i, x := range e.elems {
			elems[i] = s.force(x.position(), s.eval(x, c))
		}
		return newSeq(elems)
	case *fcnExpr:
		return s.evalFcn(e, c)
	case *fcnSetExpr:
		return s.fcnSet(e.p, s.eval(e.dom, c), s.eval(e.codom, c))
	case *recordExpr:
		keys := make([]value, len(e.fields))
		vals := make([]value, len(e.fields))
		for i, f := range e.fields {
			keys[i] = f
			vals[i] = s.force(e.vals[i].position(), s.eval(e.vals[i], c))
		}
		return newFcn(keys, vals)
	case *recordSetExpr:
		sets := make([]value, len(e.sets))
		for i, x := range e.sets {
			sets[i] = s.eval(x, c)
		}
		return s.recordSet(e.p, e.fields, sets)
	case *appExpr:
		f := s.eval(e.fcn, c)
		var arg value
		if len(e.args) == 1 {
			arg = s.force(e.args[0].position(), s.eval(e.args[0], c))
		} else {
			elems := make([]value, len(e.args))
			for i, a := range e.args {
				elems[i] = s.force(a.position(), s.eval(a, c))
			}
			arg = newSeq(elems)
		}
		return s.apply(e.p, f, arg)
	case *dotExpr:
		return s.apply(e.p, s.eval(e.x, c), e.field)
	case *exceptExpr:
		f := s.eval(e.fcn, c)
		for _, u := range e.updates {
			f = s.except(f, u.path, u.val, c)
		}
		return f
	case *atExpr:
		if c.at == nil {
			s.errorf(e.p, "@ outside EXCEPT")
		}
		return c.at
	case *timesExpr:
		sets := make([]value, len(e.sets))
		for i, x := range e.sets {
			sets[i] = s.eval(x, c)
		}
		return s.product(e.p, sets)
	case *badExpr:
		panic(evalError{e.err})
	}
	panic(fmt.Sprintf("tlaplus: eval of %T", e))
}

func (s *Spec) evalBool(e expr, c *ctx) bool {
	v := s.eval(e, c)
	b, ok := v.(bool)
	if !ok {
		s.errorf(e.position(), "expected a boolean, found %s %s", kindName(v), format(v))
	}
	return b
}

// letScope returns env extended with a LET's definitions. Each
// definition sees the others, so the scope is built first and the
// definitions point back into it.
func letScope(defs []*opDef, env *scope) *scope {
	sc := env
	var added []*scope
	for _, d := range defs {
		sc = &scope{name: d.name, op: d, next: sc}
		added = append(added, sc)
	}
	for _, a := range added {
		a.env = sc
	}
	return sc
}

func (s *Spec) evalIdent(e *identExpr, c *ctx) value {
	if b := c.env.lookup(e.name); b != nil {
		if b.op != nil {
			return s.callDef(e.p, b.op, nil, c.with(b.env))
		}
		if t, ok := b.val.(*thunk); ok {
			return s.evalThunk(t, c)
		}
		return b.val
	}
	if i, ok := s.varIndex[e.name]; ok {
		return s.varValue(e, i, c)
	}
	if v, ok := s.consts[e.name]; ok {
		return v
	}
	if d, ok := s.defs[e.name]; ok {
		return s.callDef(e.p, d, nil, c.with(nil))
	}
	if v, ok := s.builtinConst(e.name); ok {
		return v
	}
	s.errorf(e.p, "undefined: %s", e.name)
	return nil
}

func (s *Spec) varValue(e *identExpr, i int, c *ctx) value {
	if c.assume {
		s.errorf(e.p, "variable %s cannot appear in ASSUME", e.name)
	}
	var v value
	switch {
	case c.init || c.primed:
		if i >= len(c.nxt) {
			s.errorf(e.p, "variable %s has no state", e.name)
		}
		v = c.nxt[i]
	default:
		if i >= len(c.cur) {
			s.errorf(e.p, "variable %s has no state", e.name)
		}
		v = c.cur[i]
	}
	if v == nil {
		name := e.name
		if c.primed {
			name += "'"
		}
		s.errorf(e.p, "%s is not yet determined", name)
	}
	return v
}

// callDef evaluates a user-defined operator applied to args. c.env is
// the scope the definition was made in.
func (s *Spec) callDef(at pos, d *opDef, args []value, c *ctx) value {
	if d.fcn {
		return s.fcnDef(d, c)
	}
	if len(args) != len(d.params) {
		s.errorf(at, "%s takes %d arguments, got %d", d.name, len(d.params), len(args))
	}
	*c.depth++
	defer func() { *c.depth-- }()
	if *c.depth > maxDepth {
		s.errorf(at, "recursion too deep in %s", d.name)
	}
	env := c.env
	for i, p := range d.params {
		env = env.bind(p.name, args[i])
	}
	return s.eval(d.body, c.with(env))
}

// fcnDef returns the function defined by f[x \in S] == e. It is lazy,
// so that the body may refer to f recursively.
func (s *Spec) fcnDef(d *opDef, c *ctx) value {
	dom := s.domainOf(d.p, d.binds, c)
	return &lazyFcn{name: d.name, dom: dom, apply: func(arg value) value {
		*c.depth++
		defer func() { *c.depth-- }()
		if *c.depth > maxDepth {
			s.errorf(d.p, "recursion too deep in %s", d.name)
		}
		return s.applyBinds(d.p, d.binds, arg, d.body, c)
	}}
}

// domainOf returns the domain of a function over binds: the set
// itself for one binding, and the product for several.
func (s *Spec) domainOf(at pos, binds []binding, c *ctx) value {
	if len(binds) == 1 && !binds[0].tuple {
		return s.eval(binds[0].set, c)
	}
	sets := make([]value, len(binds))
	for i, b := range binds {
		if b.tuple {
			s.errorf(at, "tuple binding in a function domain is not supported")
		}
		sets[i] = s.eval(b.set, c)
	}
	return s.product(at, sets)
}

// applyBinds evaluates body with binds bound to the components of arg.
func (s *Spec) applyBinds(at pos, binds []binding, arg value, body expr, c *ctx) value {
	env := c.env
	if len(binds) == 1 {
		env = s.bindValue(at, binds[0], arg, env)
	} else {
		t, ok := arg.(*fcnValue)
		if !ok || !t.isSeq() || len(t.vals) != len(binds) {
			s.errorf(at, "function argument %s does not match its %d-part domain", format(arg), len(binds))
		}
		for i, b := range binds {
			env = s.bindValue(at, b, t.vals[i], env)
		}
	}
	return s.eval(body, c.with(env))
}

func (s *Spec) bindValue(at pos, b binding, v value, env *scope) *scope {
	if !b.tuple {
		return env.bind(b.names[0], v)
	}
	t, ok := v.(*fcnValue)
	if !ok || !t.isSeq() || len(t.vals) != len(b.names) {
		s.errorf(at, "cannot bind %s to <<%s>>", format(v), strings.Join(b.names, ", "))
	}
	for i, n := range b.names {
		env = env.bind(n, t.vals[i])
	}
	return env
}

// bindingValue returns the value a binding currently has in c: the
// bound identifier, or the tuple of them.
func (s *Spec) bindingValue(b binding, c *ctx) value {
	if !b.tuple {
		return c.env.lookup(b.names[0]).val
	}
	elems := make([]value, len(b.names))
	for i, n := range b.names {
		elems[i] = c.env.lookup(n).val
	}
	return newSeq(elems)
}

// forEach calls f with each combination of values of binds bound in
// turn, until f returns false.
func (s *Spec) forEach(binds []binding, c *ctx, f func(c *ctx) bool) bool {
	if len(binds) == 0 {
		return f(c)
	}
	b := binds[0]
	set := s.enumerate(b.set.position(), s.eval(b.set, c))
	for _, v := range set.elems {
		d := c.with(s.bindValue(b.set.position(), b, v, c.env))
		if !s.forEach(binds[1:], d, f) {
			return false
		}
	}
	return true
}

func (s *Spec) evalFcn(e *fcnExpr, c *ctx) value {
	dom := s.domainOf(e.p, e.binds, c)
	ls, lazy := dom.(*lazySet)
	if lazy && ls.elems == nil {
		return &lazyFcn{name: "[" + ls.name + " |-> ...]", dom: dom, apply: func(arg value) value {
			return s.applyBinds(e.p, e.binds, arg, e.body, c)
		}}
	}
	keys := s.enumerate(e.p, dom).elems
	vals := make([]value, len(keys))
	for i, k := range keys {
		vals[i] = s.force(e.body.position(), s.applyBinds(e.p, e.binds, k, e.body, c))
	}
	return &fcnValue{keys: keys, vals: vals}
}

// apply returns f[arg].
func (s *Spec) apply(at pos, f, arg value) value {
	switch f := f.(type) {
	case *fcnValue:
		v, ok := f.lookup(arg)
		if !ok {
			s.errorf(at, "%s is not in the domain of %s", format(arg), format(f))
		}
		return v
	case *lazyFcn:
		if !s.member(at, arg, f.dom) {
			s.errorf(at, "%s is not in the domain of %s", format(arg), f.name)
		}
		return f.apply(arg)
	}
	s.errorf(at, "cannot apply %s %s as a function", kindName(f), format(f))
	return nil
}

func (s *Spec) except(f value, path []exceptPath, val expr, c *ctx) value {
	fv, ok := s.force(val.position(), f).(*fcnValue)
	if !ok {
		s.errorf(val.position(), "EXCEPT on %s %s", kindName(f), format(f))
	}
	var key value
	p := path[0]
	if p.args == nil {
		key = p.field
	} else if len(p.args) == 1 {
		key = s.force(p.args[0].position(), s.eval(p.args[0], c))
	} else {
		elems := make([]value, len(p.args))
		for i, a := range p.args {
			elems[i] = s.force(a.position(), s.eval(a, c))
		}
		key = newSeq(elems)
	}
	i, ok := slices.BinarySearchFunc(fv.keys, key, compare)
	if !ok {
		// TLC leaves the function unchanged when the key is outside
		// its domain.
		return fv
	}
	var nv value
	if len(path) > 1 {
		nv = s.except(fv.vals[i], path[1:], val, c)
	} else {
		d := *c
		d.at = fv.vals[i]
		nv = s.force(val.position(), s.eval(val, &d))
	}
	out := &fcnValue{keys: fv.keys, vals: append([]value(nil), fv.vals...)}
	out.vals[i] = nv
	return out
}

// equal reports whether a = b. Like TLC, it refuses to compare values
// of different kinds, which is almost always a bug in the spec or in
// what the test observed.
func (s *Spec) equal(at pos, a, b value) bool {
	a, b = s.force(at, a), s.force(at, b)
	if kind(a) != kind(b) {
		if isModelValue(a) || isModelValue(b) {
			return false
		}
		s.errorf(at, "cannot compare %s %s with %s %s", kindName(a), format(a), kindName(b), format(b))
	}
	return compare(a, b) == 0
}

// force turns a lazy value with a finite extent into a finite one.
func (s *Spec) force(at pos, v value) value {
	switch v := v.(type) {
	case *lazySet:
		return s.enumerate(at, v)
	case *lazyFcn:
		dom := s.enumerate(at, v.dom)
		vals := make([]value, len(dom.elems))
		for i, k := range dom.elems {
			vals[i] = s.force(at, v.apply(k))
		}
		return &fcnValue{keys: dom.elems, vals: vals}
	case *opValue:
		s.errorf(at, "operator %s used as a value", v.name)
	}
	return v
}

// enumerate returns the elements of a set.
func (s *Spec) enumerate(at pos, v value) *setValue {
	switch v := v.(type) {
	case *setValue:
		return v
	case *lazySet:
		if v.elems == nil && v.large {
			s.errorf(at, "cannot enumerate %s: more than %d elements", v.name, maxFinite)
		}
		if v.elems == nil {
			s.errorf(at, "cannot enumerate the infinite set %s", v.name)
		}
		return v.elems()
	}
	s.errorf(at, "expected a set, found %s %s", kindName(v), format(v))
	return nil
}

// member reports whether v \in set.
func (s *Spec) member(at pos, v, set value) bool {
	switch set := set.(type) {
	case *setValue:
		v = s.force(at, v)
		for _, e := range set.elems {
			if kind(e) != kind(v) {
				if isModelValue(e) || isModelValue(v) {
					continue
				}
				// TLC refuses the comparison, as equal does.
				s.errorf(at, "cannot test whether %s %s is in a set of %ss", kindName(v), format(v), kindName(e))
			}
			if compare(e, v) == 0 {
				return true
			}
		}
		return false
	case *lazySet:
		return set.member(at, v)
	}
	s.errorf(at, "expected a set, found %s %s", kindName(set), format(set))
	return false
}

// evalApply evaluates an operator application: a user-defined
// operator, an operator parameter, or a built-in.
func (s *Spec) evalApply(e *applyExpr, c *ctx) value {
	switch e.name {
	case "=>":
		// Short-circuit, so that P => Q can guard Q's domain.
		return !s.evalBool(e.args[0], c) || s.evalBool(e.args[1], c)
	case "~":
		return !s.evalBool(e.args[0], c)
	}
	if d, env, ok := s.lookupDef(e.name, c); ok {
		return s.callDef(e.p, d, s.bindArgs(e.p, d, e.args, c), c.with(env))
	}
	if b := c.env.lookup(e.name); b != nil {
		op, ok := b.val.(*opValue)
		if !ok {
			s.errorf(e.p, "%s is not an operator", e.name)
		}
		if op.arity != len(e.args) {
			s.errorf(e.p, "%s takes %d arguments, got %d", e.name, op.arity, len(e.args))
		}
		return op.call(e.p, s.evalArgs(e.args, c))
	}
	if f, ok := builtins[e.name]; ok && s.visible[e.name] {
		if n := builtinArity(e.name); len(e.args) != n {
			s.errorf(e.p, "%s takes %d arguments, got %d", e.name, n, len(e.args))
		}
		return f(s, e.p, s.evalArgs(e.args, c))
	}
	if _, ok := builtins[e.name]; ok {
		s.errorf(e.p, "undefined: %s (not in the modules this one extends)", e.name)
	}
	s.errorf(e.p, "undefined: %s", e.name)
	return nil
}

// lookupDef returns the definition name refers to, a LET definition or
// a module-level one, and the scope it was defined in.
func (s *Spec) lookupDef(name string, c *ctx) (*opDef, *scope, bool) {
	if b := c.env.lookup(name); b != nil {
		return b.op, b.env, b.op != nil
	}
	if _, ok := s.varIndex[name]; ok {
		return nil, nil, false
	}
	d, ok := s.defs[name]
	return d, nil, ok
}

// evalArgs evaluates operator arguments. An argument that names an
// operator taking arguments, as in SelectSeq(s, IsEven), becomes an
// *opValue.
func (s *Spec) evalArgs(args []expr, c *ctx) []value {
	vals := make([]value, len(args))
	for i, a := range args {
		if id, ok := a.(*identExpr); ok {
			if d, env, ok := s.lookupDef(id.name, c); ok && len(d.params) > 0 {
				dc := c.with(env)
				vals[i] = &opValue{name: d.name, arity: len(d.params), call: func(at pos, args []value) value {
					return s.callDef(at, d, args, dc)
				}}
				continue
			}
		}
		vals[i] = s.eval(a, c)
	}
	return vals
}

// A thunk is an argument of a user-defined operator. TLA+ operators
// substitute their arguments rather than evaluating them first, so
// that in Keep(v) == UNCHANGED v, Keep(x) means UNCHANGED x: the
// argument is evaluated where the parameter is used, primed if the use
// is primed.
type thunk struct {
	e   expr
	env *scope
	at  value
	// The last value, and the state it was computed in. A parameter is
	// often used many times, as in Replay(m, log), and re-evaluating a
	// chain of arguments each time would be exponential.
	key thunkKey
	val value
}

type thunkKey struct {
	primed, init bool
	cur, nxt     *value
}

func thunkKeyOf(c *ctx) thunkKey {
	k := thunkKey{primed: c.primed, init: c.init}
	if len(c.cur) > 0 {
		k.cur = &c.cur[0]
	}
	if len(c.nxt) > 0 {
		k.nxt = &c.nxt[0]
	}
	return k
}

// evalThunk evaluates t in the state of c. A state's slices are never
// modified once built, so their addresses identify it.
func (s *Spec) evalThunk(t *thunk, c *ctx) value {
	k := thunkKeyOf(c)
	if t.val != nil && t.key == k {
		return t.val
	}
	v := s.eval(t.e, s.thunkCtx(t, c))
	t.key, t.val = k, v
	return v
}

// bindArgs returns the arguments of a call of d: an *opValue for each
// operator parameter, as in F(_), and a thunk for each other.
func (s *Spec) bindArgs(at pos, d *opDef, args []expr, c *ctx) []value {
	if len(args) != len(d.params) {
		s.errorf(at, "%s takes %d arguments, got %d", d.name, len(d.params), len(args))
	}
	vals := make([]value, len(args))
	for i, a := range args {
		if d.params[i].arity > 0 {
			vals[i] = s.evalArgs(args[i:i+1], c)[0]
			op, ok := vals[i].(*opValue)
			if !ok || op.arity != d.params[i].arity {
				s.errorf(a.position(), "%s: argument %d must be an operator of %d arguments", d.name, i+1, d.params[i].arity)
			}
			continue
		}
		vals[i] = &thunk{e: a, env: c.env, at: c.at}
	}
	return vals
}
