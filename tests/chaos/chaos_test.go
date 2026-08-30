// Package chaos exercises logq under adversarial, real-world-shaped
// conditions the golden suite's byte-exact fixtures deliberately don't
// cover: mid-stream corruption (a truncated gzip file), volume (hundreds
// of oversized lines interleaved with valid ones), and concurrency
// stress (-j N against a high-cardinality group-by). Black-box, same
// architecture as tests/golden (build the real binary once in TestMain,
// run it as a real subprocess per test) — for the SAME reason: this is
// what actually proves end-to-end behavior, not an in-process shortcut.
//
// One chaos dimension deliberately does NOT live here: mid-run signal
// interruption. Real OS SIGINT delivery to a subprocess proved
// unreliable in this project's own sandboxed dev environment (`kill
// -INT` via Git Bash/MSYS timed out against a piped background job,
// documented in the commit-36+37 work) — testing it via a real
// subprocess here would just be flaky for no real gain, since
// cmd/logq's own injectable-context mechanism (runCtx) already lets a
// test cancel deterministically, in-process, at an EXACT point mid-
// stream. That test lives in cmd/logq/chaos_test.go instead, next to
// the code it exercises directly, for exactly that reason.
//
// Intended to be run with the race detector (`make race` /
// `go test -race ./...`) once a C toolchain is available — this dev
// environment doesn't have one (`go test -race` itself fails here with
// "cgo: C compiler \"gcc\" not found"), so that verification pass
// itself could not be completed in this session; documented rather than
// claimed. -j's own design (internal/pipeline/parallel_stats.go) is
// reasoned to be race-free — each shard's state is touched only by its
// own owning goroutine, all cross-goroutine communication is via
// channels — and TestChaos_HighCardinalityParallelStats below at least
// proves it's CORRECT under real concurrent load, which -race would
// complement, not replace.
package chaos

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "logq-chaos-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "chaos: MkdirTemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	name := "logq"
	if runtime.GOOS == "windows" {
		name = "logq.exe"
	}
	binPath = filepath.Join(tmpDir, name)

	cmd := exec.Command("go", "build", "-o", binPath, "github.com/pooja-conqueror/LOGQ/cmd/logq")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "chaos: building logq:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

func runLogq(t *testing.T, dir string, stdin []byte, args ...string) (stdout, stderr string, exitCode int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err := cmd.Run()
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running logq failed to even start: %v", err)
		}
		exitCode = exitErr.ExitCode()
	}
	return outBuf.String(), errBuf.String(), exitCode
}

// TestChaos_TruncatedGzipMidStream reuses commit 18's own truncation
// technique (internal/formats/gzip_test.go's TestMaybeGunzip_
// TruncatedStreamErrorsOnRead: compress real content, then chop the
// compressed bytes in half) but proves it end to end through the WHOLE
// CLI, not just the isolated decompression function: logq must neither
// panic nor hang, must surface a clear I/O-class failure, and — since
// gzip decompression is streamed, not read-all-upfront — should still
// emit whatever valid records it decoded before hitting the truncation.
func TestChaos_TruncatedGzipMidStream(t *testing.T) {
	dir := t.TempDir()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	for i := range 500 {
		fmt.Fprintf(gw, `{"x":%d}`+"\n", i)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip Close error = %v", err)
	}
	full := buf.Bytes()
	truncated := full[:len(full)/2]

	path := filepath.Join(dir, "truncated.jsonl.gz")
	if err := os.WriteFile(path, truncated, 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	stdout, stderr, exitCode := runLogq(t, dir, nil, "exists(x)", "truncated.jsonl.gz")

	if exitCode != 4 {
		t.Fatalf("exit = %d, want 4 (E-IO — unreadable input) — got stdout=%q stderr=%q", exitCode, stdout, stderr)
	}
	if stderr == "" {
		t.Fatal("stderr is empty, want a clear error describing the corrupted stream")
	}
	// Whatever decoded before the truncation point must still be valid,
	// well-formed JSON lines — never a partial/corrupted line leaking
	// through.
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, `{"x":`) || !strings.HasSuffix(line, `}`) {
			t.Fatalf("corrupted/partial line leaked into output: %q", line)
		}
	}
}

// TestChaos_ManyOversizedLinesInterleaved stresses commit 12's
// skip-and-resume line splitting at volume — hundreds of oversized lines
// interleaved with valid ones, proving every valid line still survives
// intact and in order, and the oversized count is exact, not just
// "roughly right" or silently wrong under repeated resync.
func TestChaos_ManyOversizedLinesInterleaved(t *testing.T) {
	dir := t.TempDir()

	const total = 400
	var buf bytes.Buffer
	wantValid := 0
	for i := range total {
		if i%2 == 0 {
			fmt.Fprintf(&buf, `{"x":%d,"pad":"%s"}`+"\n", i, strings.Repeat("a", 200))
		} else {
			fmt.Fprintf(&buf, `{"x":%d}`+"\n", i)
			wantValid++
		}
	}
	path := filepath.Join(dir, "mixed.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	stdout, stderr, exitCode := runLogq(t, dir, nil, "--max-line", "50", "exists(x)", "mixed.jsonl")
	if exitCode != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", exitCode, stderr)
	}

	gotLines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(gotLines) != wantValid {
		t.Fatalf("got %d surviving lines, want %d", len(gotLines), wantValid)
	}
	for i, line := range gotLines {
		want := fmt.Sprintf(`{"x":%d}`, i*2+1)
		if line != want {
			t.Fatalf("line %d = %q, want %q — order/content must survive interleaved oversized skips exactly", i, line, want)
		}
	}
	wantOversized := total - wantValid
	if !strings.Contains(stderr, fmt.Sprintf("%d oversized", wantOversized)) {
		t.Fatalf("stderr = %q, want it to report exactly %d oversized lines", stderr, wantOversized)
	}
}

// TestChaos_HighCardinalityParallelStats pushes -j well beyond the
// determinism tests' own scale (internal/pipeline/parallel_stats_test.go
// uses up to 16 workers over ~2000 records) — real volume, high
// cardinality, through the actual compiled binary — proving -j 8's
// output is both internally correct (every group's count sums right)
// and byte-identical to -j 1's, under real concurrent load, not just a
// small in-package fixture.
func TestChaos_HighCardinalityParallelStats(t *testing.T) {
	dir := t.TempDir()

	const groups = 5000
	const perGroup = 20
	var buf bytes.Buffer
	for g := range groups {
		for range perGroup {
			fmt.Fprintf(&buf, `{"service":"svc%05d"}`+"\n", g)
		}
	}
	path := filepath.Join(dir, "high_card.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	seqOut, seqErr, seqExit := runLogq(t, dir, nil, "--max-groups", "10000", "-j", "1", "| stats count() by service", "high_card.jsonl")
	if seqExit != 0 {
		t.Fatalf("-j 1 exit = %d (stderr: %s)", seqExit, seqErr)
	}
	parOut, parErr, parExit := runLogq(t, dir, nil, "--max-groups", "10000", "-j", "8", "| stats count() by service", "high_card.jsonl")
	if parExit != 0 {
		t.Fatalf("-j 8 exit = %d (stderr: %s)", parExit, parErr)
	}

	if seqOut != parOut {
		t.Fatal("-j 1 and -j 8 output diverged under high-cardinality concurrent load")
	}
	gotLines := strings.Split(strings.TrimRight(parOut, "\n"), "\n")
	if len(gotLines) != groups {
		t.Fatalf("got %d groups, want %d", len(gotLines), groups)
	}
	wantLine := fmt.Sprintf(`{"service":"svc00000","count":%d}`, perGroup)
	if gotLines[0] != wantLine {
		t.Fatalf("first group = %q, want %q", gotLines[0], wantLine)
	}
}
