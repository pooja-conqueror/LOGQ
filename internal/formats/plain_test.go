package formats

import "testing"

func TestDecodePlainLine_Basic(t *testing.T) {
	rec := DecodePlainLine([]byte("2026-08-29 12:00:00 something happened"))
	got := rec.Get("msg")
	want := "2026-08-29 12:00:00 something happened"
	if got.S != want {
		t.Fatalf("msg = %q, want %q", got.S, want)
	}
	if rec.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 (only .msg)", rec.Len())
	}
}

func TestDecodePlainLine_Empty(t *testing.T) {
	rec := DecodePlainLine([]byte(""))
	if got := rec.Get("msg"); got.S != "" {
		t.Fatalf("msg = %q, want empty", got.S)
	}
}

func TestDecodePlainLine_JSONLookingContentIsNotParsed(t *testing.T) {
	// Plain never parses — even JSON-shaped text is wrapped verbatim, not
	// interpreted. rec.Len() == 1 alone already proves no "level" field
	// was extracted from it; DecodePlainLine only ever sets "msg".
	line := `{"level":"error"}`
	rec := DecodePlainLine([]byte(line))
	if got := rec.Get("msg"); got.S != line {
		t.Fatalf("msg = %q, want the raw line unparsed", got.S)
	}
	if rec.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 — no fields extracted from the JSON-looking text", rec.Len())
	}
}

func TestDecodePlainLine_NeverFails(t *testing.T) {
	// There is no malformed-input concept for plain — confirm a handful of
	// adversarial byte sequences all just wrap cleanly.
	for _, line := range []string{"", "\x00\x01", `unterminated "quote`, "trailing\\"} {
		rec := DecodePlainLine([]byte(line))
		if rec.Get("msg").S != line {
			t.Fatalf("DecodePlainLine(%q) did not preserve the line verbatim", line)
		}
	}
}
