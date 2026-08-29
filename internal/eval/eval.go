package eval

import (
	"fmt"
	"regexp"
	"strconv"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/lex"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// CompileError is a positioned E-TYPE — a static/compile-time semantic
// error, distinct from the parser's E-PARSE. Currently the only source is
// an invalid regex pattern (§4 S-4).
type CompileError struct {
	Pos lex.Pos
	Msg string
}

func (e *CompileError) Error() string {
	return fmt.Sprintf("%d:%d: E-TYPE: %s", e.Pos.Line, e.Pos.Col, e.Msg)
}

// CompiledFilter is a query.Expr with every Regex node's pattern
// pre-compiled exactly once (§5.4: "Pattern compiled once at query-compile
// time"). Evaluate walks the original AST directly — this only carries the
// side-table of compiled patterns keyed by AST node identity, so the query
// package's AST stays the single source of syntax truth and eval never
// needs a parallel tree of its own.
type CompiledFilter struct {
	Expr     query.Expr
	patterns map[*query.Regex]*regexp.Regexp
}

// Compile walks expr once, compiling every Regex node's pattern via the
// standard library's regexp package (RE2 class — a linear-time matching
// guarantee, no catastrophic backtracking, a composed stdlib safety
// property rather than a hand-rolled one). A malformed pattern is a
// compile-time E-TYPE, surfaced before any record is ever read — never a
// per-record failure. expr may be nil, meaning "match every record" (the
// bare-stage-pipeline case, e.g. `| stats count() by remote_addr`).
func Compile(expr query.Expr) (*CompiledFilter, error) {
	cf := &CompiledFilter{Expr: expr, patterns: map[*query.Regex]*regexp.Regexp{}}
	if err := cf.compileNode(expr); err != nil {
		return nil, err
	}
	return cf, nil
}

func (cf *CompiledFilter) compileNode(e query.Expr) error {
	switch n := e.(type) {
	case nil:
		return nil
	case *query.Filter:
		if err := cf.compileNode(n.L); err != nil {
			return err
		}
		return cf.compileNode(n.R)
	case *query.Not:
		return cf.compileNode(n.Child)
	case *query.Regex:
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return &CompileError{Pos: n.Pos, Msg: "invalid regex pattern: " + err.Error()}
		}
		cf.patterns[n] = re
		return nil
	default:
		// Cmp, In, Exists, Lit, PathRef: nothing to compile.
		return nil
	}
}

// Eval evaluates the compiled filter against rec, returning whether the
// record matches. now is the query's reference "now" for the Timestamp±
// Duration coercion (§5.3 rule 3) — batch mode passes a value frozen once
// at process start, watch mode re-evaluates it per poll tick (Phase 6/9);
// Eval itself carries no clock of its own. Eval never returns an error —
// per M-2, every pathological operand resolves to false; a filter never
// aborts a run under default flags.
func (cf *CompiledFilter) Eval(rec *Record, now time.Time) bool {
	return cf.evalNode(cf.Expr, rec, now)
}

func (cf *CompiledFilter) evalNode(e query.Expr, rec *Record, now time.Time) bool {
	switch n := e.(type) {
	case nil:
		return true // nil filter matches every record
	case *query.Filter:
		// Go's && / || already short-circuit, matching §5.1 directly.
		if n.Op == query.OpAnd {
			return cf.evalNode(n.L, rec, now) && cf.evalNode(n.R, rec, now)
		}
		return cf.evalNode(n.L, rec, now) || cf.evalNode(n.R, rec, now)
	case *query.Not:
		return !cf.evalNode(n.Child, rec, now)
	case *query.Cmp:
		return cf.evalCmp(n, rec, now)
	case *query.Regex:
		return cf.evalRegex(n, rec)
	case *query.In:
		return cf.evalIn(n, rec)
	case *query.Exists:
		return rec.Resolve(n.Path).Kind != KindMissing
	default:
		return false
	}
}

func (cf *CompiledFilter) evalOperand(e query.Expr, rec *Record) Value {
	switch n := e.(type) {
	case *query.PathRef:
		return rec.Resolve(n)
	case *query.Lit:
		return literalValue(n)
	default:
		return Missing
	}
}

// literalValue converts a parsed query.Lit into an eval.Value. now is not
// needed here: the query language has no timestamp literal syntax at all
// (LitKind covers String/Number/Duration/Bool/Null only) — a Timestamp
// Value only ever originates from a record's own resolved `ts` field
// (Phase 6), never from literal query text.
func literalValue(lit *query.Lit) Value {
	switch lit.Kind {
	case query.LitString:
		return Str(lit.Text)
	case query.LitNumber:
		return numberFromText(lit.Text)
	case query.LitDuration:
		d, err := time.ParseDuration(lit.Text)
		if err != nil {
			// Should be unreachable — the lexer only tokenizes digit+letter
			// shapes as Duration — but a literal that somehow fails to
			// parse is treated as MISSING (M-2's blanket rule), never a
			// crash or a hard error mid-stream.
			return Missing
		}
		return DurationVal(d)
	case query.LitBool:
		return Bool(lit.Bool)
	case query.LitNull:
		return Null
	default:
		return Missing
	}
}

func numberFromText(text string) Value {
	if i, err := strconv.ParseInt(text, 10, 64); err == nil {
		return Int(i)
	}
	if f, err := strconv.ParseFloat(text, 64); err == nil {
		return Float(f)
	}
	return Missing
}

func (cf *CompiledFilter) evalCmp(n *query.Cmp, rec *Record, now time.Time) bool {
	l := cf.evalOperand(n.L, rec)
	r := cf.evalOperand(n.R, rec)

	if l.Kind == KindMissing || r.Kind == KindMissing {
		return false // M-2
	}

	switch n.Op {
	case query.CmpEq:
		return cf.compareEqual(l, r, now)
	case query.CmpNe:
		return !cf.compareEqual(l, r, now)
	default:
		return cf.compareOrder(n, l, r, now)
	}
}

// compareEqual implements == (and, negated, !=) per §5.2's truth table
// plus the timestamp<->duration and string<->number coercions from §5.3
// (rules 1 and 3 apply to equality). Rule 2 — level ordinals — is scoped
// to ordering only, per the spec's own example (`level >= "warn"`), and is
// deliberately not applied here: for equality, a level field's own literal
// representation already answers the question directly (a string level
// field equals a string literal by plain string equality; there's no
// scenario the spec describes where equality needs the ordinal detour).
func (cf *CompiledFilter) compareEqual(l, r Value, now time.Time) bool {
	if l.Kind == KindNull || r.Kind == KindNull {
		return l.Kind == KindNull && r.Kind == KindNull
	}
	if l.Kind == r.Kind {
		return DeepEqual(l, r)
	}
	if cl, cr, ok := CoerceTimestampDuration(l, r, now); ok {
		return DeepEqual(cl, cr)
	}
	if nl, lok := CoerceNumeric(l); lok {
		if nr, rok := CoerceNumeric(r); rok {
			return nl == nr
		}
	}
	return false
}

// compareOrder implements <, <=, >, >= per §5.2/§5.3. Level-ordinal
// coercion is checked FIRST, before the same-kind fast path — this
// ordering matters: `level >= "warn"` has a String field on the left and a
// String literal on the right, which are same-Kind, so a naive
// same-kind-first check would silently fall through to byte-wise string
// comparison ("error" >= "warn" alphabetically) instead of the intended
// ordinal comparison (50 >= 40). Checking level-ordinal applicability
// before the same-kind path is what makes the spec's own headline example
// behave correctly.
func (cf *CompiledFilter) compareOrder(n *query.Cmp, l, r Value, now time.Time) bool {
	if ord, ok := cf.tryLevelOrdinal(n, l, r); ok {
		return applyOrder(n.Op, ord)
	}
	if l.Kind == r.Kind {
		return applyOrder(n.Op, Compare(l, r))
	}
	if cl, cr, ok := CoerceTimestampDuration(l, r, now); ok {
		return applyOrder(n.Op, Compare(cl, cr))
	}
	if nl, lok := CoerceNumeric(l); lok {
		if nr, rok := CoerceNumeric(r); rok {
			return applyOrder(n.Op, orderFrom(nl < nr, nl == nr))
		}
	}
	return false // Uncomparable, no coercion fired
}

// tryLevelOrdinal applies §6.2's level-ordinal coercion when either side of
// the comparison is a Path through a recognized level field name. If both
// sides resolve to a known ordinal, the comparison proceeds numerically.
// If not — an unrecognized token — it falls back to byte-wise string
// comparison when both operand *values* happen to be strings (§6.2:
// "Unknown token string [...] falls back to byte-wise string compare,
// documented, surprise-free"; generalized here to apply regardless of
// which side carries the unrecognized token, not just the RHS, since the
// spec's own example is symmetric in spirit).
func (cf *CompiledFilter) tryLevelOrdinal(n *query.Cmp, l, r Value) (Order, bool) {
	if !isLevelPath(n.L) && !isLevelPath(n.R) {
		return 0, false
	}
	lo, lok := LevelOrdinalFromTable(l, nil)
	ro, rok := LevelOrdinalFromTable(r, nil)
	if lok && rok {
		return orderFrom(lo < ro, lo == ro), true
	}
	if l.Kind == KindString && r.Kind == KindString {
		return orderFrom(l.S < r.S, l.S == r.S), true
	}
	return 0, false
}

func isLevelPath(e query.Expr) bool {
	p, ok := e.(*query.PathRef)
	if !ok || len(p.Segs) == 0 {
		return false
	}
	last := p.Segs[len(p.Segs)-1]
	return !last.IsIndex && IsLevelFieldName(last.Ident)
}

func applyOrder(op query.CmpOp, ord Order) bool {
	if ord == Uncomparable {
		return false
	}
	switch op {
	case query.CmpLt:
		return ord == Less
	case query.CmpLe:
		return ord == Less || ord == Equal
	case query.CmpGt:
		return ord == Greater
	case query.CmpGe:
		return ord == Greater || ord == Equal
	default:
		return false
	}
}

// evalRegex implements ~/!~ (§5.4). MISSING is ✗M for both ~ and !~ — the
// negation does NOT flip a MISSING operand into a match; that check
// happens before Neg is ever consulted.
func (cf *CompiledFilter) evalRegex(n *query.Regex, rec *Record) bool {
	re := cf.patterns[n]
	if re == nil {
		return false // defensive only; unreachable after a successful Compile
	}
	v := cf.evalOperand(n.Operand, rec)
	if v.Kind == KindMissing {
		return false
	}
	if v.Kind != KindString {
		return false // matching a non-String operand is a type mismatch, not an error
	}
	matched := re.MatchString(v.S)
	if n.Neg {
		return !matched
	}
	return matched
}

// evalIn implements `in` (§5.5): membership via equality semantics
// including coercion rule #1 (string<->number), so `"502" in [500, 503]`
// matches. The set is never empty (S-7, grammar-enforced), so no special
// casing is needed for that.
func (cf *CompiledFilter) evalIn(n *query.In, rec *Record) bool {
	l := rec.Resolve(n.Path)
	if l.Kind == KindMissing {
		return false
	}
	for _, lit := range n.Set {
		r := literalValue(lit)
		if l.Kind == r.Kind {
			if DeepEqual(l, r) {
				return true
			}
			continue
		}
		if nl, lok := CoerceNumeric(l); lok {
			if nr, rok := CoerceNumeric(r); rok && nl == nr {
				return true
			}
		}
	}
	return false
}
