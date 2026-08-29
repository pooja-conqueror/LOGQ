package query

import (
	"fmt"
	"strconv"

	"github.com/pooja-conqueror/LOGQ/internal/lex"
)

// maxParenDepth bounds recursion for nested parentheses so pathological
// input gets a positioned E-PARSE instead of relying on the runtime's
// (very large, but not infinite) goroutine stack.
const maxParenDepth = 100

// ParseError is a positioned E-PARSE. Suggest is populated by the
// Levenshtein suggester (commit 7) and is empty until then.
type ParseError struct {
	Pos     lex.Pos
	Msg     string
	Suggest string
}

func (e *ParseError) Error() string {
	if e.Suggest != "" {
		return fmt.Sprintf("%d:%d: E-PARSE: %s; did you mean '%s'?", e.Pos.Line, e.Pos.Col, e.Msg, e.Suggest)
	}
	return fmt.Sprintf("%d:%d: E-PARSE: %s", e.Pos.Line, e.Pos.Col, e.Msg)
}

type parser struct {
	lx         *lex.Lexer
	cur        lex.Token
	parenDepth int
}

func newParser(src string) *parser {
	p := &parser{lx: lex.New(src)}
	p.advance()
	return p
}

func (p *parser) advance() {
	p.cur = p.lx.Next()
}

// illegalErr turns a lexer-level Illegal token into a ParseError, using the
// lexer's own diagnostic when available.
func (p *parser) illegalErr() error {
	if p.lx.Err != nil {
		return &ParseError{Pos: p.lx.Err.Pos, Msg: p.lx.Err.Msg}
	}
	return &ParseError{Pos: p.cur.Pos, Msg: "illegal token"}
}

func (p *parser) errf(pos lex.Pos, format string, args ...any) error {
	return &ParseError{Pos: pos, Msg: fmt.Sprintf(format, args...)}
}

// ParseQuery parses a full query: an optional FilterExpr followed by
// pipeline stages. Stage grammar (StatsStage/FieldsStage/SortStage/
// LimitStage) isn't implemented until Phase 7 (commit 23) — a "|" is
// accepted syntactically (so `| stats ...`-only queries don't fail purely
// on shape) but rejected with a clear, honest error rather than silently
// discarded.
func ParseQuery(src string) (*Query, error) {
	p := newParser(src)
	q := &Query{}

	if p.cur.Kind != lex.Pipe && p.cur.Kind != lex.EOF {
		filter, err := p.parseFilterExpr()
		if err != nil {
			return nil, err
		}
		q.Filter = filter
	}

	if p.cur.Kind == lex.Pipe {
		return nil, p.errf(p.cur.Pos, "pipeline stages are not implemented yet")
	}
	if p.cur.Kind == lex.Illegal {
		return nil, p.illegalErr()
	}
	if p.cur.Kind != lex.EOF {
		return nil, p.errf(p.cur.Pos, "unexpected trailing input %q", p.cur.Text)
	}
	return q, nil
}

// ParseFilterExpr parses a bare FilterExpr with nothing else following —
// mainly useful for tests and for tools that only ever want the filter
// half of the grammar.
func ParseFilterExpr(src string) (Expr, error) {
	p := newParser(src)
	expr, err := p.parseFilterExpr()
	if err != nil {
		return nil, err
	}
	if p.cur.Kind == lex.Illegal {
		return nil, p.illegalErr()
	}
	if p.cur.Kind != lex.EOF {
		return nil, p.errf(p.cur.Pos, "unexpected trailing input %q", p.cur.Text)
	}
	return expr, nil
}

// FilterExpr = OrExpr ;
func (p *parser) parseFilterExpr() (Expr, error) { return p.parseOrExpr() }

// OrExpr = AndExpr, { "or", AndExpr } ;
func (p *parser) parseOrExpr() (Expr, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}
	for p.cur.Kind == lex.KwOr {
		p.advance()
		right, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		left = &Filter{Op: OpOr, L: left, R: right}
	}
	return left, nil
}

// AndExpr = NotExpr, { "and", NotExpr } ;
func (p *parser) parseAndExpr() (Expr, error) {
	left, err := p.parseNotExpr()
	if err != nil {
		return nil, err
	}
	for p.cur.Kind == lex.KwAnd {
		p.advance()
		right, err := p.parseNotExpr()
		if err != nil {
			return nil, err
		}
		left = &Filter{Op: OpAnd, L: left, R: right}
	}
	return left, nil
}

// NotExpr = "not", NotExpr | Primary ;
func (p *parser) parseNotExpr() (Expr, error) {
	if p.cur.Kind == lex.KwNot {
		p.advance()
		child, err := p.parseNotExpr()
		if err != nil {
			return nil, err
		}
		return &Not{Child: child}, nil
	}
	return p.parsePrimary()
}

// Primary = "(", FilterExpr, ")" | Comparison | ExistsCall | InTest ;
func (p *parser) parsePrimary() (Expr, error) {
	switch p.cur.Kind {
	case lex.Illegal:
		return nil, p.illegalErr()
	case lex.EOF:
		return nil, p.errf(p.cur.Pos, "unexpected end of query")
	case lex.LParen:
		p.parenDepth++
		if p.parenDepth > maxParenDepth {
			return nil, p.errf(p.cur.Pos, "parenthesis nesting exceeds limit of %d", maxParenDepth)
		}
		p.advance()
		inner, err := p.parseFilterExpr()
		if err != nil {
			return nil, err
		}
		if p.cur.Kind != lex.RParen {
			return nil, p.errf(p.cur.Pos, "expected ')'")
		}
		p.advance()
		p.parenDepth--
		return inner, nil
	case lex.KwExists:
		return p.parseExistsCall()
	default:
		return p.parseComparisonOrTest()
	}
}

// ExistsCall = "exists", "(", Path, ")" ;
func (p *parser) parseExistsCall() (Expr, error) {
	p.advance() // "exists"
	if p.cur.Kind != lex.LParen {
		return nil, p.errf(p.cur.Pos, "expected '(' after 'exists'")
	}
	p.advance()
	path, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	if p.cur.Kind != lex.RParen {
		return nil, p.errf(p.cur.Pos, "expected ')' to close exists(...)")
	}
	p.advance()
	return &Exists{Path: path}, nil
}

// Comparison = Operand, CmpOp, Operand | RegexTest ;
// RegexTest  = Operand, ( "~" | "!~" ), STRING ;
// InTest     = Path, "in", "[", Literal, { ",", Literal }, "]" ;
// All three share an Operand prefix, so parse the operand once and branch
// on what follows.
func (p *parser) parseComparisonOrTest() (Expr, error) {
	operand, err := p.parseOperand()
	if err != nil {
		return nil, err
	}

	switch p.cur.Kind {
	case lex.Eq, lex.Ne, lex.Lt, lex.Le, lex.Gt, lex.Ge:
		op := cmpOpFromKind(p.cur.Kind)
		opPos := p.cur.Pos
		p.advance()
		rhs, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		return &Cmp{Op: op, L: operand, R: rhs, Pos: opPos}, nil

	case lex.Match, lex.NMatch:
		neg := p.cur.Kind == lex.NMatch
		p.advance()
		if p.cur.Kind != lex.String {
			return nil, p.errf(p.cur.Pos, "expected a string pattern after '~'/'!~'")
		}
		pattern := p.cur.Text
		patPos := p.cur.Pos
		p.advance()
		return &Regex{Neg: neg, Operand: operand, Pattern: pattern, Pos: patPos}, nil

	case lex.KwIn:
		pathRef, ok := operand.(*PathRef)
		if !ok {
			return nil, p.errf(p.cur.Pos, "'in' requires a field path on the left, not a literal")
		}
		p.advance()
		if p.cur.Kind != lex.LBrack {
			return nil, p.errf(p.cur.Pos, "expected '[' after 'in'")
		}
		p.advance()
		set, err := p.parseLiteralList()
		if err != nil {
			return nil, err
		}
		if p.cur.Kind != lex.RBrack {
			return nil, p.errf(p.cur.Pos, "expected ']' to close 'in [...]'")
		}
		p.advance()
		return &In{Path: pathRef, Set: set}, nil

	default:
		return nil, p.errf(p.cur.Pos, "dangling operand; expected a comparison, '~', '!~', or 'in'")
	}
}

// parseLiteralList parses Literal, { ",", Literal } — the contents of an
// `in [...]` set. Requires at least one literal (S-7: empty set impossible).
func (p *parser) parseLiteralList() ([]*Lit, error) {
	first, err := p.parseLiteral()
	if err != nil {
		return nil, err
	}
	lits := []*Lit{first}
	for p.cur.Kind == lex.Comma {
		p.advance()
		next, err := p.parseLiteral()
		if err != nil {
			return nil, err
		}
		lits = append(lits, next)
	}
	return lits, nil
}

// Operand = Path | Literal ;
//
// A bare String token here is always a string Literal, not a quoted Path
// segment — the quoted-segment form of a Path is reachable only via a
// leading "." (see parsePath), so `"foo" == "bar"` unambiguously compares
// two string literals rather than reading a field named foo. This is a
// deliberate disambiguation of the grammar's Path/Literal overlap, not an
// accident: real queries write field paths as bare identifiers.
func (p *parser) parseOperand() (Expr, error) {
	switch p.cur.Kind {
	case lex.String, lex.Number, lex.Duration, lex.KwTrue, lex.KwFalse, lex.KwNull:
		return p.parseLiteral()
	case lex.Ident, lex.Dot:
		return p.parsePath()
	case lex.Illegal:
		return nil, p.illegalErr()
	default:
		return nil, p.errf(p.cur.Pos, "expected a field path or literal, got %q", p.cur.Text)
	}
}

func (p *parser) parseLiteral() (*Lit, error) {
	tok := p.cur
	switch tok.Kind {
	case lex.String:
		p.advance()
		return &Lit{Kind: LitString, Text: tok.Text, Pos: tok.Pos}, nil
	case lex.Number:
		p.advance()
		return &Lit{Kind: LitNumber, Text: tok.Text, Pos: tok.Pos}, nil
	case lex.Duration:
		p.advance()
		return &Lit{Kind: LitDuration, Text: tok.Text, Pos: tok.Pos}, nil
	case lex.KwTrue:
		p.advance()
		return &Lit{Kind: LitBool, Bool: true, Pos: tok.Pos}, nil
	case lex.KwFalse:
		p.advance()
		return &Lit{Kind: LitBool, Bool: false, Pos: tok.Pos}, nil
	case lex.KwNull:
		p.advance()
		return &Lit{Kind: LitNull, Pos: tok.Pos}, nil
	case lex.Illegal:
		return nil, p.illegalErr()
	default:
		return nil, p.errf(tok.Pos, "expected a literal value, got %q", tok.Text)
	}
}

// Path = PathSeg, { ".", PathSeg | "[", INT, "]" } ;
// PathSeg = IDENT | QUOTED ;
//
// A leading "." is accepted (and consumed) before the first segment, e.g.
// `."http-status"` — the documented form for a field name containing
// characters an unquoted IDENT can't carry (like a hyphen).
func (p *parser) parsePath() (*PathRef, error) {
	start := p.cur.Pos
	if p.cur.Kind == lex.Dot {
		p.advance()
	}

	first, err := p.parsePathSeg()
	if err != nil {
		return nil, err
	}
	segs := []PathSeg{first}

	for {
		switch p.cur.Kind {
		case lex.Dot:
			p.advance()
			seg, err := p.parsePathSeg()
			if err != nil {
				return nil, err
			}
			segs = append(segs, seg)
		case lex.LBrack:
			p.advance()
			if p.cur.Kind != lex.Number {
				return nil, p.errf(p.cur.Pos, "expected an integer index inside '[...]'")
			}
			idx, err := strconv.ParseInt(p.cur.Text, 10, 64)
			if err != nil {
				// Syntactically valid but out of int64 range (EC-17: "huge
				// index... parse ok; resolve MISSING") — clamp rather than
				// fail the parse; no real record has this many elements.
				idx = 1<<62 - 1
			}
			p.advance()
			if p.cur.Kind != lex.RBrack {
				return nil, p.errf(p.cur.Pos, "expected ']' to close index")
			}
			p.advance()
			segs = append(segs, PathSeg{IsIndex: true, Index: idx})
		default:
			return &PathRef{Segs: segs, Pos: start}, nil
		}
	}
}

func (p *parser) parsePathSeg() (PathSeg, error) {
	switch p.cur.Kind {
	case lex.Ident:
		text := p.cur.Text
		p.advance()
		return PathSeg{Ident: text}, nil
	case lex.String:
		text := p.cur.Text
		p.advance()
		return PathSeg{Ident: text}, nil
	case lex.Illegal:
		return PathSeg{}, p.illegalErr()
	default:
		return PathSeg{}, p.errf(p.cur.Pos, "expected a path segment (identifier or \"quoted\")")
	}
}

func cmpOpFromKind(k lex.Kind) CmpOp {
	switch k {
	case lex.Eq:
		return CmpEq
	case lex.Ne:
		return CmpNe
	case lex.Lt:
		return CmpLt
	case lex.Le:
		return CmpLe
	case lex.Gt:
		return CmpGt
	default: // lex.Ge
		return CmpGe
	}
}
