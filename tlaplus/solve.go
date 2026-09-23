package tlaplus

import (
	"fmt"
	"slices"
)

// solve calls k with each extension of c.nxt in which the predicate or
// action e holds. An action determines a variable x through a conjunct
// x' = e or x' \in S: if x' is still open the conjunct binds it, and if
// it is already known (observed, or bound by an earlier conjunct) the
// conjunct tests it. Disjunction and \E branch. Anything else is
// evaluated as a predicate. In an initial predicate, unprimed variables
// play the role of primed ones.
func (s *Spec) solve(e expr, c *ctx, k func(*ctx)) {
	switch e := e.(type) {
	case *junctionExpr:
		if e.and {
			s.solveAll(e.items, c, k)
			return
		}
		for _, item := range e.items {
			s.solve(item, c, k)
		}
		return
	case *quantExpr:
		k := s.restore(c, k)
		if !e.forall {
			s.forEach(e.binds, c, func(d *ctx) bool {
				s.solve(e.body, d, k)
				return true
			})
			return
		}
		// \A is the conjunction of its body over every binding.
		var envs []*scope
		s.forEach(e.binds, c, func(d *ctx) bool {
			envs = append(envs, d.env)
			return true
		})
		var solveFrom func(i int, c *ctx)
		solveFrom = func(i int, c *ctx) {
			if i == len(envs) {
				k(c)
				return
			}
			s.solve(e.body, c.with(envs[i]), func(d *ctx) { solveFrom(i+1, d) })
		}
		solveFrom(0, c)
		return
	case *ifExpr:
		if s.evalBool(e.cond, c) {
			s.solve(e.then, c, k)
		} else {
			s.solve(e.els, c, k)
		}
		return
	case *caseExpr:
		for _, arm := range e.arms {
			if s.evalBool(arm.cond, c) {
				s.solve(arm.val, c, k)
				return
			}
		}
		if e.other == nil {
			s.errorf(e.p, "no CASE arm applies")
		}
		s.solve(e.other, c, k)
		return
	case *letExpr:
		s.solve(e.body, c.with(letScope(e.defs, c.env)), s.restore(c, k))
		return
	case *identExpr:
		if t, ok := s.thunkOf(e, c); ok {
			s.solve(t.e, s.thunkCtx(t, c), s.restore(c, k))
			return
		}
		if d, env, ok := s.lookupDef(e.name, c); ok && !d.fcn && len(d.params) == 0 {
			s.solveDef(e.p, d, nil, c.with(env), s.restore(c, k))
			return
		}
	case *applyExpr:
		switch e.name {
		case "=":
			if s.solveEq(e, c, k) {
				return
			}
		case `\in`:
			if i, ok := s.openVar(e.args[0], c); ok {
				set := s.enumerate(e.p, s.eval(e.args[1], c))
				for _, v := range set.elems {
					s.solveBind(i, v, c, k)
				}
				if len(set.elems) == 0 {
					s.fail(c, start(e), "%s is empty", s.snippet(e.args[1]))
				}
				return
			}
		default:
			if d, env, ok := s.lookupDef(e.name, c); ok && !d.fcn {
				s.solveDef(e.p, d, s.bindArgs(e.p, d, e.args, c), c.with(env), s.restore(c, k))
				return
			}
		}
	case *unchangedExpr:
		if c.init {
			s.errorf(e.p, "UNCHANGED not allowed in an initial predicate")
		}
		s.solveUnchanged(e.x, e.p, c, k)
		return
	}
	if s.evalBool(e, c) {
		k(c)
		return
	}
	if a, ok := e.(*applyExpr); ok && a.name == "=" {
		s.fail(c, start(e), "%s is false: %s # %s", s.snippet(e),
			format(s.force(a.p, s.eval(a.args[0], c))), format(s.force(a.p, s.eval(a.args[1], c))))
		return
	}
	s.fail(c, start(e), "%s is false", s.snippet(e))
}

// solveAll solves the conjunction of items.
func (s *Spec) solveAll(items []expr, c *ctx, k func(*ctx)) {
	if len(items) == 0 {
		k(c)
		return
	}
	s.solve(items[0], c, func(d *ctx) {
		d2 := *d
		d2.progress++
		s.solveAll(items[1:], &d2, k)
	})
}

// solveDef solves the body of the operator d applied to args, as if it
// were written in place.
func (s *Spec) solveDef(at pos, d *opDef, args []value, c *ctx, k func(*ctx)) {
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
	s.solve(d.body, c.with(env), k)
}

// solveEq solves a = b where one side is an open variable, reporting
// whether it applied.
func (s *Spec) solveEq(e *applyExpr, c *ctx, k func(*ctx)) bool {
	x, y := e.args[0], e.args[1]
	i, ok := s.openVar(x, c)
	if !ok {
		i, ok = s.openVar(y, c)
		x, y = y, x
	}
	if !ok {
		return false
	}
	s.solveBind(i, s.force(y.position(), s.eval(y, c)), c, k)
	return true
}

func (s *Spec) solveBind(i int, v value, c *ctx, k func(*ctx)) {
	d := *c
	d.nxt = slices.Clone(c.nxt)
	d.nxt[i] = v
	k(&d)
}

// openVar reports whether e names a variable of the state being
// determined (x' in an action, x in an initial predicate) whose value is
// not yet known, and returns its index. It looks through operator
// parameters and definitions without arguments, so that in
// Set(v) == v' = 1, Set(x) binds x'.
func (s *Spec) openVar(e expr, c *ctx) (int, bool) {
	primed := c.primed
	for range maxResolve {
		switch x := e.(type) {
		case *primeExpr:
			if c.init || primed {
				return 0, false
			}
			primed = true
			e = x.x
			continue
		case *identExpr:
			if next, d, ok := s.resolve(x, c); ok {
				e, c = next, d
				continue
			}
			if c.env.lookup(x.name) != nil || !primed && !c.init {
				return 0, false
			}
			i, ok := s.varIndex[x.name]
			return i, ok && c.nxt[i] == nil
		}
		return 0, false
	}
	return 0, false
}

// maxResolve bounds the chain of names openVar follows.
const maxResolve = 100

// resolve returns the expression the name id stands for, if it is an
// operator parameter or a definition without arguments, and the ctx to
// evaluate it in.
func (s *Spec) resolve(id *identExpr, c *ctx) (expr, *ctx, bool) {
	if t, ok := s.thunkOf(id, c); ok {
		return t.e, s.thunkCtx(t, c), true
	}
	if d, env, ok := s.lookupDef(id.name, c); ok && !d.fcn && len(d.params) == 0 {
		return d.body, c.with(env), true
	}
	return nil, nil, false
}

// thunkOf returns the argument the parameter id is bound to.
func (s *Spec) thunkOf(id *identExpr, c *ctx) (*thunk, bool) {
	if b := c.env.lookup(id.name); b != nil {
		t, ok := b.val.(*thunk)
		return t, ok
	}
	return nil, false
}

// thunkCtx returns c changed to evaluate t's expression.
func (s *Spec) thunkCtx(t *thunk, c *ctx) *ctx {
	d := *c
	d.env = t.env
	d.at = t.at
	return &d
}

// restore wraps the continuation k of an expression solved in a scope
// of its own, a quantifier or an operator body, so that the conjuncts
// after it see the names c sees.
func (s *Spec) restore(c *ctx, k func(*ctx)) func(*ctx) {
	env, at := c.env, c.at
	return func(d *ctx) {
		d2 := *d
		d2.env, d2.at = env, at
		k(&d2)
	}
}

// solveUnchanged solves UNCHANGED x, taking apart tuples and
// definitions such as vars == <<x, y>> so that each variable is bound
// rather than compared.
func (s *Spec) solveUnchanged(x expr, at pos, c *ctx, k func(*ctx)) {
	switch x := x.(type) {
	case *tupleExpr:
		var solveFrom func(i int, c *ctx)
		solveFrom = func(i int, c *ctx) {
			if i == len(x.elems) {
				k(c)
				return
			}
			s.solveUnchanged(x.elems[i], at, c, func(d *ctx) { solveFrom(i+1, d) })
		}
		solveFrom(0, c)
		return
	case *identExpr:
		if t, ok := s.thunkOf(x, c); ok {
			s.solveUnchanged(t.e, at, s.thunkCtx(t, c), s.restore(c, k))
			return
		}
		if c.env.lookup(x.name) == nil {
			if i, ok := s.varIndex[x.name]; ok {
				if c.nxt[i] == nil {
					s.solveBind(i, c.cur[i], c, k)
					return
				}
				if s.equal(at, c.cur[i], c.nxt[i]) {
					k(c)
					return
				}
				s.fail(c, at, "UNCHANGED %s is false: %s' = %s but %s = %s",
					x.name, x.name, format(c.nxt[i]), x.name, format(c.cur[i]))
				return
			}
		}
		if d, env, ok := s.lookupDef(x.name, c); ok && !d.fcn && len(d.params) == 0 {
			s.solveUnchanged(d.body, at, c.with(env), s.restore(c, k))
			return
		}
	}
	d := *c
	d.primed = true
	if s.equal(at, s.eval(x, c), s.eval(x, &d)) {
		k(c)
		return
	}
	s.fail(c, at, "UNCHANGED %s is false", s.snippet(x))
}

// fail records a failed condition, keeping the one that got furthest.
func (s *Spec) fail(c *ctx, at pos, format string, args ...any) {
	if c.fail.msg != "" && c.fail.progress > c.progress {
		return
	}
	*c.fail = failure{progress: c.progress, at: at, msg: fmt.Sprintf(format, args...)}
}

// snippet returns e in TLA+ syntax, shortened for a message.
func (s *Spec) snippet(e expr) string {
	text := exprString(e)
	if r := []rune(text); len(r) > 80 {
		text = string(r[:77]) + "..."
	}
	return text
}

// start returns the position of e's first token. An infix expression's
// own position is its operator's.
func start(e expr) pos {
	var first expr
	switch e := e.(type) {
	case *applyExpr:
		if len(e.args) > 0 {
			first = e.args[0]
		}
	case *junctionExpr:
		first = e.items[0]
	case *primeExpr:
		first = e.x
	case *appExpr:
		first = e.fcn
	case *dotExpr:
		first = e.x
	case *timesExpr:
		first = e.sets[0]
	}
	p := e.position()
	if first != nil {
		if q := start(first); q.line < p.line || q.line == p.line && q.col < p.col {
			return q
		}
	}
	return p
}
