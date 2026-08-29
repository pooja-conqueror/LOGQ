package logfmtx

import "testing"

// FuzzLogfmtRoundTrip proves the round-trip axiom from §10: for every value
// string s, parsing an escaped encoding of s back out must reproduce s
// exactly. renderQuotedForTest (defined in logfmt_test.go, shared here —
// same package, same test binary) escapes exactly the four sequences
// scanQuoted knows how to un-escape (\" \\ \n \t) and passes every other
// byte through untouched, which is also exactly what scanQuoted's own
// default case does on decode — so the property should hold for every
// possible byte sequence, not just "normal" text.
//
// This exists purely to prove the decoder correct against adversarial
// input the seed corpus and hand-written unit tests (commit 16) wouldn't
// think to try — logfmt has no formal writer in this project (only
// jsonl/raw are real output formats), so there is deliberately no
// production encoder function for this to promote into; it stays
// test-only scaffolding on purpose.
func FuzzLogfmtRoundTrip(f *testing.F) {
	seeds := []string{
		"", // empty value
		"plain",
		"has space",
		`has"quote`,     // embedded quote — must not look "unterminated"
		`"""`,           // entirely quote characters
		`has\backslash`, // embedded backslash
		`trailing\`,     // trailing single backslash
		`trailing\\`,    // trailing double backslash
		"has=equals",    // '=' is only special in keys, ordinary in values
		"has\nnewline",
		"has\ttab",
		"\x00\x01\x02", // raw control bytes, unescaped passthrough
		"unicode: café 日本語",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, s string) {
		encoded := renderQuotedForTest(s)
		line := "k=" + encoded

		rec, err := DecodeLine([]byte(line))
		if err != nil {
			t.Fatalf("DecodeLine(%q) (encoded from %q) failed: %v", line, s, err)
		}
		if got := rec.Get("k").S; got != s {
			t.Fatalf("round-trip mismatch: original %q, encoded %q, decoded %q", s, encoded, got)
		}
	})
}
