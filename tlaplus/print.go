package tlaplus

import (
	"strconv"
	"strings"
)

// exprString returns e in TLA+ syntax, for error messages.
func exprString(e expr) string {
	var b strings.Builder
	writeExpr(&b, e, 0)
	return b.String()
}

// writeExpr writes e, parenthesized if its precedence is below prec.
func writeExpr(b *strings.Builder, e expr, prec int) {
	switch e := e.(type) {
	case *identExpr:
		b.WriteString(e.name)
	case *numExpr:
		b.WriteString(strconv.FormatInt(e.n, 10))
	case *strExpr:
		writeString(b, e.s)
	case *boolExpr:
		writeValue(b, e.b)
	case *applyExpr:
		if p, ok := infixPrec[e.name]; ok && len(e.args) == 2 {
			// Infix operators associate left, except =>.
			lp, rp := p, p+1
			if e.name == "=>" {
				lp, rp = p+1, p
			}
			paren(b, p < prec, func() {
				writeExpr(b, e.args[0], lp)
				if e.name == ".." {
					b.WriteString("..")
				} else {
					b.WriteString(" " + e.name + " ")
				}
				writeExpr(b, e.args[1], rp)
			})
			return
		}
		switch e.name {
		case "~", "-.":
			p := 4
			op := "~"
			if e.name == "-." {
				p, op = 12, "-"
			}
			paren(b, p < prec, func() {
				b.WriteString(op)
				writeExpr(b, e.args[0], p+1)
			})
			return
		case "DOMAIN", "SUBSET", "UNION":
			paren(b, 13 < prec, func() {
				b.WriteString(e.name + " ")
				writeExpr(b, e.args[0], 14)
			})
			return
		}
		b.WriteString(e.name)
		if len(e.args) > 0 {
			b.WriteByte('(')
			writeList(b, e.args)
			b.WriteByte(')')
		}
	case *junctionExpr:
		op := ` \/ `
		if e.and {
			op = ` /\ `
		}
		paren(b, 3 < prec, func() {
			for i, x := range e.items {
				if i > 0 {
					b.WriteString(op)
				}
				writeExpr(b, x, 4)
			}
		})
	case *primeExpr:
		writeExpr(b, e.x, precApply)
		b.WriteByte('\'')
	case *unchangedExpr:
		b.WriteString("UNCHANGED ")
		writeExpr(b, e.x, precApply)
	case *quantExpr:
		paren(b, prec > 0, func() {
			if e.forall {
				b.WriteString(`\A `)
			} else {
				b.WriteString(`\E `)
			}
			writeBindings(b, e.binds)
			b.WriteString(" : ")
			writeExpr(b, e.body, 0)
		})
	case *chooseExpr:
		paren(b, prec > 0, func() {
			b.WriteString("CHOOSE ")
			writeBindings(b, []binding{e.bind})
			b.WriteString(" : ")
			writeExpr(b, e.body, 0)
		})
	case *ifExpr:
		paren(b, prec > 0, func() {
			b.WriteString("IF ")
			writeExpr(b, e.cond, 0)
			b.WriteString(" THEN ")
			writeExpr(b, e.then, 0)
			b.WriteString(" ELSE ")
			writeExpr(b, e.els, 0)
		})
	case *caseExpr:
		b.WriteString("CASE ...")
	case *letExpr:
		b.WriteString("LET ... IN ")
		writeExpr(b, e.body, prec)
	case *setEnumExpr:
		b.WriteByte('{')
		writeList(b, e.elems)
		b.WriteByte('}')
	case *setFilterExpr:
		b.WriteByte('{')
		writeBindings(b, []binding{e.bind})
		b.WriteString(" : ")
		writeExpr(b, e.pred, 0)
		b.WriteByte('}')
	case *setMapExpr:
		b.WriteByte('{')
		writeExpr(b, e.elem, 0)
		b.WriteString(" : ")
		writeBindings(b, e.binds)
		b.WriteByte('}')
	case *tupleExpr:
		b.WriteString("<<")
		writeList(b, e.elems)
		b.WriteString(">>")
	case *fcnExpr:
		b.WriteByte('[')
		writeBindings(b, e.binds)
		b.WriteString(" |-> ")
		writeExpr(b, e.body, 0)
		b.WriteByte(']')
	case *fcnSetExpr:
		b.WriteByte('[')
		writeExpr(b, e.dom, 0)
		b.WriteString(" -> ")
		writeExpr(b, e.codom, 0)
		b.WriteByte(']')
	case *recordExpr:
		b.WriteByte('[')
		for i, f := range e.fields {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(f + " |-> ")
			writeExpr(b, e.vals[i], 0)
		}
		b.WriteByte(']')
	case *recordSetExpr:
		b.WriteByte('[')
		for i, f := range e.fields {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(f + " : ")
			writeExpr(b, e.sets[i], 0)
		}
		b.WriteByte(']')
	case *appExpr:
		writeExpr(b, e.fcn, precApply)
		b.WriteByte('[')
		writeList(b, e.args)
		b.WriteByte(']')
	case *dotExpr:
		writeExpr(b, e.x, precApply)
		b.WriteString("." + e.field)
	case *exceptExpr:
		b.WriteByte('[')
		writeExpr(b, e.fcn, 0)
		b.WriteString(" EXCEPT ")
		for i, u := range e.updates {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteByte('!')
			for _, p := range u.path {
				if p.args == nil {
					b.WriteString("." + p.field)
				} else {
					b.WriteByte('[')
					writeList(b, p.args)
					b.WriteByte(']')
				}
			}
			b.WriteString(" = ")
			writeExpr(b, u.val, 0)
		}
		b.WriteByte(']')
	case *atExpr:
		b.WriteByte('@')
	case *timesExpr:
		paren(b, 13 < prec, func() {
			for i, x := range e.sets {
				if i > 0 {
					b.WriteString(` \X `)
				}
				writeExpr(b, x, 14)
			}
		})
	case *badExpr:
		b.WriteString("<unsupported>")
	}
}

func paren(b *strings.Builder, need bool, f func()) {
	if need {
		b.WriteByte('(')
	}
	f()
	if need {
		b.WriteByte(')')
	}
}

func writeList(b *strings.Builder, list []expr) {
	for i, x := range list {
		if i > 0 {
			b.WriteString(", ")
		}
		writeExpr(b, x, 0)
	}
}

func writeBindings(b *strings.Builder, binds []binding) {
	for i, bd := range binds {
		if i > 0 {
			b.WriteString(", ")
		}
		if bd.tuple {
			b.WriteString("<<" + strings.Join(bd.names, ", ") + ">>")
		} else {
			b.WriteString(bd.names[0])
		}
		b.WriteString(` \in `)
		writeExpr(b, bd.set, 0)
	}
}
