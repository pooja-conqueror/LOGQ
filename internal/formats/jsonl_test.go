package formats

import (
	"errors"
	"strings"
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

func mustDecode(t *testing.T, line string) DecodeResult {
	t.Helper()
	res, err := DecodeLine([]byte(line), DefaultMaxDepth)
	if err != nil {
		t.Fatalf("DecodeLine(%q) error = %v", line, err)
	}
	return res
}

func TestDecodeLine_Basic(t *testing.T) {
	res := mustDecode(t, `{"level":"error","status":500,"ok":true,"note":null}`)
	rec := res.Record

	if got := rec.Get("level"); got.Kind != eval.KindString || got.S != "error" {
		t.Fatalf("level = %+v", got)
	}
	if got := rec.Get("status"); got.Kind != eval.KindNumber || !got.IsInt || got.I != 500 {
		t.Fatalf("status = %+v", got)
	}
	if got := rec.Get("ok"); got.Kind != eval.KindBool || !got.B {
		t.Fatalf("ok = %+v", got)
	}
	if got := rec.Get("note"); got.Kind != eval.KindNull {
		t.Fatalf("note = %+v, want KindNull", got)
	}
	if res.DupKeys != 0 {
		t.Fatalf("DupKeys = %d, want 0", res.DupKeys)
	}
}

func TestDecodeLine_KeyOrderPreserved(t *testing.T) {
	res := mustDecode(t, `{"z":1,"a":2,"m":3}`)
	got := res.Record.Keys()
	want := []string{"z", "a", "m"}
	if len(got) != len(want) {
		t.Fatalf("Keys() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Keys() = %v, want %v (source document order)", got, want)
		}
	}
}

func TestDecodeLine_NestedObjectAndArray(t *testing.T) {
	res := mustDecode(t, `{"url":{"path":"/api/x","method":"GET"},"tags":["a","b",3]}`)
	rec := res.Record

	url := rec.Get("url")
	if url.Kind != eval.KindObject {
		t.Fatalf("url = %+v, want KindObject", url)
	}
	if got := url.Obj.Get("path"); got.S != "/api/x" {
		t.Fatalf("url.path = %+v", got)
	}
	if got := url.Obj.Get("method"); got.S != "GET" {
		t.Fatalf("url.method = %+v", got)
	}

	tags := rec.Get("tags")
	if tags.Kind != eval.KindArray || len(tags.Arr) != 3 {
		t.Fatalf("tags = %+v, want a 3-element array", tags)
	}
	if tags.Arr[0].S != "a" || tags.Arr[1].S != "b" {
		t.Fatalf("tags[0:2] = %+v", tags.Arr[:2])
	}
	if tags.Arr[2].Kind != eval.KindNumber || tags.Arr[2].I != 3 {
		t.Fatalf("tags[2] = %+v, want Number(3)", tags.Arr[2])
	}
}

func TestDecodeLine_DuplicateKeysLastWinsAndCounted(t *testing.T) {
	res := mustDecode(t, `{"a":1,"b":2,"a":99}`)
	if got := res.Record.Get("a"); !got.IsInt || got.I != 99 {
		t.Fatalf("a = %+v, want the last value (99)", got)
	}
	if res.DupKeys != 1 {
		t.Fatalf("DupKeys = %d, want 1", res.DupKeys)
	}
	// Key order must still reflect first appearance, not last.
	keys := res.Record.Keys()
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("Keys() = %v, want [a b] (first-seen order, not re-ordered by the later duplicate)", keys)
	}
}

func TestDecodeLine_DuplicateKeysCountedAtEveryNestingLevel(t *testing.T) {
	res := mustDecode(t, `{"a":1,"inner":{"x":1,"x":2},"a":9}`)
	if res.DupKeys != 2 {
		t.Fatalf("DupKeys = %d, want 2 (one top-level, one nested)", res.DupKeys)
	}
}

func TestDecodeLine_Int64Precision(t *testing.T) {
	// EC-12: int64-max number -> exact, no float64 drift.
	res := mustDecode(t, `{"id":9223372036854775807}`)
	got := res.Record.Get("id")
	if !got.IsInt || got.I != 9223372036854775807 {
		t.Fatalf("id = %+v, want exact int64 max, no float64 drift", got)
	}
}

func TestDecodeLine_FloatFallback(t *testing.T) {
	cases := []struct {
		line string
		want float64
	}{
		{`{"x":3.14}`, 3.14},
		{`{"x":1e2}`, 100}, // exponent form: not int64-literal syntax, falls to float64
		{`{"x":-0.5}`, -0.5},
	}
	for _, c := range cases {
		res := mustDecode(t, c.line)
		got := res.Record.Get("x")
		if got.IsInt || got.F != c.want {
			t.Fatalf("DecodeLine(%q) x = %+v, want float %v", c.line, got, c.want)
		}
	}
}

func TestDecodeLine_NegativeZeroIntegerNormalizesToZero(t *testing.T) {
	res := mustDecode(t, `{"x":-0}`)
	got := res.Record.Get("x")
	if !got.IsInt || got.I != 0 {
		t.Fatalf("x = %+v, want exact Int(0)", got)
	}
}

func TestDecodeLine_UnicodeEscapes(t *testing.T) {
	res := mustDecode(t, `{"msg":"café"}`)
	got := res.Record.Get("msg")
	if got.S != "café" {
		t.Fatalf("msg = %q, want %q", got.S, "café")
	}
}

func TestDecodeLine_EmptyObject(t *testing.T) {
	res := mustDecode(t, `{}`)
	if res.Record.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", res.Record.Len())
	}
}

func TestDecodeLine_NotAnObject(t *testing.T) {
	cases := []string{
		`"just a string"`,
		`42`,
		`[1,2,3]`,
		`true`,
		`null`,
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			_, err := DecodeLine([]byte(line), DefaultMaxDepth)
			if !errors.Is(err, ErrNotAnObject) {
				t.Fatalf("DecodeLine(%q) error = %v, want ErrNotAnObject", line, err)
			}
		})
	}
}

func TestDecodeLine_MalformedSyntax(t *testing.T) {
	cases := []string{
		`{`,
		`{"a":}`,
		`{"a" 1}`,
		`{"a":1,}`, // trailing comma, invalid JSON
		`not json at all`,
		``,
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			if _, err := DecodeLine([]byte(line), DefaultMaxDepth); err == nil {
				t.Fatalf("DecodeLine(%q) = nil error, want a malformed-line error", line)
			}
		})
	}
}

func TestDecodeLine_TrailingGarbageRejected(t *testing.T) {
	_, err := DecodeLine([]byte(`{"a":1} extra`), DefaultMaxDepth)
	if err == nil {
		t.Fatal("trailing data after the object must be rejected, not silently ignored")
	}
}

func TestDecodeLine_TrailingWhitespaceAllowed(t *testing.T) {
	if _, err := DecodeLine([]byte(`{"a":1}   `), DefaultMaxDepth); err != nil {
		t.Fatalf("trailing whitespace must be tolerated, got error: %v", err)
	}
}

func TestDecodeLine_DepthCap(t *testing.T) {
	// With maxDepth=2: top-level object is depth 1, one nested object is
	// depth 2 (allowed), two levels of nesting is depth 3 (rejected).
	if _, err := DecodeLine([]byte(`{"a":{"b":1}}`), 2); err != nil {
		t.Fatalf("2 levels of nesting under maxDepth=2 should succeed, got: %v", err)
	}
	if _, err := DecodeLine([]byte(`{"a":{"b":{"c":1}}}`), 2); err == nil {
		t.Fatal("3 levels of nesting under maxDepth=2 should be rejected")
	}
}

func TestDecodeLine_DepthCapDefault33Deep(t *testing.T) {
	// EC-10: 10-deep nested access works; 33-deep => malformed, under the
	// spec's default cap of 32.
	deep10 := "{" + strings.Repeat(`"a":{`, 9) + `"a":1` + strings.Repeat("}", 9) + "}"
	if _, err := DecodeLine([]byte(deep10), DefaultMaxDepth); err != nil {
		t.Fatalf("10-deep nesting under the default cap should succeed, got: %v", err)
	}

	deep33 := "{" + strings.Repeat(`"a":{`, 32) + `"a":1` + strings.Repeat("}", 32) + "}"
	if _, err := DecodeLine([]byte(deep33), DefaultMaxDepth); err == nil {
		t.Fatal("33-deep nesting should exceed the default max-depth of 32")
	}
}

func TestDecodeLine_ArrayDepthCountsToo(t *testing.T) {
	if _, err := DecodeLine([]byte(`{"a":[[[1]]]}`), 2); err == nil {
		t.Fatal("nested arrays must count toward the depth cap, not just objects")
	}
}
