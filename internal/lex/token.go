// Package lex tokenizes logq query strings, tracking a {offset, line, col}
// position on every token so the parser (internal/query) can report exact
// positioned errors — col is a rune count, not a byte offset, so multibyte
// (e.g. CJK) query text still gets correct column numbers.
package lex

// Kind identifies a token's lexical category.
type Kind int

const (
	EOF Kind = iota
	Ident
	String
	Number
	Duration // raw text handed to time.ParseDuration by the parser

	// Operators & punctuation
	Eq     // ==
	Ne     // !=
	Lt     // <
	Le     // <=
	Gt     // >
	Ge     // >=
	Match  // ~
	NMatch // !~
	Pipe   // |
	Comma  // ,
	Dot    // .
	LParen // (
	RParen // )
	LBrack // [
	RBrack // ]

	// Keywords
	KwAnd
	KwOr
	KwNot
	KwStats
	KwBy
	KwEvery
	KwFields
	KwSort
	KwAsc
	KwDesc
	KwLimit
	KwCount
	KwCountDistinct
	KwSum
	KwAvg
	KwMin
	KwMax
	KwP50
	KwP95
	KwP99
	KwExists
	KwTrue
	KwFalse
	KwNull
	KwIn

	Illegal
)

var keywords = map[string]Kind{
	"and":            KwAnd,
	"or":             KwOr,
	"not":            KwNot,
	"stats":          KwStats,
	"by":             KwBy,
	"every":          KwEvery,
	"fields":         KwFields,
	"sort":           KwSort,
	"asc":            KwAsc,
	"desc":           KwDesc,
	"limit":          KwLimit,
	"count":          KwCount,
	"count_distinct": KwCountDistinct,
	"sum":            KwSum,
	"avg":            KwAvg,
	"min":            KwMin,
	"max":            KwMax,
	"p50":            KwP50,
	"p95":            KwP95,
	"p99":            KwP99,
	"exists":         KwExists,
	"true":           KwTrue,
	"false":          KwFalse,
	"null":           KwNull,
	"in":             KwIn,
}

// Pos is a token's position: Offset is a byte offset into the source, Line
// is 1-based, Col is a 1-based rune count within Line (not a byte count).
type Pos struct {
	Offset int
	Line   int
	Col    int
}

// Token is one lexical unit with its source position.
type Token struct {
	Kind Kind
	Text string
	Pos  Pos
}
