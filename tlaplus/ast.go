package tlaplus

// An expr is a node of a parsed TLA+ expression.
type expr interface {
	position() pos
}

type (
	// identExpr is a name: a variable, an operator of no arguments, or a
	// bound identifier.
	identExpr struct {
		p    pos
		name string
	}
	numExpr struct {
		p pos
		n int64
	}
	strExpr struct {
		p pos
		s string
	}
	boolExpr struct {
		p pos
		b bool
	}
	// applyExpr applies a named operator, built-in or user-defined, to
	// arguments. Infix and prefix operators parse to applyExpr too, so
	// that the evaluator has one path for all of them.
	applyExpr struct {
		p    pos
		name string
		args []expr
	}
	// junctionExpr is a conjunction or disjunction, whether written
	// infix or as a bulleted list.
	junctionExpr struct {
		p     pos
		and   bool
		items []expr
	}
	primeExpr struct {
		p pos
		x expr
	}
	unchangedExpr struct {
		p pos
		x expr
	}
	quantExpr struct {
		p      pos
		forall bool
		binds  []binding
		body   expr
	}
	chooseExpr struct {
		p    pos
		bind binding
		body expr
	}
	ifExpr struct {
		p               pos
		cond, then, els expr
	}
	caseExpr struct {
		p     pos
		arms  []caseArm
		other expr // nil if there is no OTHER arm
	}
	letExpr struct {
		p    pos
		defs []*opDef
		body expr
	}
	// setEnumExpr is {a, b, c}.
	setEnumExpr struct {
		p     pos
		elems []expr
	}
	// setFilterExpr is {x \in S : p}.
	setFilterExpr struct {
		p    pos
		bind binding
		pred expr
	}
	// setMapExpr is {e : x \in S, ...}.
	setMapExpr struct {
		p     pos
		elem  expr
		binds []binding
	}
	tupleExpr struct {
		p     pos
		elems []expr
	}
	// fcnExpr is [x \in S |-> e].
	fcnExpr struct {
		p     pos
		binds []binding
		body  expr
	}
	// fcnSetExpr is [S -> T].
	fcnSetExpr struct {
		p          pos
		dom, codom expr
	}
	recordExpr struct {
		p      pos
		fields []string
		vals   []expr
	}
	// recordSetExpr is [a : S, b : T].
	recordSetExpr struct {
		p      pos
		fields []string
		sets   []expr
	}
	// appExpr is function application f[x] or f[x, y].
	appExpr struct {
		p    pos
		fcn  expr
		args []expr
	}
	// dotExpr is record field selection r.a.
	dotExpr struct {
		p     pos
		x     expr
		field string
	}
	exceptExpr struct {
		p       pos
		fcn     expr
		updates []exceptUpdate
	}
	// atExpr is @ inside an EXCEPT update: the old value.
	atExpr struct {
		p pos
	}
	// badExpr is a definition body that failed to parse.
	badExpr struct {
		p   pos
		err error
	}
	// timesExpr is A \X B \X C, which is one n-ary operator, not a
	// nested binary one.
	timesExpr struct {
		p    pos
		sets []expr
	}
)

// A binding is the x \in S (or <<x, y>> \in S) of a quantifier.
// names has one element unless the binding is a tuple.
type binding struct {
	names []string
	tuple bool
	set   expr
}

type caseArm struct {
	cond, val expr
}

// An exceptUpdate is one ![a][b].c = v. Each path element is either an
// index list (for [a] or [a, b]) or a field name (for .c).
type exceptUpdate struct {
	path []exceptPath
	val  expr
}

type exceptPath struct {
	args  []expr // nil for a field
	field string
}

// An opDef is an operator definition, Op(a, b) == e, or a function
// definition, f[x \in S] == e.
type opDef struct {
	p      pos
	name   string
	params []param
	body   expr
	// binds is set for a function definition f[x \in S] == e.
	binds []binding
	fcn   bool
	local bool
}

// A param is an operator parameter. arity is nonzero for a parameter
// that is itself an operator, as in F(_, _).
type param struct {
	name  string
	arity int
}

func (e *identExpr) position() pos     { return e.p }
func (e *numExpr) position() pos       { return e.p }
func (e *strExpr) position() pos       { return e.p }
func (e *boolExpr) position() pos      { return e.p }
func (e *applyExpr) position() pos     { return e.p }
func (e *junctionExpr) position() pos  { return e.p }
func (e *primeExpr) position() pos     { return e.p }
func (e *unchangedExpr) position() pos { return e.p }
func (e *quantExpr) position() pos     { return e.p }
func (e *chooseExpr) position() pos    { return e.p }
func (e *ifExpr) position() pos        { return e.p }
func (e *caseExpr) position() pos      { return e.p }
func (e *letExpr) position() pos       { return e.p }
func (e *setEnumExpr) position() pos   { return e.p }
func (e *setFilterExpr) position() pos { return e.p }
func (e *setMapExpr) position() pos    { return e.p }
func (e *tupleExpr) position() pos     { return e.p }
func (e *fcnExpr) position() pos       { return e.p }
func (e *fcnSetExpr) position() pos    { return e.p }
func (e *recordExpr) position() pos    { return e.p }
func (e *recordSetExpr) position() pos { return e.p }
func (e *appExpr) position() pos       { return e.p }
func (e *dotExpr) position() pos       { return e.p }
func (e *exceptExpr) position() pos    { return e.p }
func (e *atExpr) position() pos        { return e.p }
func (e *timesExpr) position() pos     { return e.p }
func (e *badExpr) position() pos       { return e.p }
