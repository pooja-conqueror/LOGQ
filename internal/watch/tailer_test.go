package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile(%q) error = %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("WriteString error = %v", err)
	}
}

func TestTailer_FirstPollSkipsExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "line1\nline2\n")

	tl := NewTailer(path)
	defer tl.Close()
	data, rotated, err := tl.Poll()
	if err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	if rotated {
		t.Fatal("rotated = true on the very first poll, want false")
	}
	if len(data) != 0 {
		t.Fatalf("data = %q, want empty — first poll must skip pre-existing content (tail -f convention)", data)
	}
}

func TestTailer_SecondPollSeesOnlyNewlyAppendedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "old line\n")

	tl := NewTailer(path)
	defer tl.Close()
	if _, _, err := tl.Poll(); err != nil { // bootstrap poll
		t.Fatalf("Poll error = %v", err)
	}
	appendFile(t, path, "new line\n")

	data, rotated, err := tl.Poll()
	if err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	if rotated {
		t.Fatal("rotated = true, want false — this is ordinary growth")
	}
	if string(data) != "new line\n" {
		t.Fatalf("data = %q, want %q", data, "new line\n")
	}
}

func TestTailer_MultiplePollsAccumulateCorrectly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "")

	tl := NewTailer(path)
	defer tl.Close()
	if _, _, err := tl.Poll(); err != nil {
		t.Fatalf("Poll error = %v", err)
	}

	var got []byte
	for i := range 5 {
		appendFile(t, path, "chunk")
		appendFile(t, path, string(rune('0'+i)))
		appendFile(t, path, "\n")
		data, _, err := tl.Poll()
		if err != nil {
			t.Fatalf("Poll error = %v", err)
		}
		got = append(got, data...)
	}
	want := "chunk0\nchunk1\nchunk2\nchunk3\nchunk4\n"
	if string(got) != want {
		t.Fatalf("accumulated data = %q, want %q", got, want)
	}
}

func TestTailer_CopytruncateShrinkResetsOffsetAndReportsRotated(t *testing.T) {
	// EC-43: truncated in place (same file identity), smaller than
	// where Tailer had already read to.
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "aaaaaaaaaa\n")

	tl := NewTailer(path)
	defer tl.Close()
	if _, _, err := tl.Poll(); err != nil { // bootstrap, skips to EOF (offset=11)
		t.Fatalf("Poll error = %v", err)
	}

	// Truncate the SAME file (same inode/identity on Unix; same handle
	// identity semantics on Windows) down to something shorter.
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("WriteFile (truncate) error = %v", err)
	}

	data, rotated, err := tl.Poll()
	if err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	if !rotated {
		t.Fatal("rotated = false, want true — the file shrank below the previous read offset")
	}
	if string(data) != "new\n" {
		t.Fatalf("data = %q, want %q (read from the reset offset 0)", data, "new\n")
	}
}

func TestTailer_DeletedThenRecreatedBiggerReopensFromStart(t *testing.T) {
	// EC-42: deleted then recreated (possibly bigger) — reopen-from-start
	// rule fires.
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "original\n")

	tl := NewTailer(path)
	defer tl.Close()
	if _, _, err := tl.Poll(); err != nil { // bootstrap, skips existing "original\n"
		t.Fatalf("Poll error = %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove error = %v", err)
	}
	dataDuringGap, rotatedDuringGap, err := tl.Poll()
	if err != nil {
		t.Fatalf("Poll error (during gap) = %v", err)
	}
	if len(dataDuringGap) != 0 || rotatedDuringGap {
		t.Fatalf("during the missing-file gap: data=%q rotated=%v, want empty/false", dataDuringGap, rotatedDuringGap)
	}

	// Recreate, bigger than the offset the old file had reached.
	writeFile(t, path, "brand new content, much longer than before\n")

	data, rotated, err := tl.Poll()
	if err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	if !rotated {
		t.Fatal("rotated = false, want true — the file reappeared after being missing")
	}
	if string(data) != "brand new content, much longer than before\n" {
		t.Fatalf("data = %q, want the recreated file's full content from offset 0", data)
	}
}

func TestTailer_MissingFileIsNotAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.txt")
	tl := NewTailer(path)
	defer tl.Close()
	data, rotated, err := tl.Poll()
	if err != nil {
		t.Fatalf("Poll error = %v, want nil (a missing file is not a Poll error)", err)
	}
	if len(data) != 0 || rotated {
		t.Fatalf("data=%q rotated=%v, want empty/false for a missing file", data, rotated)
	}
}

func TestTailer_NoNewDataReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "line\n")

	tl := NewTailer(path)
	defer tl.Close()
	if _, _, err := tl.Poll(); err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	data, rotated, err := tl.Poll() // nothing changed since the bootstrap poll
	if err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	if len(data) != 0 || rotated {
		t.Fatalf("data=%q rotated=%v, want empty/false when nothing changed", data, rotated)
	}
}

func TestTailer_Close(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "x\n")
	tl := NewTailer(path)
	defer tl.Close()
	if _, _, err := tl.Poll(); err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	if err := tl.Close(); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := tl.Close(); err != nil { // idempotent
		t.Fatalf("second Close error = %v, want nil", err)
	}
}

func TestLoop_CallsOnDataForNewContentAndStopsOnCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "")
	tl := NewTailer(path)
	defer tl.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var collected []byte
	done := make(chan error, 1)
	go func() {
		done <- Loop(ctx, tl, 10*time.Millisecond, func(data []byte, rotated bool) error {
			collected = append(collected, data...)
			return nil
		})
	}()

	time.Sleep(30 * time.Millisecond) // let the bootstrap poll happen
	appendFile(t, path, "hello\n")
	time.Sleep(60 * time.Millisecond) // let at least one more poll see it
	cancel()

	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Loop returned err = %v, want context.Canceled", err)
	}
	if string(collected) != "hello\n" {
		t.Fatalf("collected = %q, want %q", collected, "hello\n")
	}
}

func TestLoop_PropagatesOnDataError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.txt")
	writeFile(t, path, "")
	tl := NewTailer(path)
	defer tl.Close()

	boom := errors.New("boom")
	ctx := context.Background()

	// Bootstrap first so the next append is visible to onData.
	if _, _, err := tl.Poll(); err != nil {
		t.Fatalf("Poll error = %v", err)
	}
	appendFile(t, path, "x\n")

	err := Loop(ctx, tl, 10*time.Millisecond, func(data []byte, rotated bool) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Loop returned err = %v, want %v", err, boom)
	}
}
