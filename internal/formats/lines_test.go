package formats

import (
	"io"
	"strings"
	"testing"
)

// readAll drains a LineReader into a []string of decoded lines, returning
// the final error (expected to be io.EOF on a clean end of stream).
func readAll(t *testing.T, lr *LineReader) ([]string, error) {
	t.Helper()
	var lines []string
	for {
		line, err := lr.ReadLine()
		if line != nil {
			lines = append(lines, string(line))
		}
		if err != nil {
			return lines, err
		}
	}
}

func TestLineReader_BasicSplit(t *testing.T) {
	lr := NewLineReader(strings.NewReader("a\nb\nc\n"), 0)
	lines, err := readAll(t, lr)
	if err != io.EOF {
		t.Fatalf("final error = %v, want io.EOF", err)
	}
	want := []string{"a", "b", "c"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("lines = %v, want %v", lines, want)
		}
	}
}

func TestLineReader_FinalUnterminatedLineProcessed(t *testing.T) {
	// EC-02: no trailing newline -> last line still processed.
	lr := NewLineReader(strings.NewReader("a\nb\nc"), 0)
	lines, err := readAll(t, lr)
	if err != io.EOF {
		t.Fatalf("final error = %v, want io.EOF", err)
	}
	if len(lines) != 3 || lines[2] != "c" {
		t.Fatalf("lines = %v, want [a b c] with the unterminated 'c' included", lines)
	}
}

func TestLineReader_CRLFStripped(t *testing.T) {
	// EC-04: CRLF file -> '\r' stripped, values clean.
	lr := NewLineReader(strings.NewReader("a\r\nb\r\nc\r\n"), 0)
	lines, _ := readAll(t, lr)
	for i, want := range []string{"a", "b", "c"} {
		if lines[i] != want {
			t.Fatalf("lines[%d] = %q, want %q (no stray \\r)", i, lines[i], want)
		}
	}
}

func TestLineReader_LoneMidLineCRPreservedAsData(t *testing.T) {
	// EC-05: a '\r' not immediately before '\n' is ordinary data, not a
	// line boundary and not stripped.
	lr := NewLineReader(strings.NewReader("has\ramid-line\rcarriage\n"), 0)
	lines, _ := readAll(t, lr)
	want := "has\ramid-line\rcarriage"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("lines = %v, want [%q]", lines, want)
	}
}

func TestLineReader_BOMStrippedOnceAtStart(t *testing.T) {
	// EC-03: UTF-8 BOM stripped once.
	bom := string(bomBytes)
	lr := NewLineReader(strings.NewReader(bom+"first\nsecond\n"), 0)
	lines, _ := readAll(t, lr)
	if len(lines) != 2 || lines[0] != "first" {
		t.Fatalf("lines = %v, want [first second] with the BOM stripped from the first line", lines)
	}
}

func TestLineReader_BOMBytesLaterInStreamNotStripped(t *testing.T) {
	// The BOM bytes reappearing mid-stream (not at true byte offset 0) are
	// ordinary data and must be preserved.
	bom := string(bomBytes)
	lr := NewLineReader(strings.NewReader("first\n"+bom+"second\n"), 0)
	lines, _ := readAll(t, lr)
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2 lines", lines)
	}
	if lines[1] != bom+"second" {
		t.Fatalf("lines[1] = %q, want the BOM bytes preserved (only stream-start BOM is stripped)", lines[1])
	}
}

func TestLineReader_EmptyLinesSkippedAndCounted(t *testing.T) {
	lr := NewLineReader(strings.NewReader("a\n\n\nb\n"), 0)
	lines, _ := readAll(t, lr)
	if len(lines) != 2 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("lines = %v, want [a b] (empty lines skipped, not yielded)", lines)
	}
	if lr.EmptyLines != 2 {
		t.Fatalf("EmptyLines = %d, want 2", lr.EmptyLines)
	}
}

func TestLineReader_EmptyInput(t *testing.T) {
	lr := NewLineReader(strings.NewReader(""), 0)
	lines, err := readAll(t, lr)
	if err != io.EOF {
		t.Fatalf("final error = %v, want io.EOF", err)
	}
	if len(lines) != 0 {
		t.Fatalf("lines = %v, want none", lines)
	}
}

func TestLineReader_OversizedLineSkippedAndCounted(t *testing.T) {
	huge := strings.Repeat("x", 100)
	input := "short1\n" + huge + "\nshort2\n"
	lr := NewLineReader(strings.NewReader(input), 10) // maxLine=10, huge line is 100 bytes

	lines, err := readAll(t, lr)
	if err != io.EOF {
		t.Fatalf("final error = %v, want io.EOF", err)
	}
	if len(lines) != 2 || lines[0] != "short1" || lines[1] != "short2" {
		t.Fatalf("lines = %v, want [short1 short2] with the oversized line skipped", lines)
	}
	if lr.OversizedLines != 1 {
		t.Fatalf("OversizedLines = %d, want 1", lr.OversizedLines)
	}
}

func TestLineReader_OversizedFinalLineNoTrailingNewline(t *testing.T) {
	huge := strings.Repeat("y", 100)
	lr := NewLineReader(strings.NewReader("short\n"+huge), 10)

	lines, err := readAll(t, lr)
	if err != io.EOF {
		t.Fatalf("final error = %v, want io.EOF", err)
	}
	if len(lines) != 1 || lines[0] != "short" {
		t.Fatalf("lines = %v, want [short] — the trailing oversized, unterminated line must not appear", lines)
	}
	if lr.OversizedLines != 1 {
		t.Fatalf("OversizedLines = %d, want 1", lr.OversizedLines)
	}
}

func TestLineReader_ManyConsecutiveOversizedLines(t *testing.T) {
	// Stresses the resync loop: must not recurse per oversized line (would
	// risk stack growth on a pathological file) and must correctly count
	// every one of them.
	huge := strings.Repeat("z", 50)
	var sb strings.Builder
	const n = 500
	for range n {
		sb.WriteString(huge)
		sb.WriteByte('\n')
	}
	sb.WriteString("final\n") // 5 bytes, safely under the maxLine=10 used below

	lr := NewLineReader(strings.NewReader(sb.String()), 10)
	lines, err := readAll(t, lr)
	if err != io.EOF {
		t.Fatalf("final error = %v, want io.EOF", err)
	}
	if len(lines) != 1 || lines[0] != "final" {
		t.Fatalf("lines = %v, want only [final]", lines)
	}
	if lr.OversizedLines != n {
		t.Fatalf("OversizedLines = %d, want %d", lr.OversizedLines, n)
	}
}

func TestLineReader_DefaultMaxLineUsedWhenZero(t *testing.T) {
	lr := NewLineReader(strings.NewReader("x\n"), 0)
	if lr.MaxLine != DefaultMaxLine {
		t.Fatalf("MaxLine = %d, want DefaultMaxLine (%d)", lr.MaxLine, DefaultMaxLine)
	}
}

func TestLineReader_LineExactlyAtMaxLineIsNotOversized(t *testing.T) {
	line := strings.Repeat("a", 10) // exactly maxLine bytes
	lr := NewLineReader(strings.NewReader(line+"\nnext\n"), 10)
	lines, _ := readAll(t, lr)
	if len(lines) != 2 || lines[0] != line {
		t.Fatalf("a line exactly at MaxLine must be accepted, not treated as oversized; got %v", lines)
	}
	if lr.OversizedLines != 0 {
		t.Fatalf("OversizedLines = %d, want 0", lr.OversizedLines)
	}
}
