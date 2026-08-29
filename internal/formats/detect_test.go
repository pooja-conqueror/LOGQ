package formats

import (
	"fmt"
	"strings"
	"testing"
)

func lines(strs ...string) [][]byte {
	out := make([][]byte, len(strs))
	for i, s := range strs {
		out[i] = []byte(s)
	}
	return out
}

func TestDetect_AllJSON(t *testing.T) {
	got := Detect(lines(`{"a":1}`, `{"b":2}`, `{"c":3}`))
	if got != FormatJSONL {
		t.Fatalf("Detect = %v, want FormatJSONL", got)
	}
}

func TestDetect_AllLogfmt(t *testing.T) {
	got := Detect(lines(`level=error msg=boom`, `level=info msg=ok`))
	if got != FormatLogfmt {
		t.Fatalf("Detect = %v, want FormatLogfmt", got)
	}
}

func TestDetect_Plain(t *testing.T) {
	got := Detect(lines(`2026-08-29 something happened`, `another free-text line here`))
	if got != FormatPlain {
		t.Fatalf("Detect = %v, want FormatPlain", got)
	}
}

func TestDetect_EmptySampleDefaultsToPlain(t *testing.T) {
	if got := Detect(nil); got != FormatPlain {
		t.Fatalf("Detect(nil) = %v, want FormatPlain", got)
	}
}

func TestDetect_OneBadLineDisqualifiesJSON(t *testing.T) {
	// Deliberately broken as both JSON AND logfmt (the '{' isn't a valid
	// logfmt key-charset byte either), so this must land on Plain, not
	// silently fall back to logfmt by accident.
	got := Detect(lines(`{"a":1}`, `{"b":2}`, `not json {`))
	if got != FormatPlain {
		t.Fatalf("Detect = %v, want FormatPlain (one bad line disqualifies JSON outright)", got)
	}
}

func TestDetect_OneBadLineDisqualifiesLogfmt(t *testing.T) {
	got := Detect(lines(`level=error`, `level=info`, `this is not logfmt at all!!`))
	if got != FormatPlain {
		t.Fatalf("Detect = %v, want FormatPlain", got)
	}
}

func TestDetect_WhitespaceOnlyLinesDoNotCountAsLogfmt(t *testing.T) {
	// logfmtx.DecodeLine treats a whitespace-only line as a valid, empty
	// record (not an error) — but §9.2 requires >=1 real key=value pair
	// for a line to count toward "this looks like logfmt."
	got := Detect(lines(`   `, `  `))
	if got != FormatPlain {
		t.Fatalf("Detect = %v, want FormatPlain (zero-pair lines don't count as logfmt)", got)
	}
}

func TestDetect_MixedJSONAndLogfmtFallsToPlain(t *testing.T) {
	got := Detect(lines(`{"a":1}`, `level=error msg=boom`))
	if got != FormatPlain {
		t.Fatalf("Detect = %v, want FormatPlain (neither format fits ALL lines)", got)
	}
}

func TestDetect_SingleLineSamples(t *testing.T) {
	if got := Detect(lines(`{"x":1}`)); got != FormatJSONL {
		t.Fatalf("single JSON line: got %v", got)
	}
	if got := Detect(lines(`x=1`)); got != FormatLogfmt {
		t.Fatalf("single logfmt line: got %v", got)
	}
	if got := Detect(lines(`just some text`)); got != FormatPlain {
		t.Fatalf("single plain line: got %v", got)
	}
}

func TestFormat_String(t *testing.T) {
	cases := map[Format]string{FormatJSONL: "jsonl", FormatLogfmt: "logfmt", FormatPlain: "plain"}
	for f, want := range cases {
		if f.String() != want {
			t.Fatalf("%v.String() = %q, want %q", f, f.String(), want)
		}
	}
}

// --- DetectFromReader: the sample-then-replay contract --------------------

func TestDetectFromReader_FewerThan64Lines(t *testing.T) {
	src := `{"a":1}` + "\n" + `{"b":2}` + "\n" + `{"c":3}` + "\n"
	lr := NewLineReader(strings.NewReader(src), 0)

	format, sample, err := DetectFromReader(lr)
	if err != nil {
		t.Fatalf("DetectFromReader error = %v", err)
	}
	if format != FormatJSONL {
		t.Fatalf("format = %v, want FormatJSONL", format)
	}
	if len(sample) != 3 {
		t.Fatalf("sample len = %d, want 3", len(sample))
	}

	// Nothing left in the reader — the whole (short) source was sampled.
	if _, err := lr.ReadLine(); err == nil {
		t.Fatal("expected EOF after sampling the entire short source")
	}
}

func TestDetectFromReader_MoreThan64LinesSamplesExactly64(t *testing.T) {
	var sb strings.Builder
	const total = 70
	for i := range total {
		fmt.Fprintf(&sb, `{"n":%d}`+"\n", i)
	}
	lr := NewLineReader(strings.NewReader(sb.String()), 0)

	format, sample, err := DetectFromReader(lr)
	if err != nil {
		t.Fatalf("DetectFromReader error = %v", err)
	}
	if format != FormatJSONL {
		t.Fatalf("format = %v, want FormatJSONL", format)
	}
	if len(sample) != DetectSampleSize {
		t.Fatalf("sample len = %d, want %d", len(sample), DetectSampleSize)
	}

	// The remaining (70 - 64) = 6 lines must still be readable afterward —
	// this is the actual replay contract: nothing beyond the sample was
	// silently consumed and discarded.
	remaining := 0
	for {
		_, err := lr.ReadLine()
		if err != nil {
			break
		}
		remaining++
	}
	if remaining != total-DetectSampleSize {
		t.Fatalf("remaining lines after sampling = %d, want %d", remaining, total-DetectSampleSize)
	}
}

func TestDetectFromReader_EndToEndNoLinesLostOrDuplicated(t *testing.T) {
	// The real caller pattern: sample, decode+process the sampled lines,
	// then keep reading for the rest — total processed count must exactly
	// match the source, with no line seen twice and none skipped.
	var sb strings.Builder
	const total = 70
	for i := range total {
		fmt.Fprintf(&sb, `{"n":%d}`+"\n", i)
	}
	lr := NewLineReader(strings.NewReader(sb.String()), 0)

	_, sample, err := DetectFromReader(lr)
	if err != nil {
		t.Fatalf("DetectFromReader error = %v", err)
	}

	seen := map[string]bool{}
	for _, line := range sample {
		s := string(line)
		if seen[s] {
			t.Fatalf("line %q appeared twice in the sample itself", s)
		}
		seen[s] = true
	}
	for {
		line, err := lr.ReadLine()
		if line != nil {
			s := string(line)
			if seen[s] {
				t.Fatalf("line %q was processed twice (once in sample, once after)", s)
			}
			seen[s] = true
		}
		if err != nil {
			break
		}
	}

	if len(seen) != total {
		t.Fatalf("total distinct lines processed = %d, want %d (none lost)", len(seen), total)
	}
}

func TestDetectFromReader_EmptySource(t *testing.T) {
	lr := NewLineReader(strings.NewReader(""), 0)
	format, sample, err := DetectFromReader(lr)
	if err != nil {
		t.Fatalf("DetectFromReader(empty) error = %v", err)
	}
	if format != FormatPlain {
		t.Fatalf("format = %v, want FormatPlain", format)
	}
	if len(sample) != 0 {
		t.Fatalf("sample len = %d, want 0", len(sample))
	}
}
