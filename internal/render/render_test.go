package render

import (
	"bytes"
	"testing"
	"time"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/formats"
)

func TestRaw_WritesExactBytesPlusNewline(t *testing.T) {
	var buf bytes.Buffer
	line := []byte(`this "line" has \backslashes\ and 'quotes' — untouched`)
	if err := Raw(&buf, line); err != nil {
		t.Fatalf("Raw error = %v", err)
	}
	want := string(line) + "\n"
	if buf.String() != want {
		t.Fatalf("Raw output = %q, want %q (byte-for-byte, no re-escaping)", buf.String(), want)
	}
}

func TestRaw_EmptyLine(t *testing.T) {
	var buf bytes.Buffer
	if err := Raw(&buf, []byte{}); err != nil {
		t.Fatalf("Raw error = %v", err)
	}
	if buf.String() != "\n" {
		t.Fatalf("Raw(empty) = %q, want just a newline", buf.String())
	}
}

func TestRaw_MultipleWritesAreIndependent(t *testing.T) {
	var buf bytes.Buffer
	_ = Raw(&buf, []byte("first"))
	_ = Raw(&buf, []byte("second"))
	if buf.String() != "first\nsecond\n" {
		t.Fatalf("got %q", buf.String())
	}
}

func TestJSONL_RoundTripsThroughDecoder(t *testing.T) {
	src := `{"level":"error","status":500,"ok":true,"note":null,"nested":{"a":1,"b":2},"tags":["x","y"]}`
	res, err := formats.DecodeLine([]byte(src), formats.DefaultMaxDepth)
	if err != nil {
		t.Fatalf("decode error = %v", err)
	}

	var buf bytes.Buffer
	if err := JSONL(&buf, res.Record); err != nil {
		t.Fatalf("JSONL render error = %v", err)
	}

	// Re-decode the rendered output and confirm it round-trips to an
	// equivalent record.
	rendered := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	res2, err := formats.DecodeLine(rendered, formats.DefaultMaxDepth)
	if err != nil {
		t.Fatalf("re-decode of rendered output failed: %v (rendered: %s)", err, rendered)
	}
	if !eval.DeepEqual(eval.Object(res.Record), eval.Object(res2.Record)) {
		t.Fatalf("round-trip mismatch:\n  original: %s\n  rendered: %s", src, rendered)
	}
}

func TestJSONL_PreservesFieldOrderInOutputBytes(t *testing.T) {
	rec := eval.NewRecord()
	rec.Set("z", eval.Int(1))
	rec.Set("a", eval.Int(2))
	rec.Set("m", eval.Int(3))

	var buf bytes.Buffer
	if err := JSONL(&buf, rec); err != nil {
		t.Fatalf("JSONL error = %v", err)
	}
	want := `{"z":1,"a":2,"m":3}` + "\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q (source key order, not alphabetical)", buf.String(), want)
	}
}

func TestJSONL_StringEscaping(t *testing.T) {
	rec := eval.NewRecord()
	rec.Set("msg", eval.Str(`has "quotes", \backslash\, and unicode: café`))

	var buf bytes.Buffer
	if err := JSONL(&buf, rec); err != nil {
		t.Fatalf("JSONL error = %v", err)
	}

	// The rendered output must itself be valid JSON that decodes back to
	// the exact original string.
	res, err := formats.DecodeLine(bytes.TrimSuffix(buf.Bytes(), []byte{'\n'}), formats.DefaultMaxDepth)
	if err != nil {
		t.Fatalf("rendered output isn't valid JSON: %v (output: %s)", err, buf.String())
	}
	got := res.Record.Get("msg")
	want := `has "quotes", \backslash\, and unicode: café`
	if got.S != want {
		t.Fatalf("round-tripped string = %q, want %q", got.S, want)
	}
}

func TestJSONL_NumberFormatting(t *testing.T) {
	rec := eval.NewRecord()
	rec.Set("bigint", eval.Int(9223372036854775807))
	rec.Set("f", eval.Float(3.14))

	var buf bytes.Buffer
	if err := JSONL(&buf, rec); err != nil {
		t.Fatalf("JSONL error = %v", err)
	}
	want := `{"bigint":9223372036854775807,"f":3.14}` + "\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

func TestJSONL_TimestampAndDuration(t *testing.T) {
	ts := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	rec := eval.NewRecord()
	rec.Set("ts", eval.Timestamp(ts))
	rec.Set("elapsed", eval.DurationVal(90*time.Minute))

	var buf bytes.Buffer
	if err := JSONL(&buf, rec); err != nil {
		t.Fatalf("JSONL error = %v", err)
	}
	want := `{"ts":"2026-08-29T12:00:00Z","elapsed":"1h30m0s"}` + "\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

func TestJSONL_NestedObjectAndArray(t *testing.T) {
	inner := eval.NewRecord()
	inner.Set("x", eval.Int(1))
	rec := eval.NewRecord()
	rec.Set("obj", eval.Object(inner))
	rec.Set("arr", eval.Array([]eval.Value{eval.Int(1), eval.Str("two"), eval.Bool(true)}))

	var buf bytes.Buffer
	if err := JSONL(&buf, rec); err != nil {
		t.Fatalf("JSONL error = %v", err)
	}
	want := `{"obj":{"x":1},"arr":[1,"two",true]}` + "\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

func TestJSONL_EmptyRecord(t *testing.T) {
	var buf bytes.Buffer
	if err := JSONL(&buf, eval.NewRecord()); err != nil {
		t.Fatalf("JSONL error = %v", err)
	}
	if buf.String() != "{}\n" {
		t.Fatalf("output = %q, want %q", buf.String(), "{}\n")
	}
}
