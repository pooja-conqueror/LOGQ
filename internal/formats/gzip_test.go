package formats

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func gzipBytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write([]byte(data)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func TestMaybeGunzip_RoundTrip(t *testing.T) {
	original := "line one\nline two\nline three\n"
	compressed := gzipBytes(t, original)

	r, err := MaybeGunzip(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("MaybeGunzip error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error = %v", err)
	}
	if string(got) != original {
		t.Fatalf("decompressed = %q, want %q", got, original)
	}
}

func TestMaybeGunzip_NonGzipPassthroughUnchanged(t *testing.T) {
	original := `{"level":"error"}` + "\n" + `{"level":"info"}` + "\n"
	r, err := MaybeGunzip(strings.NewReader(original))
	if err != nil {
		t.Fatalf("MaybeGunzip error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error = %v", err)
	}
	// The sniff must not lose or alter any bytes for non-gzip input.
	if string(got) != original {
		t.Fatalf("passthrough = %q, want byte-identical %q", got, original)
	}
}

func TestMaybeGunzip_MultiMemberStream(t *testing.T) {
	// Two independently-compressed gzip members concatenated must
	// decompress as one continuous stream (compress/gzip.Reader defaults
	// to Multistream(true) — this proves that default is actually in
	// effect, not just assumed).
	member1 := gzipBytes(t, "first member\n")
	member2 := gzipBytes(t, "second member\n")
	concatenated := append(append([]byte{}, member1...), member2...)

	r, err := MaybeGunzip(bytes.NewReader(concatenated))
	if err != nil {
		t.Fatalf("MaybeGunzip error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error = %v", err)
	}
	want := "first member\nsecond member\n"
	if string(got) != want {
		t.Fatalf("multi-member decompress = %q, want %q", got, want)
	}
}

func TestMaybeGunzip_TruncatedStreamErrorsOnRead(t *testing.T) {
	// A truncated gzip stream must fail with a real read error partway
	// through — never silently produce partial/corrupt data as if it were
	// complete, and never panic. This exact fixture shape (valid gzip
	// header, corrupted/truncated body) is reused conceptually by the
	// Phase 11 chaos suite's gzip-corruption test.
	full := gzipBytes(t, strings.Repeat("some log line\n", 200))
	truncated := full[:len(full)/2]

	r, err := MaybeGunzip(bytes.NewReader(truncated))
	if err != nil {
		// Sniffing itself may or may not fail depending on exactly where
		// the truncation lands; either outcome is acceptable here as long
		// as it's a reported error, not silence.
		return
	}
	_, readErr := io.ReadAll(r)
	if readErr == nil {
		t.Fatal("reading a truncated gzip stream must produce an error, not silently succeed")
	}
}

func TestMaybeGunzip_EmptyInput(t *testing.T) {
	r, err := MaybeGunzip(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("MaybeGunzip(empty) error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll(empty) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d bytes from empty input, want 0", len(got))
	}
}

func TestMaybeGunzip_SingleByteInput(t *testing.T) {
	// Fewer than 2 bytes total: the sniff can't even check the full magic
	// number — must not panic or misbehave, just treat it as not-gzip.
	r, err := MaybeGunzip(bytes.NewReader([]byte{0x1f}))
	if err != nil {
		t.Fatalf("MaybeGunzip(1 byte) error = %v", err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll error = %v", err)
	}
	if string(got) != "\x1f" {
		t.Fatalf("got %q, want the single byte preserved", got)
	}
}
