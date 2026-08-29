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

func TestRun_UnrecognizedOutputFlagRejectedCleanly(t *testing.T) {
	code, _, errOut := runCLI(t, []string{"-o", "xml", `x == 1`}, "")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "not recognized") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestRun_TableOutputWorks(t *testing.T) {
	stdin := `{"level":"error","n":1}` + "\n" + `{"level":"info","n":2}` + "\n"
	code, out, errOut := runCLI(t, []string{"-o", "table", `exists(n)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if !strings.Contains(out, "level") || !strings.Contains(out, "error") || !strings.Contains(out, "info") {
		t.Fatalf("out = %q, want a table with header and both rows", out)
	}
}

func TestRun_CSVOutputWorks(t *testing.T) {
	stdin := `{"level":"error","n":1}` + "\n"
	code, out, errOut := runCLI(t, []string{"-o", "csv", `exists(n)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if !strings.Contains(out, "level,n\r\n") || !strings.Contains(out, "error,1\r\n") {
		t.Fatalf("out = %q, want RFC4180 CSV with header", out)
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

// --- Phase 6: --tz / --since / --until / the virtual "ts" field ----------

func TestRun_TzUsesEmbeddedTzdata(t *testing.T) {
	// This is the actual point of the time/tzdata blank import in this
	// commit: a named IANA zone must resolve even though this dev machine
	// (Windows) has no guaranteed system tzdata of its own. If the blank
	// import were missing or broken, time.LoadLocation here would fail
	// and the run would exit 1, not 0.
	stdin := `{"ts":"2026-08-29T12:00:00"}` + "\n" // naive, no zone offset
	code, out, errOut := runCLI(t, []string{"--tz", "America/New_York", `exists(ts)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s) — is time/tzdata actually blank-imported?", code, exitOK, errOut)
	}
	if out == "" {
		t.Fatal("expected the record to match exists(ts)")
	}
}

func TestRun_InvalidTzRejectedCleanly(t *testing.T) {
	code, _, errOut := runCLI(t, []string{"--tz", "Not/A/Real/Zone", `x == 1`}, "")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "invalid --tz") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestRun_VirtualTsFieldQueryable(t *testing.T) {
	// "ts" resolves via candidate-field priority regardless of which raw
	// field actually supplied it — here "timestamp", not "ts" itself.
	// exists(ts), not an inequality against a string literal: the query
	// language has no timestamp-literal syntax at all (only relative
	// durations like -1h via the Timestamp±Duration coercion), so
	// exists() is the correct way to prove resolution succeeded.
	stdin := `{"timestamp":"2026-08-29T12:00:00Z","msg":"a"}` + "\n"
	code, out, errOut := runCLI(t, []string{`exists(ts)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if out == "" {
		t.Fatal("expected the record to match exists(ts), resolved via the 'timestamp' field")
	}

	// Sanity: a record with NO resolvable timestamp field at all must not
	// match exists(ts).
	code2, out2, errOut2 := runCLI(t, []string{`exists(ts)`}, `{"msg":"no timestamp here"}`+"\n")
	if code2 != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code2, errOut2)
	}
	if out2 != "" {
		t.Fatalf("out = %q, want empty — no candidate timestamp field present", out2)
	}
}

func TestRun_SinceDropsOlderRecords(t *testing.T) {
	stdin := `{"ts":"2026-08-29T10:00:00Z","n":1}
{"ts":"2026-08-29T13:00:00Z","n":2}
`
	// "now" is frozen at run start (real time, not 2026-08-29), so use an
	// absolute RFC3339 --since bound rather than a relative duration —
	// this isolates the since/until *filtering logic* from what "now"
	// happens to be when the test executes.
	code, out, errOut := runCLI(t, []string{"--since", "2026-08-29T12:00:00Z", `exists(n)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d, want %d (stderr: %s)", code, exitOK, errOut)
	}
	if !strings.Contains(out, `"n":2`) || strings.Contains(out, `"n":1`) {
		t.Fatalf("out = %q, want only the 13:00 record (n=2), not the 10:00 one", out)
	}
	if !strings.Contains(errOut, "1 dropped by --since/--until") {
		t.Fatalf("errOut = %q, want the dropped-by-window count reported", errOut)
	}
}

func TestRun_UntilDropsNewerRecords(t *testing.T) {
	stdin := `{"ts":"2026-08-29T10:00:00Z","n":1}
{"ts":"2026-08-29T13:00:00Z","n":2}
`
	code, out, errOut := runCLI(t, []string{"--until", "2026-08-29T12:00:00Z", `exists(n)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, `"n":1`) || strings.Contains(out, `"n":2`) {
		t.Fatalf("out = %q, want only the 10:00 record (n=1)", out)
	}
}

func TestRun_SinceUntilDropUnresolvableTimestamps(t *testing.T) {
	// D-1: a record with no resolvable timestamp is normally never
	// dropped for that reason alone — EXCEPT under an explicit
	// --since/--until bound, where it's dropped and counted.
	stdin := `{"msg":"no timestamp field at all"}` + "\n"

	// Without --since/--until: passes through untouched.
	code, out, _ := runCLI(t, []string{`exists(msg)`}, stdin)
	if code != exitOK || out == "" {
		t.Fatalf("without a window bound, a timestamp-less record must still match; exit=%d out=%q", code, out)
	}

	// With --since set: dropped, not matched, and counted.
	code, out, errOut := runCLI(t, []string{"--since", "2026-01-01T00:00:00Z", `exists(msg)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if out != "" {
		t.Fatalf("out = %q, want empty — a timestamp-less record must be dropped under --since", out)
	}
	if !strings.Contains(errOut, "1 dropped by --since/--until") {
		t.Fatalf("errOut = %q, want the drop counted", errOut)
	}
}

func TestRun_InvalidSinceRejectedCleanly(t *testing.T) {
	code, _, errOut := runCLI(t, []string{"--since", "not a valid bound", `x == 1`}, "")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut, "invalid --since") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestRun_UntilAcceptsTheLiteralNow(t *testing.T) {
	// A record timestamped in the past must always be <= "now".
	stdin := `{"ts":"2020-01-01T00:00:00Z","n":1}` + "\n"
	code, out, errOut := runCLI(t, []string{"--until", "now", `exists(n)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if out == "" {
		t.Fatal("a record from 2020 must be <= --until now")
	}
}

func TestRun_FieldsStageActuallyProjects(t *testing.T) {
	// Was a regression guard (commit 26) for stages parsing but not being
	// applied — now that they're wired (commit 27), this instead confirms
	// the real behavior: "b" must NOT survive the projection.
	stdin := `{"a":1,"b":2}` + "\n"
	code, out, errOut := runCLI(t, []string{"-o", "jsonl", `exists(a) | fields a`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if strings.TrimRight(out, "\n") != `{"a":1}` {
		t.Fatalf("out = %q, want {\"a\":1} — field b must be projected away", out)
	}
}

func TestRun_LimitStageActuallyLimits(t *testing.T) {
	stdin := `{"n":1}` + "\n" + `{"n":2}` + "\n" + `{"n":3}` + "\n"
	code, out, errOut := runCLI(t, []string{"-o", "jsonl", `exists(n) | limit 2`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want exactly 2 (limit 2):\n%s", len(lines), out)
	}
}

func TestRun_LimitStopsReadingLaterFilesEntirely(t *testing.T) {
	// The pipeline is ONE shared instance across every source — limit's
	// count must apply across files, not restart per file, and once
	// satisfied it should stop opening later files at all.
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.jsonl")
	f2 := filepath.Join(dir, "b.jsonl")
	if err := os.WriteFile(f1, []byte(`{"n":1}`+"\n"+`{"n":2}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.WriteFile(f2, []byte(`{"n":999}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	code, out, errOut := runCLI(t, []string{"-o", "jsonl", `exists(n) | limit 2`, f1, f2}, "")
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if strings.Contains(out, "999") {
		t.Fatalf("out = %q, want file b.jsonl never read at all once limit 2 was satisfied by a.jsonl", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
}

func TestRun_SortStageActuallySorts(t *testing.T) {
	stdin := `{"n":3}` + "\n" + `{"n":1}` + "\n" + `{"n":2}` + "\n"
	code, out, errOut := runCLI(t, []string{"-o", "jsonl", `exists(n) | sort n asc limit 10`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	want := `{"n":1}` + "\n" + `{"n":2}` + "\n" + `{"n":3}` + "\n"
	if out != want {
		t.Fatalf("out = %q, want %q (sorted ascending)", out, want)
	}
}

func TestRun_SortStageWithTableOutput(t *testing.T) {
	// Proves sort's Flush-time output correctly reaches a *buffered*
	// renderer too, not just raw/jsonl's streaming path.
	stdin := `{"n":3}` + "\n" + `{"n":1}` + "\n"
	code, out, errOut := runCLI(t, []string{"-o", "table", `exists(n) | sort n asc limit 10`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 { // header + 2 rows
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), out)
	}
}

func TestRun_RawOutputFallsBackToJSONLWhenStagesPresent(t *testing.T) {
	// §11.6's byte-verbatim guarantee can't survive a fields projection —
	// documented fallback: raw becomes jsonl serialization of the final
	// record whenever any stage ran, rather than the stale original line.
	stdin := `{"a":1,"b":2}` + "\n"
	code, out, errOut := runCLI(t, []string{`exists(a) | fields a`}, stdin) // default -o raw
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if strings.TrimRight(out, "\n") != `{"a":1}` {
		t.Fatalf("out = %q, want the jsonl-fallback projected record, not the stale original line", out)
	}
}

func TestRun_RawOutputStillByteVerbatimWithoutStages(t *testing.T) {
	// Sanity check that the fallback above didn't regress the ordinary,
	// no-stages case: raw output must still be the untouched source line.
	stdin := `{"b":2,"a":1}` + "\n" // deliberately non-canonical key order
	code, out, errOut := runCLI(t, []string{`exists(a)`}, stdin)
	if code != exitOK {
		t.Fatalf("exit = %d (stderr: %s)", code, errOut)
	}
	if strings.TrimRight(out, "\n") != `{"b":2,"a":1}` {
		t.Fatalf("out = %q, want the byte-identical original line", out)
	}
}
