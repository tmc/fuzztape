package tlaplus

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// A pos is a position in a module's source. Columns count runes from 1,
// because a junction list's meaning depends on them.
type pos struct {
	line, col int
}

// A tokKind classifies a token.
type tokKind int

const (
	tEOF tokKind = iota
	tIdent
	tNumber
	tString
	tOp      // punctuation and symbolic operators, including \-words such as \in
	tKeyword // reserved words such as IF and EXCEPT
	tDashes  // ---- (a module header's rule or a separator)
	tEnd     // ==== (the end of a module)
)

// A token is one lexical element.
type token struct {
	kind tokKind
	text string
	pos  pos
}

func (t token) String() string {
	switch t.kind {
	case tEOF:
		return "end of file"
	case tString:
		return fmt.Sprintf("%q", t.text)
	}
	return t.text
}

var keywords = map[string]bool{
	"ASSUME": true, "ASSUMPTION": true, "AXIOM": true, "BOOLEAN": true,
	"CASE": true, "CHOOSE": true, "CONSTANT": true, "CONSTANTS": true,
	"DOMAIN": true, "ELSE": true, "ENABLED": true, "EXCEPT": true,
	"EXTENDS": true, "FALSE": true, "IF": true, "IN": true,
	"INSTANCE": true, "LAMBDA": true, "LET": true, "LEMMA": true,
	"LOCAL": true, "MODULE": true, "OTHER": true, "RECURSIVE": true,
	"STRING": true, "SUBSET": true, "THEN": true, "THEOREM": true,
	"TRUE": true, "UNCHANGED": true, "UNION": true, "VARIABLE": true,
	"VARIABLES": true, "WITH": true,
}

// symbols lists the punctuation and operator spellings, longest first
// so that the lexer takes the longest match.
var symbols = []string{
	"<=>", "|->", "...", "::=",
	"==", "=>", "=<", "<=", ">=", "/=", "/\\", "\\/", "<<", ">>", "->",
	"<-", "..", ":>", "@@", "[]", "#", "=", "<", ">", "+", "-", "*",
	"%", "^", "~", "'", "(", ")", "[", "]", "{", "}", ",", ":", ".",
	"!", "@", "_", "|", "&",
}

// backslashOps maps the \word operators to their canonical spellings.
var backslashOps = map[string]string{
	`\in`: `\in`, `\notin`: `\notin`, `\cup`: `\cup`, `\union`: `\cup`,
	`\cap`: `\cap`, `\intersect`: `\cap`, `\subseteq`: `\subseteq`,
	`\div`: `\div`, `\o`: `\o`, `\circ`: `\o`, `\X`: `\X`, `\times`: `\X`,
	`\A`: `\A`, `\E`: `\E`, `\AA`: `\AA`, `\EE`: `\EE`, `\lnot`: `~`,
	`\neg`: `~`, `\land`: `/\`, `\lor`: `\/`, `\equiv`: `<=>`,
	`\leq`: `<=`, `\geq`: `>=`, `\subset`: `\subset`, `\supseteq`: `\supseteq`,
	`\supset`: `\supset`,
}

// lex splits src into tokens. It stops at the first error.
func lex(filename string, src []byte) ([]token, error) {
	l := &lexer{file: filename, src: string(src), line: 1, col: 1}
	var toks []token
	for {
		t, err := l.next()
		if err != nil {
			return nil, err
		}
		toks = append(toks, t)
		if t.kind == tEOF {
			return toks, nil
		}
	}
}

type lexer struct {
	file      string
	src       string
	off       int
	line, col int
}

func (l *lexer) errorf(p pos, format string, args ...any) error {
	return &posError{file: l.file, pos: p, msg: fmt.Sprintf(format, args...)}
}

// advance consumes n bytes, keeping line and column current.
func (l *lexer) advance(n int) {
	for _, r := range l.src[l.off : l.off+n] {
		if r == '\n' {
			l.line++
			l.col = 1
		} else {
			l.col++
		}
	}
	l.off += n
}

func (l *lexer) next() (token, error) {
	if err := l.skipSpace(); err != nil {
		return token{}, err
	}
	p := pos{l.line, l.col}
	if l.off >= len(l.src) {
		return token{kind: tEOF, pos: p}, nil
	}
	rest := l.src[l.off:]
	c := rest[0]
	switch {
	case strings.HasPrefix(rest, "----"):
		n := 0
		for n < len(rest) && rest[n] == '-' {
			n++
		}
		l.advance(n)
		return token{kind: tDashes, text: "----", pos: p}, nil
	case strings.HasPrefix(rest, "===="):
		n := 0
		for n < len(rest) && rest[n] == '=' {
			n++
		}
		l.advance(n)
		return token{kind: tEnd, text: "====", pos: p}, nil
	case c == '"':
		return l.string(p)
	case isDigit(c):
		n := 0
		for n < len(rest) && isDigit(rest[n]) {
			n++
		}
		// An identifier may begin with digits, as in 1st.
		if n < len(rest) && isIdentByte(rest[n]) && !isDigit(rest[n]) {
			return l.ident(p)
		}
		l.advance(n)
		return token{kind: tNumber, text: rest[:n], pos: p}, nil
	case isIdentByte(c):
		return l.ident(p)
	case c == '\\':
		n := 1
		for n < len(rest) && isLetter(rest[n]) {
			n++
		}
		if n > 1 {
			word := rest[:n]
			// An operator this package does not know, such as \prec,
			// is a token of its own, so that a definition using it
			// fails only if something evaluates it.
			op, ok := backslashOps[word]
			if !ok {
				op = word
			}
			l.advance(n)
			return token{kind: tOp, text: op, pos: p}, nil
		}
		if strings.HasPrefix(rest, `\/`) {
			l.advance(2)
			return token{kind: tOp, text: `\/`, pos: p}, nil
		}
		l.advance(1)
		return token{kind: tOp, text: `\`, pos: p}, nil
	}
	for _, s := range symbols {
		if strings.HasPrefix(rest, s) {
			l.advance(len(s))
			text := s
			switch s {
			case "=<":
				text = "<="
			case "/=":
				text = "#"
			}
			return token{kind: tOp, text: text, pos: p}, nil
		}
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return token{}, l.errorf(p, "unexpected character %q", r)
}

func (l *lexer) ident(p pos) (token, error) {
	rest := l.src[l.off:]
	n := 0
	for n < len(rest) && isIdentByte(rest[n]) {
		n++
	}
	word := rest[:n]
	l.advance(n)
	if keywords[word] {
		return token{kind: tKeyword, text: word, pos: p}, nil
	}
	return token{kind: tIdent, text: word, pos: p}, nil
}

func (l *lexer) string(p pos) (token, error) {
	var b strings.Builder
	i := l.off + 1
	for {
		if i >= len(l.src) || l.src[i] == '\n' {
			return token{}, l.errorf(p, "unterminated string")
		}
		c := l.src[i]
		if c == '"' {
			i++
			break
		}
		if c == '\\' && i+1 < len(l.src) {
			switch e := l.src[i+1]; e {
			case '"', '\\':
				b.WriteByte(e)
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case 'f':
				b.WriteByte('\f')
			default:
				return token{}, l.errorf(p, "unknown escape \\%c in string", e)
			}
			i += 2
			continue
		}
		b.WriteByte(c)
		i++
	}
	l.advance(i - l.off)
	return token{kind: tString, text: b.String(), pos: p}, nil
}

// skipSpace skips white space and comments. Block comments nest.
func (l *lexer) skipSpace() error {
	for l.off < len(l.src) {
		rest := l.src[l.off:]
		switch {
		case rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n' || rest[0] == '\r':
			l.advance(1)
		case strings.HasPrefix(rest, `\*`):
			n := strings.IndexByte(rest, '\n')
			if n < 0 {
				n = len(rest)
			}
			l.advance(n)
		case strings.HasPrefix(rest, "(*"):
			p := pos{l.line, l.col}
			depth, i := 0, 0
			for {
				if i >= len(rest) {
					return l.errorf(p, "unterminated comment")
				}
				if strings.HasPrefix(rest[i:], "(*") {
					depth++
					i += 2
				} else if strings.HasPrefix(rest[i:], "*)") {
					depth--
					i += 2
					if depth == 0 {
						break
					}
				} else {
					i++
				}
			}
			l.advance(i)
		default:
			return nil
		}
	}
	return nil
}

func isDigit(c byte) bool  { return '0' <= c && c <= '9' }
func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isIdentByte(c byte) bool {
	return isLetter(c) || isDigit(c) || c == '_'
}

// A posError is an error at a position in a module.
type posError struct {
	file string
	pos  pos
	msg  string
}

func (e *posError) Error() string {
	return fmt.Sprintf("%s:%d:%d: %s", e.file, e.pos.line, e.pos.col, e.msg)
}
