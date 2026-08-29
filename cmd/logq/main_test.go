package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI is the test harness: exercises run() exactly as main() does, but
// with in-memory I/O instead of the real os.Stdin/Stdout/Stderr.
func runCLI(t *testing.T, args []string, stdin string) (exitCode int, stdout, stderr string) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	code := run(args, strings.NewReader(stdin), &outBuf, &errBuf)
	return code, outBuf.String(), errBuf.String()
}

func TestRun_Version(t *testing.T) {
	code, out, _ := runCLI(t, []string{"--version"}, "")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "logq "+version) {
		t.Fatalf("stdout = %q, want it to contain the version string", out)
	}
}

func TestRun_Help(t *testing.T) {
	code, out, _ := runCLI(t, []string{"--help"}, "")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d", code, exitOK)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("stdout = %q, want usage text", out)
	}
}

func TestRun_MissingQuery(t *testing.T) {
	code, _, errOut := runCLI(t, []string{}, "")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "missing QUERY") {
		t.Fatalf("stderr = %q, want a missing-QUERY message", errOut)
	}
}

func TestRun_QueryCompileError(t *testing.T) {
	code, _, errOut := runCLI(t, []string{`level ==`}, "")
	if code != exitCompile {
		t.Fatalf("exit = %d, want %d", code, exitCompile)
	}
	if !strings.Contains(errOut, "E-PARSE") {
		t.Fatalf("stderr = %q, want an E-PARSE message", errOut)
	}
}

func TestRun_EndToEndFilterOverJSONLFromStdin(t *testing.T) {
	stdin := `{"level":"info","msg":"boot"}
{"level":"error","msg":"disk full"}
{"level":"warn","msg":"low memory"}
{"level":"error","msg":"connection refused"}
`
	code, out, errOut := runCLI(t, []string{`level == "error"`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d matching lines, want 2:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[0], "disk full") || !strings.Contains(lines[1], "connection refused") {
		t.Fatalf("unexpected matches:\n%s", out)
	}
	// -o raw is byte-verbatim: the original source lines, untouched.
	if lines[0] != `{"level":"error","msg":"disk full"}` {
		t.Fatalf("raw output line = %q, want byte-identical to the source line", lines[0])
	}
}

func TestRun_JSONLOutput(t *testing.T) {
	stdin := `{"a":1,"b":2}` + "\n"
	code, out, errOut := runCLI(t, []string{`-o`, `jsonl`, `a == 1`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if strings.TrimRight(out, "\n") != `{"a":1,"b":2}` {
		t.Fatalf("jsonl output = %q", out)
	}
}

func TestRun_ZeroMatchesStillExitsZero(t *testing.T) {
	stdin := `{"level":"info"}` + "\n"
	code, out, errOut := runCLI(t, []string{`level == "error"`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (0 matches is still success)", code, exitOK)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want empty (nothing matched)", out)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want empty (no malformed lines, nothing to report)", errOut)
	}
}

func TestRun_MalformedLinesSkippedNotFatal(t *testing.T) {
	stdin := `{"level":"error","n":1}
not valid json at all
{"level":"error","n":2}
`
	// -f jsonl bypasses auto-detection deliberately: this test isolates
	// decode-time malformed-line handling from format detection, which is
	// its own separate, already-well-tested concern (commit 19) — see
	// TestRun_MalformedLineWithinSampleAffectsAutoDetection below for what
	// happens to this exact fixture under auto mode instead.
	code, out, errOut := runCLI(t, []string{"-f", "jsonl", `level == "error"`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (a malformed line must not abort the run)", code, exitOK)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 valid matches despite the malformed line in between:\n%s", len(lines), out)
	}
	if !strings.Contains(errOut, "1 malformed") {
		t.Fatalf("stderr = %q, want it to report exactly 1 malformed line", errOut)
	}
}

func TestRun_FileNotFound(t *testing.T) {
	code, _, errOut := runCLI(t, []string{`x == 1`, "/no/such/file/logq-test"}, "")
	if code != exitIO {
		t.Fatalf("exit = %d, want %d", code, exitIO)
	}
	if !strings.Contains(errOut, "cannot open") {
		t.Fatalf("stderr = %q, want a cannot-open message", errOut)
	}
}

func TestRun_ReadsFromRealFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.jsonl")
	content := `{"status":500}` + "\n" + `{"status":200}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, out, errOut := runCLI(t, []string{`status >= 500`, path}, "")
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if strings.TrimRight(out, "\n") != `{"status":500}` {
		t.Fatalf("out = %q", out)
	}
}

func TestRun_DashMeansStdinAlongsideRealFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.jsonl")
	if err := os.WriteFile(path, []byte(`{"src":"file"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, out, errOut := runCLI(t, []string{`exists(src)`, path, "-"}, `{"src":"stdin"}`+"\n")
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, `"file"`) || !strings.Contains(out, `"stdin"`) {
		t.Fatalf("out = %q, want lines from both the file and stdin", out)
	}
}

func TestRun_UnrecognizedFormatFlagRejectedCleanly(t *testing.T) {
	code, _, errOut := runCLI(t, []string{"-f", "xml", `x == 1`}, "")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "not recognized") {
		t.Fatalf("stderr = %q, want a clear not-recognized message", errOut)
	}
}

func TestRun_LogfmtFormatWorks(t *testing.T) {
	stdin := "level=error msg=boom\nlevel=info msg=ok\n"
	code, out, errOut := runCLI(t, []string{"-f", "logfmt", `level == "error"`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if strings.TrimRight(out, "\n") != "level=error msg=boom" {
		t.Fatalf("out = %q", out)
	}
}

func TestRun_PlainFormatWorks(t *testing.T) {
	stdin := "auth failed for bob\nrequest completed ok\n"
	code, out, errOut := runCLI(t, []string{"-f", "plain", `msg ~ "auth failed"`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if strings.TrimRight(out, "\n") != "auth failed for bob" {
		t.Fatalf("out = %q", out)
	}
}

func TestRun_AutoDetectsLogfmtWithoutForcing(t *testing.T) {
	stdin := "level=error msg=boom\nlevel=info msg=ok\n"
	code, out, errOut := runCLI(t, []string{`level == "error"`}, stdin) // no -f: auto
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if strings.TrimRight(out, "\n") != "level=error msg=boom" {
		t.Fatalf("out = %q, want auto-detection to correctly identify logfmt", out)
	}
}

func TestRun_MalformedLineWithinSampleAffectsAutoDetection(t *testing.T) {
	// Documents a real, spec-faithful consequence of the deterministic
	// cascade (§9.2): auto-detection requires EVERY sampled line to fit a
	// format. A single malformed line among the first 64 makes the whole
	// source fall through — here, all the way to plain, since this exact
	// mix is neither valid JSONL (one broken line) nor valid logfmt (the
	// two intact lines are JSON, and JSON's '{' isn't a valid logfmt key
	// byte). This is why TestRun_MalformedLinesSkippedNotFatal forces
	// -f jsonl explicitly instead of relying on auto-detection.
	stdin := `{"level":"error","n":1}
not valid json at all
{"level":"error","n":2}
`
	code, out, errOut := runCLI(t, []string{`level == "error"`}, stdin) // no -f: auto
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty — under plain-format fallback there is no 'level' field to match", out)
	}
}

func TestRun_UnsupportedOutputFlagRejectedCleanly(t *testing.T) {
	code, _, errOut := runCLI(t, []string{"-o", "table", `x == 1`}, "")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "not yet supported") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestRun_StdoutIsResultsOnlyStderrIsDiagnosticsOnly(t *testing.T) {
	stdin := `{"level":"error"}` + "\n"
	_, out, errOut := runCLI(t, []string{`level == "error"`}, stdin)
	if strings.Contains(out, "logq:") {
		t.Fatalf("stdout contains a diagnostic-looking line: %q", out)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want empty on a clean run with no malformed lines", errOut)
	}
}
