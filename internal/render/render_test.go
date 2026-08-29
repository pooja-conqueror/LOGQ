package render

import (
	"bytes"
	"encoding/csv"
	"strings"
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

// --- Table -----------------------------------------------------------------

func recFrom(pairs ...any) *eval.Record {
	rec := eval.NewRecord()
	for i := 0; i < len(pairs); i += 2 {
		rec.Set(pairs[i].(string), pairs[i+1].(eval.Value))
	}
	return rec
}

func TestTable_HeaderAndRows(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("level", eval.Str("error"), "n", eval.Int(1)))
	tbl.Add(recFrom("level", eval.Str("info"), "n", eval.Int(2)))

	var buf bytes.Buffer
	if err := tbl.Flush(&buf); err != nil {
		t.Fatalf("Flush error = %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "level") || !strings.Contains(out, "n") {
		t.Fatalf("output missing header columns:\n%s", out)
	}
	if !strings.Contains(out, "error") || !strings.Contains(out, "info") {
		t.Fatalf("output missing row data:\n%s", out)
	}
}

func TestTable_ColumnOrderIsFirstSeen(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("z", eval.Int(1), "a", eval.Int(2)))
	tbl.Add(recFrom("m", eval.Int(3))) // a new column, first seen in row 2

	var buf bytes.Buffer
	tbl.Flush(&buf)
	header := strings.Fields(strings.Split(buf.String(), "\n")[0])
	want := []string{"z", "a", "m"}
	if len(header) != len(want) {
		t.Fatalf("header = %v, want %v", header, want)
	}
	for i, w := range want {
		if header[i] != w {
			t.Fatalf("header = %v, want %v (first-seen order)", header, want)
		}
	}
}

func TestTable_MissingRendersAsLabel(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("a", eval.Int(1), "b", eval.Int(2)))
	tbl.Add(recFrom("a", eval.Int(3))) // "b" missing on this row

	var buf bytes.Buffer
	tbl.Flush(&buf)
	if !strings.Contains(buf.String(), "(missing)") {
		t.Fatalf("output must show (missing) for an absent field:\n%s", buf.String())
	}
}

func TestTable_NullRendersAsNullLiteral(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("x", eval.Null))
	var buf bytes.Buffer
	tbl.Flush(&buf)
	if !strings.Contains(buf.String(), "null") {
		t.Fatalf("output must show the literal 'null':\n%s", buf.String())
	}
}

func TestTable_NumericColumnRightAligned(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("n", eval.Int(1)))
	tbl.Add(recFrom("n", eval.Int(100)))

	var buf bytes.Buffer
	tbl.Flush(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	// Right-aligned: the shorter value ("1") should have leading padding
	// making it end at the same column position as "100".
	idx1 := strings.Index(lines[1], "1")
	idx100 := strings.Index(lines[2], "100")
	if idx1 == -1 || idx100 == -1 {
		t.Fatalf("could not locate values in rows:\n%s", buf.String())
	}
	// Position of the LAST digit of each value should line up.
	if idx1 != idx100+len("100")-1 {
		t.Fatalf("numeric column not right-aligned: row1=%q row2=%q", lines[1], lines[2])
	}
}

func TestTable_NonNumericColumnNotRightAligned(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("s", eval.Str("a")))
	tbl.Add(recFrom("s", eval.Str("bbbbbbbbbb")))

	var buf bytes.Buffer
	tbl.Flush(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// Left-aligned: both values should start at the same column position.
	if strings.Index(lines[1], "a") != strings.Index(lines[2], "bbbbbbbbbb") {
		t.Fatalf("string column should be left-aligned: row1=%q row2=%q", lines[1], lines[2])
	}
}

func TestTable_LongCellTruncatedWithEllipsis(t *testing.T) {
	long := strings.Repeat("x", 100)
	tbl := NewTable()
	tbl.Add(recFrom("s", eval.Str(long)))

	var buf bytes.Buffer
	tbl.Flush(&buf)
	if strings.Contains(buf.String(), long) {
		t.Fatal("a 100-char cell must be truncated, not shown in full")
	}
	if !strings.Contains(buf.String(), "…") {
		t.Fatalf("truncated cell must end with an ellipsis:\n%s", buf.String())
	}
}

func TestTable_EmptyInput(t *testing.T) {
	tbl := NewTable()
	var buf bytes.Buffer
	if err := tbl.Flush(&buf); err != nil {
		t.Fatalf("Flush(empty) error = %v", err)
	}
	// No columns, no rows — just don't crash, and produce (at most) an
	// empty/blank header line.
	if strings.TrimSpace(buf.String()) != "" {
		t.Fatalf("expected no meaningful output for zero records, got %q", buf.String())
	}
}

func TestTable_ArrayObjectCellsUsePlaceholders(t *testing.T) {
	tbl := NewTable()
	tbl.Add(recFrom("arr", eval.Array([]eval.Value{eval.Int(1)}), "obj", eval.Object(eval.NewRecord())))

	var buf bytes.Buffer
	tbl.Flush(&buf)
	if !strings.Contains(buf.String(), "[array]") || !strings.Contains(buf.String(), "[object]") {
		t.Fatalf("expected placeholder cells for nested structures:\n%s", buf.String())
	}
}

// --- CSV --------------------------------------------------------------------

func TestCSV_HeaderAndRows(t *testing.T) {
	c := NewCSV()
	c.Add(recFrom("level", eval.Str("error"), "n", eval.Int(1)))
	c.Add(recFrom("level", eval.Str("info"), "n", eval.Int(2)))

	var buf bytes.Buffer
	if err := c.Flush(&buf); err != nil {
		t.Fatalf("Flush error = %v", err)
	}
	want := "level,n\r\nerror,1\r\ninfo,2\r\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

func TestCSV_QuoteOnNeed(t *testing.T) {
	c := NewCSV()
	c.Add(recFrom("msg", eval.Str(`has,comma`)))
	c.Add(recFrom("msg", eval.Str(`has"quote`)))
	c.Add(recFrom("msg", eval.Str("has\nnewline")))

	var buf bytes.Buffer
	c.Flush(&buf)
	out := buf.String()
	if !strings.Contains(out, `"has,comma"`) {
		t.Fatalf("comma-containing field must be quoted:\n%s", out)
	}
	if !strings.Contains(out, `"has""quote"`) {
		t.Fatalf(`quote-containing field must be quoted with "" escaping:%s`, out)
	}
}

func TestCSV_MissingAndNullBothEmptyCell(t *testing.T) {
	c := NewCSV()
	c.Add(recFrom("a", eval.Int(1), "b", eval.Int(2))) // establishes columns a,b
	c.Add(recFrom("a", eval.Null))                     // b is MISSING here; a is Null

	var buf bytes.Buffer
	c.Flush(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	// Second data row: "a" is Null -> empty, "b" is MISSING -> empty.
	if lines[2] != "," {
		t.Fatalf("row = %q, want an empty cell for both Null and MISSING", lines[2])
	}
}

func TestCSV_EmptyInput(t *testing.T) {
	c := NewCSV()
	var buf bytes.Buffer
	if err := c.Flush(&buf); err != nil {
		t.Fatalf("Flush(empty) error = %v", err)
	}
	if buf.String() != "\r\n" {
		t.Fatalf("expected just a blank header line for zero columns/records, got %q", buf.String())
	}
}

func TestCSV_ValidRFC4180RoundTrip(t *testing.T) {
	// The output must itself be parseable as valid CSV — proves quoting
	// is actually correct, not just visually plausible.
	c := NewCSV()
	c.Add(recFrom("msg", eval.Str(`tricky, "quoted", and
multiline`)))

	var buf bytes.Buffer
	c.Flush(&buf)

	r := csv.NewReader(strings.NewReader(buf.String()))
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("output is not valid CSV: %v\noutput:\n%s", err, buf.String())
	}
	if len(records) != 2 || records[1][0] != "tricky, \"quoted\", and\nmultiline" {
		t.Fatalf("round-tripped records = %#v", records)
	}
}
