package lex

import "testing"

func TestNext_IdentsAndKeywords(t *testing.T) {
	cases := []struct {
		src  string
		kind Kind
		text string
	}{
		{"level", Ident, "level"},
		{"_foo", Ident, "_foo"},
		{"foo123", Ident, "foo123"},
		{"and", KwAnd, "and"},
		{"or", KwOr, "or"},
		{"not", KwNot, "not"},
		{"stats", KwStats, "stats"},
		{"by", KwBy, "by"},
		{"every", KwEvery, "every"},
		{"fields", KwFields, "fields"},
		{"sort", KwSort, "sort"},
		{"asc", KwAsc, "asc"},
		{"desc", KwDesc, "desc"},
		{"limit", KwLimit, "limit"},
		{"count", KwCount, "count"},
		{"count_distinct", KwCountDistinct, "count_distinct"},
		{"sum", KwSum, "sum"},
		{"avg", KwAvg, "avg"},
		{"min", KwMin, "min"},
		{"max", KwMax, "max"},
		{"p50", KwP50, "p50"},
		{"p95", KwP95, "p95"},
		{"p99", KwP99, "p99"},
		{"exists", KwExists, "exists"},
		{"true", KwTrue, "true"},
		{"false", KwFalse, "false"},
		{"null", KwNull, "null"},
		{"in", KwIn, "in"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			tok := New(c.src).Next()
			if tok.Kind != c.kind || tok.Text != c.text {
				t.Fatalf("Next(%q) = {%v %q}, want {%v %q}", c.src, tok.Kind, tok.Text, c.kind, c.text)
			}
		})
	}
}

func TestNext_Numbers(t *testing.T) {
	cases := []struct{ src, text string }{
		{"0", "0"},
		{"42", "42"},
		{"-1", "-1"},
		{"+7", "+7"},
		{"3.14", "3.14"},
		{".5", ".5"},
		{"1e10", "1e10"},
		{"1E+10", "1E+10"},
		{"1e-10", "1e-10"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			tok := New(c.src).Next()
			if tok.Kind != Number || tok.Text != c.text {
				t.Fatalf("Next(%q) = {%v %q}, want Number %q", c.src, tok.Kind, tok.Text, c.text)
			}
		})
	}
}

func TestNext_Durations(t *testing.T) {
	cases := []struct{ src, text string }{
		{"30s", "30s"},
		{"5m", "5m"},
		{"1h30m", "1h30m"},
		{"500ms", "500ms"},
		{"2h45m10s", "2h45m10s"},
		{"-1h", "-1h"},
		{"1.5h", "1.5h"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			tok := New(c.src).Next()
			if tok.Kind != Duration || tok.Text != c.text {
				t.Fatalf("Next(%q) = {%v %q}, want Duration %q", c.src, tok.Kind, tok.Text, c.text)
			}
		})
	}
}

func TestNext_Strings(t *testing.T) {
	cases := []struct{ src, want string }{
		{`"hello"`, "hello"},
		{`'hello'`, "hello"},
		{`"a\"b"`, `a"b`},
		{`'a\'b'`, `a'b`},
		{`"line\nbreak"`, "line\nbreak"},
		{`"tab\there"`, "tab\there"},
		{`"back\\slash"`, `back\slash`},
		{`"a\/b"`, "a/b"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			l := New(c.src)
			tok := l.Next()
			if tok.Kind != String {
				t.Fatalf("Next(%q) kind = %v, want String (Err=%v)", c.src, tok.Kind, l.Err)
			}
			if tok.Text != c.want {
				t.Fatalf("Next(%q) text = %q, want %q", c.src, tok.Text, c.want)
			}
		})
	}
}

func TestNext_StringErrors(t *testing.T) {
	t.Run("unterminated", func(t *testing.T) {
		l := New(`"never closes`)
		tok := l.Next()
		if tok.Kind != Illegal || l.Err == nil {
			t.Fatalf("want Illegal + Err, got kind=%v err=%v", tok.Kind, l.Err)
		}
	})
	t.Run("raw newline forbidden", func(t *testing.T) {
		l := New("\"line1\nline2\"")
		tok := l.Next()
		if tok.Kind != Illegal || l.Err == nil {
			t.Fatalf("want Illegal + Err, got kind=%v err=%v", tok.Kind, l.Err)
		}
	})
	t.Run("unknown escape", func(t *testing.T) {
		l := New(`"bad\qescape"`)
		tok := l.Next()
		if tok.Kind != Illegal || l.Err == nil {
			t.Fatalf("want Illegal + Err, got kind=%v err=%v", tok.Kind, l.Err)
		}
	})
}

func TestNext_Operators(t *testing.T) {
	cases := []struct {
		src  string
		kind Kind
	}{
		{"==", Eq}, {"!=", Ne}, {"<", Lt}, {"<=", Le}, {">", Gt}, {">=", Ge},
		{"~", Match}, {"!~", NMatch}, {"|", Pipe}, {",", Comma}, {".", Dot},
		{"(", LParen}, {")", RParen}, {"[", LBrack}, {"]", RBrack},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			tok := New(c.src).Next()
			if tok.Kind != c.kind {
				t.Fatalf("Next(%q) kind = %v, want %v", c.src, tok.Kind, c.kind)
			}
		})
	}
}

func TestNext_IllegalOperators(t *testing.T) {
	for _, src := range []string{"=", "!", "@", "#"} {
		t.Run(src, func(t *testing.T) {
			l := New(src)
			tok := l.Next()
			if tok.Kind != Illegal || l.Err == nil {
				t.Fatalf("Next(%q) = kind %v err %v, want Illegal + Err", src, tok.Kind, l.Err)
			}
		})
	}
}

func TestNext_EOF(t *testing.T) {
	l := New("")
	tok := l.Next()
	if tok.Kind != EOF {
		t.Fatalf("Next(\"\") kind = %v, want EOF", tok.Kind)
	}
	// EOF must be stable across repeated calls.
	tok2 := l.Next()
	if tok2.Kind != EOF {
		t.Fatalf("second Next() kind = %v, want EOF", tok2.Kind)
	}
}

func TestNext_PositionTracking(t *testing.T) {
	// Full query across two lines; assert offset/line/col on selected tokens.
	src := "level == \"error\"\n  and ts >= -1h"
	l := New(src)

	tok := l.Next() // "level"
	if tok.Pos != (Pos{Offset: 0, Line: 1, Col: 1}) {
		t.Fatalf("level pos = %+v", tok.Pos)
	}

	tok = l.Next() // "=="
	if tok.Pos.Col != 7 {
		t.Fatalf("== col = %d, want 7", tok.Pos.Col)
	}

	tok = l.Next() // "error" string
	if tok.Kind != String || tok.Pos.Col != 10 {
		t.Fatalf("string tok = %+v", tok)
	}

	tok = l.Next() // "and", start of line 2, indented 2 spaces
	if tok.Pos.Line != 2 || tok.Pos.Col != 3 {
		t.Fatalf("and pos = %+v, want line 2 col 3", tok.Pos)
	}
}

func TestNext_RuneColumnsNotByteOffsets(t *testing.T) {
	// A multibyte identifier-adjacent rune must not desync column counting
	// from byte offsets: "配" is 3 bytes but 1 rune.
	src := `"配" == "x"`
	l := New(src)

	tok := l.Next() // the quoted multibyte string, starts at col 1
	if tok.Kind != String || tok.Pos.Col != 1 {
		t.Fatalf("first tok = %+v, want col 1", tok)
	}
	// The string token is 3 runes ("配" plus its two quotes), then one
	// whitespace rune separates it from "==" — so "==" sits at rune-col 5.
	// In bytes, "配" alone is 3 bytes, so if columns were (wrongly) counted
	// in bytes instead of runes this would instead land on byte-col 7.
	tok = l.Next() // "=="
	if tok.Pos.Col != 5 {
		t.Fatalf("== col = %d, want 5 (rune count, not byte offset)", tok.Pos.Col)
	}
	if tok.Pos.Offset != 6 {
		t.Fatalf("== offset = %d, want 6 (byte offset: 1+3+1+1 for the string token plus the space)", tok.Pos.Offset)
	}
}
