package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/lex"
)

// minEveryDuration and maxEveryDuration implement S-1's range check on
// StatsStage's "every" window: 1ms–365d, inclusive both ends.
var (
	minEveryDuration = time.Millisecond
	maxEveryDuration = 365 * 24 * time.Hour
)

// maxParenDepth bounds recursion for nested parentheses so pathological
// input gets a positioned E-PARSE instead of relying on the runtime's
// (very large, but not infinite) goroutine stack.
const maxParenDepth = 100

// topLevelConnectors and existsCandidates are the current, honest set of
// keywords a "did you mean" suggestion can be offered against. They grow as
// more grammar lands (e.g. stage keywords in Phase 7/8) — never claim a
// suggestion against a keyword the parser doesn't actually accept yet.
var topLevelConnectors = []string{"and", "or"}
var existsCandidates = []string{"exists"}

// ParseError is a positioned E-PARSE, optionally carrying a Levenshtein
// "did you mean" suggestion (see Suggest in suggest.go).
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

// errWithSuggest is errf plus a "did you mean" lookup against candidates,
// for the specific unrecognized-identifier text got.
func (p *parser) errWithSuggest(pos lex.Pos, got string, candidates []string, format string, args ...any) error {
	return &ParseError{
		Pos:     pos,
		Msg:     fmt.Sprintf(format, args...),
		Suggest: Suggest(got, candidates),
	}
}

// ParseQuery parses a full query: an optional FilterExpr followed by zero
// or more "|"-separated pipeline stages — fields, sort, limit, and stats
// are all real grammar as of this commit (stats' actual aggregation
// engine lands later in Phase 8; it parses correctly here regardless).
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

	statsSeen := false
	for p.cur.Kind == lex.Pipe {
		pipePos := p.cur.Pos
		p.advance()
		stage, err := p.parseStage()
		if err != nil {
			return nil, err
		}

		// S-5: once 'stats' has appeared, only 'sort'/'limit' may still
		// follow it — bounded top-K over the aggregate groups it produced
		// (§8.5: "sort <aggcol> desc limit K after stats"), reusing the
		// exact same SortStage/LimitStage machinery an ordinary
		// post-filter record stream uses. 'fields' or a second 'stats'
		// still can't follow: stats remains the terminal AGGREGATION
		// stage (its own group-key columns are the only paths a
		// following sort/limit can meaningfully reference), just not the
		// terminal stage outright. statsSeen (not just "the immediately
		// preceding stage") is what's checked, so this also correctly
		// rejects e.g. `stats ... | sort a desc limit 5 | fields x` — the
		// restriction doesn't lapse just because a sort/limit stage sits
		// between stats and the offending one.
		if statsSeen {
			switch stage.(type) {
			case *SortStage, *LimitStage:
			default:
				return nil, p.errf(pipePos, "'stats' must be the terminal stage, except for a following 'sort'/'limit' (top-K over its groups) — no other stage may follow it")
			}
		}
		if _, ok := stage.(*StatsStage); ok {
			statsSeen = true
		}

		q.Stages = append(q.Stages, stage)
	}

	if p.cur.Kind == lex.Illegal {
		return nil, p.illegalErr()
	}
	if p.cur.Kind != lex.EOF {
		if p.cur.Kind == lex.Ident {
			return nil, p.errWithSuggest(p.cur.Pos, p.cur.Text, topLevelConnectors,
				"unexpected trailing input %q", p.cur.Text)
		}
		return nil, p.errf(p.cur.Pos, "unexpected trailing input %q", p.cur.Text)
	}
	return q, nil
}

// parseStage dispatches on the leading keyword of a "|"-separated stage.
func (p *parser) parseStage() (Stage, error) {
	switch p.cur.Kind {
	case lex.KwFields:
		return p.parseFieldsStage()
	case lex.KwSort:
		return p.parseSortStage()
	case lex.KwLimit:
		return p.parseLimitStage()
	case lex.KwStats:
		return p.parseStatsStage()
	case lex.Illegal:
		return nil, p.illegalErr()
	default:
		return nil, p.errf(p.cur.Pos, "expected a pipeline stage (fields, sort, limit, or stats), got %q", p.cur.Text)
	}
}

// FieldsStage = "fields", Path, { ",", Path } ;
func (p *parser) parseFieldsStage() (*FieldsStage, error) {
	p.advance() // "fields"
	first, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	paths := []*PathRef{first}
	for p.cur.Kind == lex.Comma {
		p.advance()
		next, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		paths = append(paths, next)
	}
	return &FieldsStage{Paths: paths}, nil
}

// SortStage = "sort", Path, [ "asc" | "desc" ], "limit", POSINT ;
// "limit" is not optional here — see SortStage's doc comment.
func (p *parser) parseSortStage() (*SortStage, error) {
	p.advance() // "sort"
	path, err := p.parsePath()
	if err != nil {
		return nil, err
	}

	order := SortAsc
	switch p.cur.Kind {
	case lex.KwAsc:
		p.advance()
	case lex.KwDesc:
		order = SortDesc
		p.advance()
	}

	if p.cur.Kind != lex.KwLimit {
		return nil, p.errf(p.cur.Pos, "'sort' requires 'limit N' — sort without a bound has no constant-memory guarantee, so it isn't allowed")
	}
	p.advance() // "limit"

	n, err := p.parsePosInt()
	if err != nil {
		return nil, err
	}
	return &SortStage{Path: path, Order: order, Limit: n}, nil
}

// LimitStage = "limit", POSINT ;
func (p *parser) parseLimitStage() (*LimitStage, error) {
	p.advance() // "limit"
	n, err := p.parsePosInt()
	if err != nil {
		return nil, err
	}
	return &LimitStage{Limit: n}, nil
}

// parsePosInt parses a POSINT (a positive integer, >= 1) — the shared
// "limit N" tail of both SortStage and LimitStage.
func (p *parser) parsePosInt() (int64, error) {
	if p.cur.Kind != lex.Number {
		return 0, p.errf(p.cur.Pos, "expected a positive integer, got %q", p.cur.Text)
	}
	text, pos := p.cur.Text, p.cur.Pos
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 1 {
		return 0, p.errf(pos, "expected a positive integer (>= 1), got %q", text)
	}
	p.advance()
	return n, nil
}

// StatsStage = "stats", StatFn, { ",", StatFn },
//
//	[ "by", Path, { ",", Path } ], [ "every", DURATION ] ;
//
// The formal grammar text is ambiguous about whether bare "count" takes
// parens at all (one source document writes "count()" as if it were a
// single literal token; another writes bare "count" with no call syntax).
// Every example query in both documents consistently writes "count()",
// so that's the form implemented here — parens required uniformly for
// every stat function, count() simply has an empty argument list.
func (p *parser) parseStatsStage() (*StatsStage, error) {
	p.advance() // "stats"

	first, err := p.parseStatFn()
	if err != nil {
		return nil, err
	}
	fns := []StatFn{first}
	for p.cur.Kind == lex.Comma {
		p.advance()
		next, err := p.parseStatFn()
		if err != nil {
			return nil, err
		}
		fns = append(fns, next)
	}

	// S-6: duplicate stat-function+path pairs are rejected — ambiguous
	// output column names, same idea as S-8's duplicate check for fields.
	seen := map[string]bool{}
	for _, fn := range fns {
		key := statFnKey(fn)
		if seen[key] {
			return nil, p.errf(p.cur.Pos, "duplicate stat function %q in stats stage", key)
		}
		seen[key] = true
	}

	ss := &StatsStage{Fns: fns}

	if p.cur.Kind == lex.KwBy {
		p.advance()
		by, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		ss.By = []*PathRef{by}
		for p.cur.Kind == lex.Comma {
			p.advance()
			next, err := p.parsePath()
			if err != nil {
				return nil, err
			}
			ss.By = append(ss.By, next)
		}
	}

	if p.cur.Kind == lex.KwEvery {
		p.advance()
		if p.cur.Kind != lex.Duration {
			return nil, p.errf(p.cur.Pos, "expected a duration after 'every', got %q", p.cur.Text)
		}
		// S-1: "every" requires a duration between 1ms and 365d,
		// inclusive — a static, compile-time range check, unlike an
		// ordinary Duration Lit elsewhere in the grammar whose parsing is
		// deliberately deferred to the evaluator (§5.3's Lit doc comment).
		durText, durPos := p.cur.Text, p.cur.Pos
		d, err := time.ParseDuration(durText)
		if err != nil {
			return nil, p.errf(durPos, "invalid duration %q after 'every': %v", durText, err)
		}
		if d < minEveryDuration || d > maxEveryDuration {
			return nil, p.errf(durPos, "'every' duration %q must be between 1ms and 365d", durText)
		}
		ss.Every = durText
		p.advance()
	}

	return ss, nil
}

// StatFn = "count", "(", ")"
//
//	| "count_distinct", "(", Path, ")"
//	| ( "sum"|"avg"|"min"|"max" ), "(", Path, ")"
//	| ( "p50"|"p95"|"p99" ), "(", Path, ")" ;
func (p *parser) parseStatFn() (StatFn, error) {
	kind, hasArg, ok := statFnKindFromToken(p.cur.Kind)
	if !ok {
		return StatFn{}, p.errf(p.cur.Pos, "expected a stat function (count, count_distinct, sum, avg, min, max, p50, p95, p99), got %q", p.cur.Text)
	}
	p.advance()

	if p.cur.Kind != lex.LParen {
		return StatFn{}, p.errf(p.cur.Pos, "expected '(' after stat function name")
	}
	p.advance()

	var path *PathRef
	if hasArg {
		pr, err := p.parsePath()
		if err != nil {
			return StatFn{}, err
		}
		path = pr
	}

	if p.cur.Kind != lex.RParen {
		return StatFn{}, p.errf(p.cur.Pos, "expected ')' to close stat function call")
	}
	p.advance()

	return StatFn{Kind: kind, Path: path}, nil
}

func statFnKindFromToken(k lex.Kind) (kind StatFnKind, hasArg bool, ok bool) {
	switch k {
	case lex.KwCount:
		return StatCount, false, true
	case lex.KwCountDistinct:
		return StatCountDistinct, true, true
	case lex.KwSum:
		return StatSum, true, true
	case lex.KwAvg:
		return StatAvg, true, true
	case lex.KwMin:
		return StatMin, true, true
	case lex.KwMax:
		return StatMax, true, true
	case lex.KwP50:
		return StatP50, true, true
	case lex.KwP95:
		return StatP95, true, true
	case lex.KwP99:
		return StatP99, true, true
	default:
		return 0, false, false
	}
}

// statFnKey renders a StatFn to canonical text for S-6 duplicate
// detection — e.g. "count()", "sum(duration_ms)".
func statFnKey(fn StatFn) string {
	name := statFnName(fn.Kind)
	if fn.Path == nil {
		return name + "()"
	}
	return name + "(" + pathText(fn.Path) + ")"
}

func statFnName(k StatFnKind) string {
	switch k {
	case StatCount:
		return "count"
	case StatCountDistinct:
		return "count_distinct"
	case StatSum:
		return "sum"
	case StatAvg:
		return "avg"
	case StatMin:
		return "min"
	case StatMax:
		return "max"
	case StatP50:
		return "p50"
	case StatP95:
		return "p95"
	case StatP99:
		return "p99"
	default:
		return "?"
	}
}

// pathText renders a PathRef to its flat dotted/bracket text form — e.g.
// b.c, items[0]. A small, package-local duplicate of the equivalent logic
// in internal/pipeline/fields.go's pathKey: query can't import pipeline
// (pipeline already imports query), and this is small enough that a
// shared helper isn't worth the awkward dependency direction.
func pathText(p *PathRef) string {
	var sb strings.Builder
	for i, seg := range p.Segs {
		if seg.IsIndex {
			sb.WriteByte('[')
			sb.WriteString(strconv.FormatInt(seg.Index, 10))
			sb.WriteByte(']')
			continue
		}
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(seg.Ident)
	}
	return sb.String()
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
		if p.cur.Kind == lex.Ident {
			return nil, p.errWithSuggest(p.cur.Pos, p.cur.Text, topLevelConnectors,
				"unexpected trailing input %q", p.cur.Text)
		}
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
		// A single bare-identifier PathRef here (nothing else could follow
		// it) is the shape a typo'd keyword takes — e.g. "exsits(x)" lexes
		// as a plain path segment "exsits" followed by "(", which is not a
		// valid path continuation, landing right here.
		if path, ok := operand.(*PathRef); ok && len(path.Segs) == 1 && !path.Segs[0].IsIndex {
			return nil, p.errWithSuggest(p.cur.Pos, path.Segs[0].Ident, existsCandidates,
				"dangling operand; expected a comparison, '~', '!~', or 'in'")
		}
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
	switch {
	case p.cur.Kind == lex.Ident, p.cur.Kind == lex.String:
		text := p.cur.Text
		p.advance()
		return PathSeg{Ident: text}, nil
	case lex.IsKeyword(p.cur.Kind):
		// A keyword-shaped bare word (e.g. "count") is still a valid path
		// segment wherever a path is unambiguously expected. parsePathSeg
		// is only ever reached (via parsePath) from grammar positions
		// that have ALREADY committed to "a path comes next" — fields'
		// targets, sort's target, stats' by-clause and function
		// arguments, exists(...)'s argument, and path continuations after
		// "." or "[". The general filter-expression operand dispatch
		// (parseOperand) gates on lex.Ident/lex.Dot BEFORE ever calling
		// parsePath, so this can't make a bare `count == 5` filter
		// ambiguous with any keyword — that dispatch is untouched. This
		// is what makes a stats output column literally named "count"
		// (StatCount's own bare column name) usable as an ordinary
		// `sort count desc` target (§8.5's top-K over aggregate groups).
		text := p.cur.Text
		p.advance()
		return PathSeg{Ident: text}, nil
	case p.cur.Kind == lex.Illegal:
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
