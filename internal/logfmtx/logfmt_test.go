package logfmtx

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func mustDecode(t *testing.T, line string) *eval.Record {
	t.Helper()
	rec, err := DecodeLine([]byte(line))
	if err != nil {
		t.Fatalf("DecodeLine(%q) error = %v", line, err)
	}
	return rec
}

func TestDecodeLine_Basic(t *testing.T) {
	rec := mustDecode(t, `level=error msg=disk_full status=500`)
	if got := rec.Get("level"); got.S != "error" {
		t.Fatalf("level = %+v", got)
	}
	if got := rec.Get("msg"); got.S != "disk_full" {
		t.Fatalf("msg = %+v", got)
	}
	if got := rec.Get("status"); got.S != "500" {
		// Note: logfmt values decode as plain strings; numeric coercion
		// happens at the evaluator level (Phase 3), not here.
		t.Fatalf("status = %+v, want string \"500\"", got)
	}
}

func TestDecodeLine_KeyOrderPreserved(t *testing.T) {
	rec := mustDecode(t, `z=1 a=2 m=3`)
	got := rec.Keys()
	want := []string{"z", "a", "m"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys() = %v, want %v", got, want)
		}
	}
}

func TestDecodeLine_EmptyValue(t *testing.T) {
	rec := mustDecode(t, `key= next=val`)
	if got := rec.Get("key"); got.S != "" {
		t.Fatalf("key = %+v, want empty string", got)
	}
	if got := rec.Get("next"); got.S != "val" {
		t.Fatalf("next = %+v", got)
	}
}

func TestDecodeLine_EmptyValueAtEOL(t *testing.T) {
	rec := mustDecode(t, `key=`)
	if got := rec.Get("key"); got.S != "" {
		t.Fatalf("key = %+v, want empty string", got)
	}
}

func TestDecodeLine_QuotedValue(t *testing.T) {
	rec := mustDecode(t, `msg="hello world" level=info`)
	if got := rec.Get("msg"); got.S != "hello world" {
		t.Fatalf("msg = %+v", got)
	}
	if got := rec.Get("level"); got.S != "info" {
		t.Fatalf("level = %+v", got)
	}
}

func TestDecodeLine_QuotedEscapes(t *testing.T) {
	cases := []struct {
		line string
		want string
	}{
		{`msg="a\"b"`, `a"b`},
		{`msg="a\\b"`, `a\b`},
		{`msg="line\nbreak"`, "line\nbreak"},
		{`msg="tab\there"`, "tab\there"},
		{`msg=""`, ""},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			rec := mustDecode(t, c.line)
			if got := rec.Get("msg"); got.S != c.want {
				t.Fatalf("msg = %q, want %q", got.S, c.want)
			}
		})
	}
}

func TestDecodeLine_KeyCharsetCoverage(t *testing.T) {
	rec := mustDecode(t, `x-y.z/w_v=1`)
	if got := rec.Get("x-y.z/w_v"); got.S != "1" {
		t.Fatalf("hyphen/dot/slash/underscore key not accepted: %+v", got)
	}
}

func TestDecodeLine_WhitespaceOnlyLineIsEmptyNotError(t *testing.T) {
	rec := mustDecode(t, `   `)
	if rec.Len() != 0 {
		t.Fatalf("Len() = %d, want 0 for a whitespace-only line", rec.Len())
	}
}

func TestDecodeLine_MultipleSpacesBetweenPairs(t *testing.T) {
	rec := mustDecode(t, `a=1    b=2`)
	if rec.Get("a").S != "1" || rec.Get("b").S != "2" {
		t.Fatalf("a=%+v b=%+v", rec.Get("a"), rec.Get("b"))
	}
}

func TestDecodeLine_DuplicateKeyLastWins(t *testing.T) {
	rec := mustDecode(t, `a=1 b=2 a=99`)
	if got := rec.Get("a"); got.S != "99" {
		t.Fatalf("a = %+v, want the last value", got)
	}
	// Same Record semantics as JSON: key order reflects first appearance.
	keys := rec.Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("Keys() = %v, want [a b]", keys)
	}
}

// --- Error cases, each positioned -----------------------------------------

func mustFail(t *testing.T, line string) *LineError {
	t.Helper()
	rec, err := DecodeLine([]byte(line))
	if err == nil {
		t.Fatalf("DecodeLine(%q) = %+v, want an error", line, rec)
	}
	le, ok := err.(*LineError)
	if !ok {
		t.Fatalf("error type = %T, want *LineError", err)
	}
	return le
}

func TestDecodeLine_BareTokenNoEquals(t *testing.T) {
	le := mustFail(t, `justaword`)
	if le.Offset != 0 {
		t.Fatalf("Offset = %d, want 0 (start of the bare token)", le.Offset)
	}
}

func TestDecodeLine_BareTokenFollowedByValidPairStillErrors(t *testing.T) {
	// The whole line is malformed if any part of it is — no partial
	// success on an otherwise-valid trailing pair.
	le := mustFail(t, `bareword key=val`)
	if le.Offset != 0 {
		t.Fatalf("Offset = %d, want 0", le.Offset)
	}
}

func TestDecodeLine_EmptyKey(t *testing.T) {
	le := mustFail(t, `=value`)
	if le.Msg == "" {
		t.Fatal("Msg must not be empty")
	}
}

func TestDecodeLine_InvalidCharacterInKey(t *testing.T) {
	le := mustFail(t, `fo!o=1`)
	if le.Offset != 2 {
		t.Fatalf("Offset = %d, want 2 (position of '!')", le.Offset)
	}
}

func TestDecodeLine_UnterminatedQuote(t *testing.T) {
	le := mustFail(t, `msg="never closes`)
	if le.Offset != 4 {
		t.Fatalf("Offset = %d, want 4 (position of the opening quote)", le.Offset)
	}
}

func TestDecodeLine_UnterminatedQuoteEndingInBackslash(t *testing.T) {
	mustFail(t, `msg="trailing\`)
}

func TestDecodeLine_UnknownEscape(t *testing.T) {
	le := mustFail(t, `msg="bad\qescape"`)
	if le.Offset != 8 {
		t.Fatalf("Offset = %d, want 8 (position of the backslash)", le.Offset)
	}
}

func TestDecodeLine_NoWhitespaceAfterQuotedValue(t *testing.T) {
	le := mustFail(t, `msg="a"extra=1`)
	if le.Offset != 7 {
		t.Fatalf("Offset = %d, want 7 (right after the closing quote)", le.Offset)
	}
}

// --- Round-trip axiom sanity (full property fuzz lands in commit 17) -----

func TestDecodeLine_RoundTripSanity(t *testing.T) {
	// A hand-picked spot check of the round-trip axiom (§10: parse(render(x))
	// == x) ahead of the property-fuzz test landing in the next commit —
	// confirms the escape set is internally consistent both directions.
	cases := []string{"plain", `has space`, `has"quote`, `has\backslash`, "has\ttab", "has\nnewline"}
	for _, want := range cases {
		rendered := renderQuotedForTest(want)
		rec, err := DecodeLine([]byte("k=" + rendered))
		if err != nil {
			t.Fatalf("round-trip decode of %q (rendered %q) failed: %v", want, rendered, err)
		}
		if got := rec.Get("k").S; got != want {
			t.Fatalf("round-trip mismatch: got %q, want %q (rendered form: %q)", got, want, rendered)
		}
	}
}

// renderQuotedForTest is the minimal inverse of scanQuoted, used only to
// sanity-check the round-trip axiom here; the real renderer (used by the
// property-fuzz test in commit 17) is more complete.
func renderQuotedForTest(s string) string {
	out := []byte{'"'}
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\t':
			out = append(out, '\\', 't')
		default:
			out = append(out, c)
		}
	}
	out = append(out, '"')
	return string(out)
}
