package tlaplus

import (
	"fmt"
	"slices"
	"strconv"
)

// A module is a parsed TLA+ module.
type module struct {
	name      string
	extends   []string
	variables []string
	constants []string
	defs      []*opDef
	assumes   []expr
}

type parser struct {
	file string
	toks []token
	i    int
	// limits is the stack of junction-list columns. Inside a bulleted
	// list, every token of an item must lie strictly right of its
	// bullet, so a token at or left of the innermost column ends the
	// item.
	limits []int
}

// bailout carries a parse error up through the recursive descent.
type bailout struct{ err error }

func parseModule(filename string, src []byte) (m *module, err error) {
	toks, err := lex(filename, src)
	if err != nil {
		return nil, err
	}
	p := &parser{file: filename, toks: toks}
	defer func() {
		if r := recover(); r != nil {
			b, ok := r.(bailout)
			if !ok {
				panic(r)
			}
			m, err = nil, b.err
		}
	}()
	return p.module(), nil
}

func (p *parser) errorf(at pos, format string, args ...any) {
	panic(bailout{&posError{file: p.file, pos: at, msg: fmt.Sprintf(format, args...)}})
}

// raw returns the next token, ignoring junction columns.
func (p *parser) raw() token { return p.toks[p.i] }

// peek returns the next token, or an end-of-file token if the innermost
// junction column hides it.
func (p *parser) peek() token {
	t := p.toks[p.i]
	if n := len(p.limits); n > 0 && t.kind != tEOF && t.pos.col <= p.limits[n-1] {
		return token{kind: tEOF, pos: t.pos}
	}
	return t
}

func (p *parser) peekAt(k int) token {
	if p.i+k >= len(p.toks) {
		return p.toks[len(p.toks)-1]
	}
	return p.toks[p.i+k]
}

func (p *parser) next() token {
	t := p.peek()
	if t.kind != tEOF {
		p.i++
	}
	return t
}

// is reports whether the next token is the operator or keyword text.
func (p *parser) is(text string) bool {
	t := p.peek()
	return (t.kind == tOp || t.kind == tKeyword) && t.text == text
}

func (p *parser) accept(text string) bool {
	if p.is(text) {
		p.i++
		return true
	}
	return false
}

func (p *parser) expect(text string) token {
	t := p.peek()
	if !p.is(text) {
		p.errorf(t.pos, "expected %s, found %s", text, t)
	}
	p.i++
	return t
}

func (p *parser) ident() token {
	t := p.peek()
	if t.kind != tIdent {
		p.errorf(t.pos, "expected identifier, found %s", t)
	}
	p.i++
	return t
}

// withoutLimits runs f with the junction columns suspended, for
// constructs whose own delimiters end them, such as a parenthesized
// expression. TLA+ still requires their tokens to lie right of an
// enclosing bullet, but nothing is lost by accepting more.
func (p *parser) withoutLimits(f func()) {
	saved := p.limits
	p.limits = nil
	defer func() { p.limits = saved }()
	f()
}

func (p *parser) module() *module {
	// Text before the module header is ignored.
	for {
		t := p.raw()
		if t.kind == tEOF {
			p.errorf(t.pos, "no MODULE header")
		}
		if t.kind == tDashes && p.peekAt(1).kind == tKeyword && p.peekAt(1).text == "MODULE" {
			break
		}
		p.i++
	}
	p.i += 2
	m := &module{name: p.ident().text}
	if p.raw().kind != tDashes {
		p.errorf(p.raw().pos, "expected ---- after module name")
	}
	p.i++
	for {
		t := p.raw()
		switch {
		case t.kind == tEnd:
			return m
		case t.kind == tEOF:
			p.errorf(t.pos, "missing ==== at end of module")
		case t.kind == tDashes:
			p.i++
		case t.kind == tKeyword:
			p.unit(m, t)
		case t.kind == tIdent:
			m.defs = append(m.defs, p.tolerantDefinition())
		default:
			p.errorf(t.pos, "unexpected %s", t)
		}
	}
}

func (p *parser) unit(m *module, t token) {
	p.i++
	switch t.text {
	case "EXTENDS":
		m.extends = append(m.extends, p.identList()...)
	case "VARIABLE", "VARIABLES":
		m.variables = append(m.variables, p.identList()...)
	case "CONSTANT", "CONSTANTS":
		for {
			id := p.ident()
			if p.peek().text == "(" {
				p.errorf(id.pos, "operator constant %s is not supported", id.text)
			}
			m.constants = append(m.constants, id.text)
			if !p.accept(",") {
				break
			}
		}
	case "ASSUME", "ASSUMPTION", "AXIOM":
		if p.peek().kind == tIdent && p.peekAt(1).text == "==" {
			p.i += 2
		}
		m.assumes = append(m.assumes, p.expr())
	case "THEOREM", "LEMMA":
		// Theorems are usually temporal and are never checked.
		p.skipUnit()
	case "LOCAL":
		if p.raw().kind == tKeyword && p.raw().text == "INSTANCE" {
			p.errorf(p.raw().pos, "INSTANCE is not supported")
		}
		d := p.definition()
		d.local = true
		m.defs = append(m.defs, d)
	case "RECURSIVE":
		// Every definition may be recursive; the declaration adds
		// nothing the evaluator needs.
		for {
			p.ident()
			if p.accept("(") {
				for !p.accept(")") {
					p.next()
				}
			}
			if !p.accept(",") {
				break
			}
		}
	case "INSTANCE":
		p.errorf(t.pos, "INSTANCE is not supported")
	default:
		p.errorf(t.pos, "unexpected %s", t.text)
	}
}

func (p *parser) identList() []string {
	var names []string
	for {
		names = append(names, p.ident().text)
		if !p.accept(",") {
			return names
		}
	}
}

// tolerantDefinition parses a module-level definition. A definition
// whose body is outside the supported subset, such as a temporal
// formula Spec == Init /\ [][Next]_vars, is kept with its parse error
// as its body, so that it fails only if something evaluates it.
func (p *parser) tolerantDefinition() (d *opDef) {
	start := p.i
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		b, ok := r.(bailout)
		if !ok {
			panic(r)
		}
		p.limits = nil
		p.i = start
		name := p.ident()
		p.skipUnit()
		d = &opDef{p: name.pos, name: name.text, body: &badExpr{p: name.pos, err: b.err}}
	}()
	d = p.definition()
	// A body that stops at a token no unit can begin with, such as an
	// operator this parser does not know, is not supported either.
	switch t := p.raw(); t.kind {
	case tEOF, tEnd, tDashes, tIdent, tKeyword:
	default:
		p.errorf(t.pos, "unexpected %s", t)
	}
	return d
}

// skipUnit skips tokens up to the start of the next module-level unit,
// which by convention begins in the first column.
func (p *parser) skipUnit() {
	if p.raw().kind != tEOF {
		p.i++
	}
	for {
		t := p.raw()
		if t.kind == tEOF || t.kind == tEnd || t.pos.col == 1 {
			return
		}
		p.i++
	}
}

// definition parses Op == e, Op(a, F(_)) == e, or f[x \in S] == e.
func (p *parser) definition() *opDef {
	name := p.ident()
	d := &opDef{p: name.pos, name: name.text}
	switch {
	case p.accept("("):
		for {
			pt := p.ident()
			arity := 0
			if p.accept("(") {
				for {
					if t := p.next(); t.text != "_" {
						p.errorf(t.pos, "expected _ in operator parameter, found %s", t)
					}
					arity++
					if !p.accept(",") {
						break
					}
				}
				p.expect(")")
			}
			d.params = append(d.params, param{name: pt.text, arity: arity})
			if !p.accept(",") {
				break
			}
		}
		p.expect(")")
	case p.accept("["):
		d.fcn = true
		d.binds = p.bindings()
		p.expect("]")
	}
	p.expect("==")
	d.body = p.expr()
	return d
}

// bindings parses x \in S, y, z \in T, <<a, b>> \in U.
func (p *parser) bindings() []binding {
	var bs []binding
	for {
		bs = append(bs, p.bindingGroup()...)
		if !p.accept(",") {
			return bs
		}
	}
}

// bindingGroup parses x, y \in S (one group sharing a set) or a tuple
// binding.
func (p *parser) bindingGroup() []binding {
	if p.accept("<<") {
		var names []string
		for {
			names = append(names, p.ident().text)
			if !p.accept(",") {
				break
			}
		}
		p.expect(">>")
		p.expect(`\in`)
		return []binding{{names: names, tuple: true, set: p.expr()}}
	}
	names := []string{p.ident().text}
	for p.is(",") && p.peekAt(1).kind == tIdent && p.peekAt(2).text != "|->" && p.peekAt(2).text != ":" {
		p.i++
		names = append(names, p.ident().text)
	}
	t := p.peek()
	if !p.accept(`\in`) {
		p.errorf(t.pos, "unbounded quantifier: expected \\in, found %s", t)
	}
	set := p.expr()
	bs := make([]binding, len(names))
	for i, n := range names {
		bs[i] = binding{names: []string{n}, set: set}
	}
	return bs
}

// Binding powers of the infix operators, from Specifying Systems.
var infixPrec = map[string]int{
	"=>": 1, "<=>": 2, `/\`: 3, `\/`: 3,
	"=": 5, "#": 5, "<": 5, ">": 5, "<=": 5, ">=": 5,
	`\in`: 5, `\notin`: 5, `\subseteq`: 5, `\subset`: 5, `\supseteq`: 5, `\supset`: 5,
	"@@": 6, ":>": 7, `\cup`: 8, `\cap`: 8, `\`: 8, "..": 9,
	"+": 10, "-": 11, "%": 11, "*": 13, `\div`: 13, `\o`: 13, `\X`: 13, "^": 14,
}

const (
	precPrime = 15
	precApply = 16
)

// expr parses an expression, which extends as far right as it can.
func (p *parser) expr() expr { return p.binary(0) }

func (p *parser) binary(min int) expr {
	left := p.unary()
	for {
		t := p.peek()
		switch {
		case t.kind == tOp && t.text == "'" && precPrime >= min:
			p.i++
			left = &primeExpr{p: t.pos, x: left}
			continue
		case t.kind == tOp && t.text == "[" && precApply >= min:
			p.i++
			var args []expr
			p.withoutLimits(func() {
				args = p.exprList()
				p.expect("]")
			})
			left = &appExpr{p: t.pos, fcn: left, args: args}
			continue
		case t.kind == tOp && t.text == "." && precApply >= min:
			p.i++
			f := p.fieldName()
			left = &dotExpr{p: t.pos, x: left, field: f}
			continue
		}
		prec, ok := infixPrec[t.text]
		if t.kind != tOp || !ok || prec < min {
			return left
		}
		p.i++
		switch t.text {
		case `/\`, `\/`:
			right := p.binary(prec + 1)
			left = junction(t, left, right)
		case "=>":
			right := p.binary(prec) // right-associative
			left = &applyExpr{p: t.pos, name: "=>", args: []expr{left, right}}
		case `\X`:
			sets := []expr{left, p.binary(prec + 1)}
			for p.is(`\X`) {
				p.i++
				sets = append(sets, p.binary(prec+1))
			}
			left = &timesExpr{p: t.pos, sets: sets}
		default:
			right := p.binary(prec + 1)
			left = &applyExpr{p: t.pos, name: t.text, args: []expr{left, right}}
		}
	}
}

// fieldName parses the name after a dot. Record fields may be keywords
// in principle; identifiers suffice here.
func (p *parser) fieldName() string {
	t := p.next()
	if t.kind != tIdent {
		p.errorf(t.pos, "expected field name, found %s", t)
	}
	return t.text
}

func junction(t token, left, right expr) expr {
	and := t.text == `/\`
	if j, ok := left.(*junctionExpr); ok && j.and == and {
		j.items = append(j.items, right)
		return j
	}
	return &junctionExpr{p: t.pos, and: and, items: []expr{left, right}}
}

func (p *parser) unary() expr {
	t := p.peek()
	switch t.kind {
	case tEOF:
		p.errorf(t.pos, "expected expression, found %s", p.raw())
	case tNumber:
		p.i++
		n, err := strconv.ParseInt(t.text, 10, 64)
		if err != nil {
			p.errorf(t.pos, "integer %s out of range", t.text)
		}
		return &numExpr{p: t.pos, n: n}
	case tString:
		p.i++
		return &strExpr{p: t.pos, s: t.text}
	case tIdent:
		p.i++
		if p.raw().kind == tOp && p.raw().text == "(" && p.peek().kind != tEOF {
			p.i++
			var args []expr
			p.withoutLimits(func() {
				args = p.argList()
				p.expect(")")
			})
			return &applyExpr{p: t.pos, name: t.text, args: args}
		}
		return &identExpr{p: t.pos, name: t.text}
	case tKeyword:
		return p.keywordExpr(t)
	case tOp:
		return p.opExpr(t)
	}
	p.errorf(t.pos, "unexpected %s", t)
	return nil
}

// argList parses operator arguments, which may name operators, as in
// SelectSeq(s, IsEven).
func (p *parser) argList() []expr {
	return p.exprList()
}

func (p *parser) exprList() []expr {
	var list []expr
	for {
		list = append(list, p.expr())
		if !p.accept(",") {
			return list
		}
	}
}

func (p *parser) keywordExpr(t token) expr {
	p.i++
	switch t.text {
	case "TRUE", "FALSE":
		return &boolExpr{p: t.pos, b: t.text == "TRUE"}
	case "BOOLEAN", "STRING":
		return &identExpr{p: t.pos, name: t.text}
	case "IF":
		cond := p.expr()
		p.expect("THEN")
		then := p.expr()
		p.expect("ELSE")
		return &ifExpr{p: t.pos, cond: cond, then: then, els: p.expr()}
	case "CASE":
		c := &caseExpr{p: t.pos}
		for {
			if p.accept("OTHER") {
				p.expect("->")
				c.other = p.expr()
				return c
			}
			cond := p.expr()
			p.expect("->")
			c.arms = append(c.arms, caseArm{cond: cond, val: p.expr()})
			if !p.accept("[]") {
				return c
			}
		}
	case "LET":
		l := &letExpr{p: t.pos}
		for !p.is("IN") {
			if p.accept("RECURSIVE") {
				for {
					p.ident()
					if p.accept("(") {
						for !p.accept(")") {
							p.next()
						}
					}
					if !p.accept(",") {
						break
					}
				}
				continue
			}
			if p.peek().kind != tIdent {
				p.errorf(p.peek().pos, "expected definition or IN, found %s", p.peek())
			}
			l.defs = append(l.defs, p.definition())
		}
		p.expect("IN")
		l.body = p.expr()
		return l
	case "CHOOSE":
		bs := p.bindingGroup()
		if len(bs) != 1 {
			p.errorf(t.pos, "CHOOSE binds one identifier")
		}
		p.expect(":")
		return &chooseExpr{p: t.pos, bind: bs[0], body: p.expr()}
	case "UNCHANGED":
		return &unchangedExpr{p: t.pos, x: p.binary(precPrime)}
	case "DOMAIN", "SUBSET", "UNION":
		return &applyExpr{p: t.pos, name: t.text, args: []expr{p.binary(13)}}
	case "ENABLED":
		p.errorf(t.pos, "ENABLED is not supported")
	}
	p.errorf(t.pos, "unexpected %s", t.text)
	return nil
}

func (p *parser) opExpr(t token) expr {
	switch t.text {
	case "(":
		p.i++
		var e expr
		p.withoutLimits(func() {
			e = p.expr()
			p.expect(")")
		})
		return e
	case `/\`, `\/`:
		return p.junctionList(t)
	case "~":
		p.i++
		return &applyExpr{p: t.pos, name: "~", args: []expr{p.binary(4)}}
	case "-":
		p.i++
		return &applyExpr{p: t.pos, name: "-.", args: []expr{p.binary(12)}}
	case `\A`, `\E`:
		p.i++
		bs := p.bindings()
		p.expect(":")
		return &quantExpr{p: t.pos, forall: t.text == `\A`, binds: bs, body: p.expr()}
	case "{":
		p.i++
		var e expr
		p.withoutLimits(func() { e = p.setExpr(t) })
		return e
	case "<<":
		p.i++
		tup := &tupleExpr{p: t.pos}
		p.withoutLimits(func() {
			if !p.accept(">>") {
				tup.elems = p.exprList()
				p.expect(">>")
			}
		})
		return tup
	case "[":
		p.i++
		var e expr
		p.withoutLimits(func() { e = p.bracketExpr(t) })
		return e
	case "@":
		p.i++
		return &atExpr{p: t.pos}
	}
	p.errorf(t.pos, "unexpected %s", t)
	return nil
}

// junctionList parses a bulleted list whose bullets are aligned at t's
// column.
func (p *parser) junctionList(t token) expr {
	col := t.pos.col
	j := &junctionExpr{p: t.pos, and: t.text == `/\`}
	for {
		p.i++ // the bullet
		p.limits = append(p.limits, col)
		item := p.expr()
		p.limits = p.limits[:len(p.limits)-1]
		j.items = append(j.items, item)
		next := p.raw()
		if next.kind != tOp || next.text != t.text || next.pos.col != col {
			break
		}
		if n := len(p.limits); n > 0 && col <= p.limits[n-1] {
			break
		}
	}
	if len(j.items) == 1 {
		return j.items[0]
	}
	return j
}

// setExpr parses the inside of { }.
func (p *parser) setExpr(open token) expr {
	if p.accept("}") {
		return &setEnumExpr{p: open.pos}
	}
	// {<<x, y>> \in S : p} is a filter over a tuple binding.
	if p.is("<<") && p.tupleBindingAhead() {
		b := p.bindingGroup()[0]
		p.expect(":")
		pred := p.expr()
		p.expect("}")
		return &setFilterExpr{p: open.pos, bind: b, pred: pred}
	}
	at := p.peek().pos
	first := p.expr()
	if p.accept(":") {
		// {x \in S : p} is a filter; {e : x \in S} is a map, even
		// when e is (x \in S). The identifier opening the set tells
		// them apart.
		if in, ok := first.(*applyExpr); ok && in.name == `\in` {
			if id, ok := in.args[0].(*identExpr); ok && id.p == at {
				pred := p.expr()
				p.expect("}")
				return &setFilterExpr{p: open.pos, bind: binding{names: []string{id.name}, set: in.args[1]}, pred: pred}
			}
		}
		bs := p.bindings()
		p.expect("}")
		return &setMapExpr{p: open.pos, elem: first, binds: bs}
	}
	elems := []expr{first}
	for p.accept(",") {
		elems = append(elems, p.expr())
	}
	p.expect("}")
	return &setEnumExpr{p: open.pos, elems: elems}
}

// tupleBindingAhead reports whether the tokens ahead read <<a, b>> \in.
func (p *parser) tupleBindingAhead() bool {
	for k := 1; ; k += 2 {
		if p.peekAt(k).kind != tIdent {
			return false
		}
		switch p.peekAt(k + 1).text {
		case ",":
			continue
		case ">>":
			return p.peekAt(k+2).text == `\in`
		}
		return false
	}
}

// field parses a record field name, which must not be one of seen.
func (p *parser) field(seen []string) string {
	id := p.ident()
	if slices.Contains(seen, id.text) {
		p.errorf(id.pos, "field %s repeated", id.text)
	}
	return id.text
}

// bracketExpr parses the inside of [ ]: a record, a set of records, a
// function, a set of functions, or an EXCEPT.
func (p *parser) bracketExpr(open token) expr {
	if p.peek().kind == tIdent && (p.peekAt(1).text == "|->" || p.peekAt(1).text == ":") {
		if p.peekAt(1).text == "|->" {
			r := &recordExpr{p: open.pos}
			for {
				r.fields = append(r.fields, p.field(r.fields))
				p.expect("|->")
				r.vals = append(r.vals, p.expr())
				if !p.accept(",") {
					break
				}
			}
			p.expect("]")
			return r
		}
		r := &recordSetExpr{p: open.pos}
		for {
			r.fields = append(r.fields, p.field(r.fields))
			p.expect(":")
			r.sets = append(r.sets, p.expr())
			if !p.accept(",") {
				break
			}
		}
		p.expect("]")
		return r
	}
	if p.bindingAhead() {
		bs := p.bindings()
		p.expect("|->")
		body := p.expr()
		p.expect("]")
		return &fcnExpr{p: open.pos, binds: bs, body: body}
	}
	first := p.expr()
	switch {
	case p.accept("->"):
		codom := p.expr()
		p.expect("]")
		return &fcnSetExpr{p: open.pos, dom: first, codom: codom}
	case p.accept("EXCEPT"):
		ex := &exceptExpr{p: open.pos, fcn: first}
		for {
			p.expect("!")
			var u exceptUpdate
			for len(u.path) == 0 || !p.is("=") {
				switch {
				case p.accept("["):
					u.path = append(u.path, exceptPath{args: p.exprList()})
					p.expect("]")
				case p.accept("."):
					u.path = append(u.path, exceptPath{field: p.fieldName()})
				default:
					p.errorf(p.peek().pos, "expected [ or . in EXCEPT, found %s", p.peek())
				}
			}
			p.expect("=")
			u.val = p.expr()
			ex.updates = append(ex.updates, u)
			if !p.accept(",") {
				break
			}
		}
		p.expect("]")
		return ex
	}
	p.errorf(p.peek().pos, "expected ->, |->, or EXCEPT, found %s", p.peek())
	return nil
}

// bindingAhead reports whether a function constructor's bindings
// start here: an identifier list or tuple followed by \in, with |->
// somewhere after it before the closing bracket.
func (p *parser) bindingAhead() bool {
	k := 0
	if p.peekAt(0).text == "<<" {
		for k = 1; p.peekAt(k).kind == tIdent && (p.peekAt(k+1).text == "," || p.peekAt(k+1).text == ">>"); k += 2 {
			if p.peekAt(k+1).text == ">>" {
				k += 2
				break
			}
		}
	} else {
		for p.peekAt(k).kind == tIdent && p.peekAt(k+1).text == "," {
			k += 2
		}
		if p.peekAt(k).kind != tIdent {
			return false
		}
		k++
	}
	if p.peekAt(k).text != `\in` {
		return false
	}
	// Scan to the matching ] for a top-level |->.
	depth := 0
	for j := p.i + k; j < len(p.toks); j++ {
		switch t := p.toks[j]; {
		case t.kind == tEOF:
			return false
		case t.kind == tOp && (t.text == "[" || t.text == "(" || t.text == "{" || t.text == "<<"):
			depth++
		case t.kind == tOp && (t.text == "]" || t.text == ")" || t.text == "}" || t.text == ">>"):
			if depth == 0 {
				return false
			}
			depth--
		case t.kind == tOp && t.text == "|->" && depth == 0:
			return true
		}
	}
	return false
}
