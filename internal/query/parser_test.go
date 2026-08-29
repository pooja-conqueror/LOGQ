package query

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, src string) Expr {
	t.Helper()
	e, err := ParseFilterExpr(src)
	if err != nil {
		t.Fatalf("ParseFilterExpr(%q) error = %v", src, err)
	}
	return e
}

func mustFail(t *testing.T, src string) *ParseError {
	t.Helper()
	e, err := ParseFilterExpr(src)
	if err == nil {
		t.Fatalf("ParseFilterExpr(%q) = %#v, want error", src, e)
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("ParseFilterExpr(%q) error type = %T, want *ParseError", src, err)
	}
	return pe
}

func TestParse_SimpleComparisons(t *testing.T) {
	ops := map[string]CmpOp{
		"==": CmpEq, "!=": CmpNe, "<": CmpLt, "<=": CmpLe, ">": CmpGt, ">=": CmpGe,
	}
	for opText, want := range ops {
		t.Run(opText, func(t *testing.T) {
			e := mustParse(t, `level `+opText+` "error"`)
			cmp, ok := e.(*Cmp)
			if !ok {
				t.Fatalf("got %#v, want *Cmp", e)
			}
			if cmp.Op != want {
				t.Fatalf("Op = %v, want %v", cmp.Op, want)
			}
			path, ok := cmp.L.(*PathRef)
			if !ok || len(path.Segs) != 1 || path.Segs[0].Ident != "level" {
				t.Fatalf("L = %#v, want path 'level'", cmp.L)
			}
			lit, ok := cmp.R.(*Lit)
			if !ok || lit.Kind != LitString || lit.Text != "error" {
				t.Fatalf("R = %#v, want string literal 'error'", cmp.R)
			}
		})
	}
}

func TestParse_LiteralKinds(t *testing.T) {
	cases := []struct {
		src  string
		kind LitKind
	}{
		{`msg == "hello"`, LitString},
		{`status == 500`, LitNumber},
		{`ts >= -1h`, LitDuration},
		{`ok == true`, LitBool},
		{`ok == false`, LitBool},
		{`x == null`, LitNull},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			e := mustParse(t, c.src)
			cmp := e.(*Cmp)
			lit, ok := cmp.R.(*Lit)
			if !ok || lit.Kind != c.kind {
				t.Fatalf("R = %#v, want Lit kind %v", cmp.R, c.kind)
			}
		})
	}
}

func TestParse_Precedence(t *testing.T) {
	// "or" binds loosest, so this must parse as (a and b) or (c and d).
	e := mustParse(t, `a == 1 and b == 2 or c == 3 and d == 4`)
	top, ok := e.(*Filter)
	if !ok || top.Op != OpOr {
		t.Fatalf("top = %#v, want top-level OpOr", e)
	}
	left, ok := top.L.(*Filter)
	if !ok || left.Op != OpAnd {
		t.Fatalf("top.L = %#v, want OpAnd", top.L)
	}
	right, ok := top.R.(*Filter)
	if !ok || right.Op != OpAnd {
		t.Fatalf("top.R = %#v, want OpAnd", top.R)
	}
}

func TestParse_NotBindsTighterThanComparison(t *testing.T) {
	// "not level == \"x\"" must bind as "not (level == \"x\")".
	e := mustParse(t, `not level == "x"`)
	not, ok := e.(*Not)
	if !ok {
		t.Fatalf("got %#v, want *Not", e)
	}
	if _, ok := not.Child.(*Cmp); !ok {
		t.Fatalf("Not.Child = %#v, want *Cmp", not.Child)
	}
}

func TestParse_Parens(t *testing.T) {
	e := mustParse(t, `(a == 1 or b == 2) and c == 3`)
	top, ok := e.(*Filter)
	if !ok || top.Op != OpAnd {
		t.Fatalf("top = %#v, want OpAnd", e)
	}
	inner, ok := top.L.(*Filter)
	if !ok || inner.Op != OpOr {
		t.Fatalf("top.L = %#v, want the parenthesized OpOr", top.L)
	}
}

func TestParse_ParenDepthLimit(t *testing.T) {
	src := strings.Repeat("(", 101) + `a == 1` + strings.Repeat(")", 101)
	pe := mustFail(t, src)
	if !strings.Contains(pe.Msg, "nesting exceeds limit") {
		t.Fatalf("Msg = %q, want a nesting-limit error", pe.Msg)
	}
}

func TestParse_RegexMatch(t *testing.T) {
	e := mustParse(t, `msg ~ "auth failed"`)
	re, ok := e.(*Regex)
	if !ok || re.Neg || re.Pattern != "auth failed" {
		t.Fatalf("got %#v", e)
	}

	e = mustParse(t, `msg !~ "auth failed"`)
	re, ok = e.(*Regex)
	if !ok || !re.Neg {
		t.Fatalf("got %#v, want negated Regex", e)
	}
}

func TestParse_RegexRequiresStringRHS(t *testing.T) {
	pe := mustFail(t, `msg ~ 123`)
	if !strings.Contains(pe.Msg, "string pattern") {
		t.Fatalf("Msg = %q, want a string-pattern error", pe.Msg)
	}
}

func TestParse_ExistsCall(t *testing.T) {
	e := mustParse(t, `exists(url.path)`)
	ex, ok := e.(*Exists)
	if !ok {
		t.Fatalf("got %#v, want *Exists", e)
	}
	if len(ex.Path.Segs) != 2 || ex.Path.Segs[0].Ident != "url" || ex.Path.Segs[1].Ident != "path" {
		t.Fatalf("Path.Segs = %#v", ex.Path.Segs)
	}
}

func TestParse_InTest(t *testing.T) {
	e := mustParse(t, `status in [500, 502, 503]`)
	in, ok := e.(*In)
	if !ok {
		t.Fatalf("got %#v, want *In", e)
	}
	if len(in.Set) != 3 {
		t.Fatalf("Set = %#v, want 3 literals", in.Set)
	}
	for i, want := range []string{"500", "502", "503"} {
		if in.Set[i].Text != want {
			t.Fatalf("Set[%d] = %q, want %q", i, in.Set[i].Text, want)
		}
	}
}

func TestParse_InRequiresPathOnLeft(t *testing.T) {
	pe := mustFail(t, `"literal" in [1, 2]`)
	if !strings.Contains(pe.Msg, "field path") {
		t.Fatalf("Msg = %q, want a field-path-required error", pe.Msg)
	}
}

func TestParse_PathForms(t *testing.T) {
	cases := []struct {
		src      string
		wantSegs []string // Ident text for non-index segs; "[N]" for index segs
	}{
		{`url.path == "x"`, []string{"url", "path"}},
		{`a.b.c == "x"`, []string{"a", "b", "c"}},
		{`."http-status" == 200`, []string{"http-status"}},
		{`a."b-c" == "x"`, []string{"a", "b-c"}},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			e := mustParse(t, c.src)
			cmp := e.(*Cmp)
			path := cmp.L.(*PathRef)
			if len(path.Segs) != len(c.wantSegs) {
				t.Fatalf("Segs = %#v, want %d segments", path.Segs, len(c.wantSegs))
			}
			for i, want := range c.wantSegs {
				if path.Segs[i].Ident != want {
					t.Fatalf("Segs[%d].Ident = %q, want %q", i, path.Segs[i].Ident, want)
				}
			}
		})
	}
}

func TestParse_ArrayIndex(t *testing.T) {
	// items[0].name is three segments: ident "items", index 0, ident "name".
	e := mustParse(t, `items[0].name == "x"`)
	cmp := e.(*Cmp)
	path := cmp.L.(*PathRef)
	if len(path.Segs) != 3 {
		t.Fatalf("Segs = %#v, want 3 segments", path.Segs)
	}
	if path.Segs[0].IsIndex || path.Segs[0].Ident != "items" {
		t.Fatalf("Segs[0] = %#v, want ident 'items'", path.Segs[0])
	}
	if !path.Segs[1].IsIndex || path.Segs[1].Index != 0 {
		t.Fatalf("Segs[1] = %#v, want index 0", path.Segs[1])
	}
	if path.Segs[2].IsIndex || path.Segs[2].Ident != "name" {
		t.Fatalf("Segs[2] = %#v, want ident 'name'", path.Segs[2])
	}
}

func TestParse_HugeIndexClampsInsteadOfFailing(t *testing.T) {
	// EC-17: "Negative/huge index -> parse ok; resolve MISSING". A huge
	// literal index must not be a parse error.
	_, err := ParseFilterExpr(`a[99999999999999999999] == "x"`)
	if err != nil {
		t.Fatalf("huge index should parse, got error: %v", err)
	}
}

func TestParse_NegativeIndex(t *testing.T) {
	_, err := ParseFilterExpr(`a[-1] == "x"`)
	if err != nil {
		t.Fatalf("negative index should parse, got error: %v", err)
	}
}

func TestParse_BareStringOperandIsLiteralNotPath(t *testing.T) {
	// A bare quoted string in Operand position is always a string literal,
	// never a one-segment quoted path (see parseOperand's doc comment).
	e := mustParse(t, `"foo" == "bar"`)
	cmp := e.(*Cmp)
	if _, ok := cmp.L.(*Lit); !ok {
		t.Fatalf("L = %#v, want a string literal, not a path", cmp.L)
	}
}

func TestParse_Errors_PositionedAndSpecific(t *testing.T) {
	cases := []struct {
		src        string
		wantLine   int
		wantCol    int
		wantSubstr string
	}{
		{`level ==`, 1, 9, "expected a field path or literal"},
		{`level == "error" extra`, 1, 18, "unexpected trailing input"},
		{``, 1, 1, "unexpected end of query"},
		{`level "error"`, 1, 7, "dangling operand"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			pe := mustFail(t, c.src)
			if pe.Pos.Line != c.wantLine || pe.Pos.Col != c.wantCol {
				t.Fatalf("pos = %d:%d, want %d:%d (msg: %s)", pe.Pos.Line, pe.Pos.Col, c.wantLine, c.wantCol, pe.Msg)
			}
			if !strings.Contains(pe.Msg, c.wantSubstr) {
				t.Fatalf("Msg = %q, want substring %q", pe.Msg, c.wantSubstr)
			}
		})
	}
}

func TestParse_ErrorFormat(t *testing.T) {
	pe := mustFail(t, `level ==`)
	got := pe.Error()
	if !strings.HasPrefix(got, "1:9: E-PARSE: ") {
		t.Fatalf("Error() = %q, want it to start with '1:9: E-PARSE: '", got)
	}
}

func TestParse_IllegalLexTokenPropagates(t *testing.T) {
	pe := mustFail(t, `msg == "unterminated`)
	if !strings.Contains(pe.Msg, "unterminated string") {
		t.Fatalf("Msg = %q, want the lexer's unterminated-string message", pe.Msg)
	}
}

func TestParseQuery_BareStagePipeNotYetSupported(t *testing.T) {
	_, err := ParseQuery(`| stats count() by remote_addr`)
	if err == nil {
		t.Fatal("want an error: pipeline stages are not implemented yet")
	}
	if !strings.Contains(err.Error(), "not implemented yet") {
		t.Fatalf("err = %v, want a not-implemented-yet message", err)
	}
}

func TestParseQuery_FilterOnly(t *testing.T) {
	q, err := ParseQuery(`level >= "error"`)
	if err != nil {
		t.Fatalf("ParseQuery error = %v", err)
	}
	if _, ok := q.Filter.(*Cmp); !ok {
		t.Fatalf("Filter = %#v, want *Cmp", q.Filter)
	}
}

// --- FieldsStage / SortStage / LimitStage (commit 23) ---------------------

func TestParseQuery_FieldsStage_SinglePath(t *testing.T) {
	q, err := ParseQuery(`level == "error" | fields msg`)
	if err != nil {
		t.Fatalf("ParseQuery error = %v", err)
	}
	if len(q.Stages) != 1 {
		t.Fatalf("Stages = %#v, want 1 stage", q.Stages)
	}
	fs, ok := q.Stages[0].(*FieldsStage)
	if !ok || len(fs.Paths) != 1 || fs.Paths[0].Segs[0].Ident != "msg" {
		t.Fatalf("Stages[0] = %#v, want FieldsStage{msg}", q.Stages[0])
	}
}

func TestParseQuery_FieldsStage_MultiplePaths(t *testing.T) {
	q, err := ParseQuery(`| fields a, b.c, items[0]`)
	if err != nil {
		t.Fatalf("ParseQuery error = %v", err)
	}
	fs := q.Stages[0].(*FieldsStage)
	if len(fs.Paths) != 3 {
		t.Fatalf("Paths = %#v, want 3", fs.Paths)
	}
	if fs.Paths[0].Segs[0].Ident != "a" {
		t.Fatalf("Paths[0] = %#v", fs.Paths[0])
	}
	if len(fs.Paths[1].Segs) != 2 || fs.Paths[1].Segs[1].Ident != "c" {
		t.Fatalf("Paths[1] = %#v, want b.c", fs.Paths[1])
	}
	if !fs.Paths[2].Segs[1].IsIndex {
		t.Fatalf("Paths[2] = %#v, want an index segment", fs.Paths[2])
	}
}

func TestParseQuery_SortStage_Valid(t *testing.T) {
	cases := []struct {
		src       string
		wantOrder SortOrder
		wantLimit int64
	}{
		{`| sort ts limit 10`, SortAsc, 10}, // default order is asc
		{`| sort ts asc limit 10`, SortAsc, 10},
		{`| sort ts desc limit 5`, SortDesc, 5},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			q, err := ParseQuery(c.src)
			if err != nil {
				t.Fatalf("ParseQuery(%q) error = %v", c.src, err)
			}
			ss, ok := q.Stages[0].(*SortStage)
			if !ok {
				t.Fatalf("Stages[0] = %#v, want *SortStage", q.Stages[0])
			}
			if ss.Order != c.wantOrder {
				t.Fatalf("Order = %v, want %v", ss.Order, c.wantOrder)
			}
			if ss.Limit != c.wantLimit {
				t.Fatalf("Limit = %d, want %d", ss.Limit, c.wantLimit)
			}
			if ss.Path.Segs[0].Ident != "ts" {
				t.Fatalf("Path = %#v, want ts", ss.Path)
			}
		})
	}
}

func TestParseQuery_SortStage_WithoutLimitIsParseTimeError(t *testing.T) {
	// The constant-memory guarantee (S-2): sort without a bound is a
	// grammar violation caught at parse time, never something that slips
	// through to a runtime surprise later.
	_, err := ParseQuery(`| sort ts`)
	if err == nil {
		t.Fatal("ParseQuery(| sort ts) should fail — sort requires 'limit N'")
	}
	pe, ok := err.(*ParseError)
	if !ok {
		t.Fatalf("error type = %T, want *ParseError", err)
	}
	if !strings.Contains(pe.Msg, "requires 'limit N'") {
		t.Fatalf("Msg = %q, want a message about the missing limit", pe.Msg)
	}
}

func TestParseQuery_SortStage_DescWithoutLimitStillErrors(t *testing.T) {
	_, err := ParseQuery(`| sort ts desc`)
	if err == nil {
		t.Fatal("ParseQuery(| sort ts desc) should fail — still no 'limit N'")
	}
}

func TestParseQuery_LimitStage_Valid(t *testing.T) {
	q, err := ParseQuery(`| limit 20`)
	if err != nil {
		t.Fatalf("ParseQuery error = %v", err)
	}
	ls, ok := q.Stages[0].(*LimitStage)
	if !ok || ls.Limit != 20 {
		t.Fatalf("Stages[0] = %#v, want LimitStage{20}", q.Stages[0])
	}
}

func TestParseQuery_LimitStage_ZeroRejected(t *testing.T) {
	// POSINT means >= 1, not >= 0.
	if _, err := ParseQuery(`| limit 0`); err == nil {
		t.Fatal("ParseQuery(| limit 0) should fail — POSINT requires >= 1")
	}
}

func TestParseQuery_SortStage_LimitZeroRejected(t *testing.T) {
	if _, err := ParseQuery(`| sort ts limit 0`); err == nil {
		t.Fatal("ParseQuery(| sort ts limit 0) should fail")
	}
}

func TestParseQuery_LimitStage_NegativeRejected(t *testing.T) {
	// Lexically "-5" tokenizes as a single signed Number, still rejected
	// by the POSINT >= 1 check.
	if _, err := ParseQuery(`| limit -5`); err == nil {
		t.Fatal("ParseQuery(| limit -5) should fail")
	}
}

func TestParseQuery_ChainedStages(t *testing.T) {
	q, err := ParseQuery(`level == "error" | fields msg, status | sort status desc limit 5`)
	if err != nil {
		t.Fatalf("ParseQuery error = %v", err)
	}
	if len(q.Stages) != 2 {
		t.Fatalf("Stages = %#v, want 2 stages", q.Stages)
	}
	if _, ok := q.Stages[0].(*FieldsStage); !ok {
		t.Fatalf("Stages[0] = %#v, want *FieldsStage", q.Stages[0])
	}
	if _, ok := q.Stages[1].(*SortStage); !ok {
		t.Fatalf("Stages[1] = %#v, want *SortStage", q.Stages[1])
	}
}

func TestParseQuery_UnknownStageKeyword(t *testing.T) {
	_, err := ParseQuery(`| bogus stuff`)
	if err == nil {
		t.Fatal("ParseQuery(| bogus stuff) should fail")
	}
	if !strings.Contains(err.Error(), "expected a pipeline stage") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseQuery_StatsAfterFilterStillNotImplemented(t *testing.T) {
	_, err := ParseQuery(`level == "error" | stats count()`)
	if err == nil || !strings.Contains(err.Error(), "not implemented yet") {
		t.Fatalf("err = %v, want a not-implemented-yet message for stats", err)
	}
}

func TestParseQuery_TrailingGarbageAfterStage(t *testing.T) {
	_, err := ParseQuery(`| limit 5 extra`)
	if err == nil {
		t.Fatal("trailing garbage after a valid stage should still fail")
	}
}
