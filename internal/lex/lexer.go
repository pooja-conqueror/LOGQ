package lex

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LexError describes a lexical error at a specific source position.
type LexError struct {
	Pos Pos
	Msg string
}

func (e *LexError) Error() string { return e.Msg }

// Lexer scans a query string into a stream of Tokens. Col is a rune count
// (not a byte count), so multibyte query text still reports correct
// columns; Offset is a byte offset into the original source.
type Lexer struct {
	src    []rune
	i      int
	offset int
	line   int
	col    int

	// Err is set to the most recent lexical error whenever Next returns an
	// Illegal token. The parser is expected to check it immediately after
	// receiving an Illegal token and turn it into a positioned E-PARSE.
	Err *LexError
}

// New creates a Lexer over the given query text.
func New(src string) *Lexer {
	return &Lexer{src: []rune(src), i: 0, offset: 0, line: 1, col: 1}
}

func (l *Lexer) pos() Pos {
	return Pos{Offset: l.offset, Line: l.line, Col: l.col}
}

func (l *Lexer) peek() rune {
	if l.i >= len(l.src) {
		return 0
	}
	return l.src[l.i]
}

func (l *Lexer) peekAt(off int) rune {
	if l.i+off >= len(l.src) {
		return 0
	}
	return l.src[l.i+off]
}

func (l *Lexer) advance() rune {
	r := l.src[l.i]
	l.i++
	l.offset += utf8.RuneLen(r)
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func isIdentStart(r rune) bool { return r == '_' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool  { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
func isDigit(r rune) bool      { return r >= '0' && r <= '9' }

func (l *Lexer) skipWhitespace() {
	for {
		switch l.peek() {
		case ' ', '\t', '\r', '\n':
			l.advance()
		default:
			return
		}
	}
}

// Next scans and returns the next token. At end of input it returns an EOF
// token (repeatedly, if called again). On a lexical error it returns an
// Illegal token and sets Err.
func (l *Lexer) Next() Token {
	l.skipWhitespace()
	start := l.pos()

	r := l.peek()
	switch {
	case r == 0:
		return Token{Kind: EOF, Pos: start}
	case isIdentStart(r):
		return l.scanIdent(start)
	case isDigit(r):
		return l.scanNumberOrDuration(start)
	case r == '.' && isDigit(l.peekAt(1)):
		return l.scanNumberOrDuration(start)
	case (r == '+' || r == '-') && isDigit(l.peekAt(1)):
		return l.scanNumberOrDuration(start)
	case r == '\'' || r == '"':
		return l.scanString(start, r)
	default:
		return l.scanOperator(start)
	}
}

func (l *Lexer) scanIdent(start Pos) Token {
	var sb strings.Builder
	for isIdentPart(l.peek()) {
		sb.WriteRune(l.advance())
	}
	text := sb.String()
	if kind, ok := keywords[text]; ok {
		return Token{Kind: kind, Text: text, Pos: start}
	}
	return Token{Kind: Ident, Text: text, Pos: start}
}

// scanNumberOrDuration handles both NUMBER and DURATION literals. It scans
// greedily and classifies the result by what follows the numeric part: a
// letter run means a duration unit (raw text handed to time.ParseDuration
// downstream, which also covers compound forms like "1h30m"); an exponent
// (e/E + digits) or nothing means a plain number.
func (l *Lexer) scanNumberOrDuration(start Pos) Token {
	var sb strings.Builder

	if l.peek() == '+' || l.peek() == '-' {
		sb.WriteRune(l.advance())
	}
	for isDigit(l.peek()) {
		sb.WriteRune(l.advance())
	}
	if l.peek() == '.' && (isDigit(l.peekAt(1)) || sb.Len() > 0) {
		sb.WriteRune(l.advance())
		for isDigit(l.peek()) {
			sb.WriteRune(l.advance())
		}
	}

	if r := l.peek(); r == 'e' || r == 'E' {
		hasExp := isDigit(l.peekAt(1)) || ((l.peekAt(1) == '+' || l.peekAt(1) == '-') && isDigit(l.peekAt(2)))
		if hasExp {
			sb.WriteRune(l.advance())
			if l.peek() == '+' || l.peek() == '-' {
				sb.WriteRune(l.advance())
			}
			for isDigit(l.peek()) {
				sb.WriteRune(l.advance())
			}
			return Token{Kind: Number, Text: sb.String(), Pos: start}
		}
	}

	if isLetter(l.peek()) {
		for isLetter(l.peek()) || isDigit(l.peek()) || l.peek() == '.' {
			sb.WriteRune(l.advance())
		}
		return Token{Kind: Duration, Text: sb.String(), Pos: start}
	}

	return Token{Kind: Number, Text: sb.String(), Pos: start}
}

func isLetter(r rune) bool { return unicode.IsLetter(r) }

func (l *Lexer) scanString(start Pos, quote rune) Token {
	l.advance() // opening quote
	var sb strings.Builder
	for {
		r := l.peek()
		switch r {
		case 0:
			l.Err = &LexError{Pos: l.pos(), Msg: "unterminated string literal"}
			return Token{Kind: Illegal, Text: sb.String(), Pos: start}
		case '\n':
			l.Err = &LexError{Pos: l.pos(), Msg: "raw newline in string literal (unterminated)"}
			return Token{Kind: Illegal, Text: sb.String(), Pos: start}
		case quote:
			l.advance()
			return Token{Kind: String, Text: sb.String(), Pos: start}
		case '\\':
			escPos := l.pos()
			l.advance() // backslash
			switch e := l.peek(); e {
			case '\\':
				sb.WriteRune('\\')
			case '/':
				sb.WriteRune('/')
			case '\'':
				sb.WriteRune('\'')
			case '"':
				sb.WriteRune('"')
			case 'n':
				sb.WriteRune('\n')
			case 't':
				sb.WriteRune('\t')
			case 'r':
				sb.WriteRune('\r')
			default:
				l.Err = &LexError{Pos: escPos, Msg: "unknown escape sequence"}
				return Token{Kind: Illegal, Text: sb.String(), Pos: start}
			}
			l.advance() // the escaped character itself
		default:
			sb.WriteRune(l.advance())
		}
	}
}

func (l *Lexer) scanOperator(start Pos) Token {
	r := l.advance()
	switch r {
	case '=':
		if l.peek() == '=' {
			l.advance()
			return Token{Kind: Eq, Text: "==", Pos: start}
		}
		l.Err = &LexError{Pos: start, Msg: "unexpected '='; did you mean '=='?"}
		return Token{Kind: Illegal, Text: "=", Pos: start}
	case '!':
		switch l.peek() {
		case '=':
			l.advance()
			return Token{Kind: Ne, Text: "!=", Pos: start}
		case '~':
			l.advance()
			return Token{Kind: NMatch, Text: "!~", Pos: start}
		}
		l.Err = &LexError{Pos: start, Msg: "unexpected '!'"}
		return Token{Kind: Illegal, Text: "!", Pos: start}
	case '<':
		if l.peek() == '=' {
			l.advance()
			return Token{Kind: Le, Text: "<=", Pos: start}
		}
		return Token{Kind: Lt, Text: "<", Pos: start}
	case '>':
		if l.peek() == '=' {
			l.advance()
			return Token{Kind: Ge, Text: ">=", Pos: start}
		}
		return Token{Kind: Gt, Text: ">", Pos: start}
	case '~':
		return Token{Kind: Match, Text: "~", Pos: start}
	case '|':
		return Token{Kind: Pipe, Text: "|", Pos: start}
	case ',':
		return Token{Kind: Comma, Text: ",", Pos: start}
	case '.':
		return Token{Kind: Dot, Text: ".", Pos: start}
	case '(':
		return Token{Kind: LParen, Text: "(", Pos: start}
	case ')':
		return Token{Kind: RParen, Text: ")", Pos: start}
	case '[':
		return Token{Kind: LBrack, Text: "[", Pos: start}
	case ']':
		return Token{Kind: RBrack, Text: "]", Pos: start}
	default:
		l.Err = &LexError{Pos: start, Msg: "unexpected character " + strconv.QuoteRune(r)}
		return Token{Kind: Illegal, Text: string(r), Pos: start}
	}
}
