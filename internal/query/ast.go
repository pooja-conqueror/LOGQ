// Package query implements logq's query language: a hand-written
// recursive-descent parser producing a small, closed AST, exactly per the
// frozen grammar in the project spec. No parser-generator, no parsing
// library — Track B's "implements the format by hand" criterion applies to
// the query language itself, not just the log formats it reads.
package query

import "github.com/pooja-conqueror/LOGQ/internal/lex"

// Expr is implemented by every filter-expression AST node.
type Expr interface{ exprNode() }

// LogicOp distinguishes the two FilterExpr combinators.
type LogicOp int

const (
	OpAnd LogicOp = iota
	OpOr
)

// Filter is a binary "and"/"or" combination of two sub-expressions.
type Filter struct {
	Op   LogicOp
	L, R Expr
}

// Not negates its child expression.
type Not struct {
	Child Expr
}

// CmpOp is one of ==, !=, <, <=, >, >=.
type CmpOp int

const (
	CmpEq CmpOp = iota
	CmpNe
	CmpLt
	CmpLe
	CmpGt
	CmpGe
)

// Cmp compares two operands, each a *PathRef or a *Lit.
type Cmp struct {
	Op   CmpOp
	L, R Expr
	Pos  lex.Pos // position of the operator, for eval-time diagnostics
}

// Regex is a `~`/`!~` match of an operand against a string pattern. Per the
// grammar the RHS is always a string literal, never a path.
type Regex struct {
	Neg     bool
	Operand Expr
	Pattern string
	Pos     lex.Pos // position of the pattern literal (compile errors anchor here)
}

// In tests Path membership in a literal set.
type In struct {
	Path *PathRef
	Set  []*Lit
}

// Exists tests whether Path resolves to a present (non-MISSING) value.
type Exists struct {
	Path *PathRef
}

// LitKind distinguishes the kinds of literal value.
type LitKind int

const (
	LitString LitKind = iota
	LitNumber
	LitDuration
	LitBool
	LitNull
)

// Lit is a literal value. Numeric/duration text is kept raw here — parsing
// into an actual number or time.Duration happens in the evaluator (Phase 3
// onward), not the parser, so a syntactically valid but semantically odd
// literal (e.g. an out-of-range number) is never a parse-time failure.
type Lit struct {
	Kind LitKind
	Text string // raw source text for String/Number/Duration
	Bool bool   // valid only when Kind == LitBool
	Pos  lex.Pos
}

// PathSeg is one segment of a Path: either a field name (bare or quoted) or
// an array index.
type PathSeg struct {
	IsIndex bool
	Ident   string // valid when !IsIndex
	Index   int64  // valid when IsIndex
}

// PathRef is a field path, e.g. url.path or headers["x-id"][0].
type PathRef struct {
	Segs []PathSeg
	Pos  lex.Pos
}

func (*Filter) exprNode()  {}
func (*Not) exprNode()     {}
func (*Cmp) exprNode()     {}
func (*Regex) exprNode()   {}
func (*In) exprNode()      {}
func (*Exists) exprNode()  {}
func (*Lit) exprNode()     {}
func (*PathRef) exprNode() {}

// Stage is implemented by each pipeline stage kind. FieldsStage,
// SortStage, and LimitStage land here (commit 23); StatsStage lands in
// Phase 8 (commit 28) — until then a "stats" keyword after a "|" is a
// clear "not implemented yet" parse error, not silently ignored.
type Stage interface{ stageNode() }

// FieldsStage projects a record down to just the listed paths.
type FieldsStage struct {
	Paths []*PathRef
}

// SortOrder is asc or desc for a SortStage.
type SortOrder int

const (
	SortAsc SortOrder = iota
	SortDesc
)

// SortStage sorts by Path (asc by default) and keeps only the first Limit
// records. Limit is grammar-mandatory — there is no SortStage value
// without one — which is what makes sort's constant-memory guarantee
// (S-2) a parser-enforced fact, not a runtime convention someone could
// forget to check.
type SortStage struct {
	Path  *PathRef
	Order SortOrder
	Limit int64
}

// LimitStage passes through only the first N records reaching it.
type LimitStage struct {
	Limit int64
}

func (*FieldsStage) stageNode() {}
func (*SortStage) stageNode()   {}
func (*LimitStage) stageNode()  {}

// Query is the top-level parsed query: an optional FilterExpr followed by
// zero or more pipeline stages, applied left to right.
type Query struct {
	Filter Expr // nil means "match every record"
	Stages []Stage
}
