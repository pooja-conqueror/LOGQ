// Package golden implements Track B's "test suite proving it" the
// black-box way: build the REAL logq binary once, then run it as a real
// subprocess against a directory of fixtures — a query × format × flag
// combination per fixture — comparing stdout, stderr, and exit code
// byte-exact against checked-in golden files. Deliberately black-box
// (a real subprocess, not an in-process call into cmd/logq's own
// package, which isn't importable from outside it anyway) rather than
// unit-level: this is what actually exercises the full, real,
// end-user-facing behavior end to end, catching integration issues a
// same-package unit test could miss entirely.
//
// Dependency-free by construction: `go build`/`os/exec`/`bytes` only.
package golden

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// binPath is the real logq binary, built once in TestMain before any
// fixture runs — never per-fixture, which would dominate the whole
// suite's runtime with N redundant compiles instead of one.
var binPath string

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "logq-golden-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "golden: MkdirTemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmpDir)

	name := "logq"
	if runtime.GOOS == "windows" {
		name = "logq.exe"
	}
	binPath = filepath.Join(tmpDir, name)

	// The full module import path, not a relative one — resolves
	// correctly regardless of `go test`'s own working directory.
	cmd := exec.Command("go", "build", "-o", binPath, "github.com/pooja-conqueror/LOGQ/cmd/logq")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "golden: building logq:", err)
		os.Exit(1)
	}

	os.Exit(m.Run())
}

// TestGolden runs every fixture directory under testdata/ as its own
// subtest (go test -run TestGolden/<name> targets one directly).
func TestGolden(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatalf("ReadDir(testdata) error = %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		t.Run(name, func(t *testing.T) {
			runGoldenCase(t, filepath.Join("testdata", name))
		})
	}
}

// runGoldenCase runs one fixture directory's case: args (one CLI
// argument per line), an optional stdin file, run the real binary with
// its working directory set to the fixture itself (so args referencing
// an input file by its own bare relative name — e.g. "input.jsonl" —
// just work, no path-rewriting needed), then compare stdout/stderr/exit
// code byte-exact against stdout.golden/stderr.golden/exit.golden. A
// missing stdout.golden/stderr.golden means "expect empty"; a missing
// exit.golden means "expect exit 0."
func runGoldenCase(t *testing.T, dir string) {
	t.Helper()

	args := readArgsFile(t, filepath.Join(dir, "args"))
	var stdin []byte
	if data, err := os.ReadFile(filepath.Join(dir, "stdin")); err == nil {
		stdin = data
	}

	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		exitErr, ok := runErr.(*exec.ExitError)
		if !ok {
			t.Fatalf("running logq failed to even start: %v", runErr)
		}
		exitCode = exitErr.ExitCode()
	}

	compareGolden(t, filepath.Join(dir, "stdout.golden"), stdout.Bytes(), "stdout")
	compareGolden(t, filepath.Join(dir, "stderr.golden"), stderr.Bytes(), "stderr")

	wantExit := readExitFile(t, filepath.Join(dir, "exit.golden"))
	if exitCode != wantExit {
		t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", exitCode, wantExit, stdout.String(), stderr.String())
	}
}

func readArgsFile(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	var args []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		args = append(args, line)
	}
	return args
}

func readExitFile(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0 // absent exit.golden means "expect success"
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("exit.golden content %q is not an integer: %v", data, err)
	}
	return n
}

func compareGolden(t *testing.T, path string, got []byte, label string) {
	t.Helper()
	want, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		want = nil // absent golden file means "expect empty"
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s mismatch (byte-exact required)\n--- got ---\n%s\n--- want ---\n%s", label, got, want)
	}
}
