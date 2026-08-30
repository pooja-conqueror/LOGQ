package render

import (
	"bytes"
	"os"
	"testing"
)

// unsetenvForTest unsets key for the duration of t, restoring whatever
// value (or absence) it had before — os.Unsetenv alone would leak into
// whatever runs after t in the same test binary; t.Setenv only covers
// the "set to a value" case, not "ensure absent."
func unsetenvForTest(t *testing.T, key string) {
	t.Helper()
	if orig, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { os.Setenv(key, orig) })
	} else {
		t.Cleanup(func() { os.Unsetenv(key) })
	}
	os.Unsetenv(key)
}

func TestIsTTY_FalseForNonFileWriters(t *testing.T) {
	var buf bytes.Buffer
	if IsTTY(&buf) {
		t.Fatal("IsTTY(bytes.Buffer) = true, want false — never a terminal")
	}
}

func TestIsTTY_FalseForRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("CreateTemp error = %v", err)
	}
	defer f.Close()
	if IsTTY(f) {
		t.Fatal("IsTTY(regular file) = true, want false")
	}
}

func TestShouldColor_ExplicitFlagAlwaysWins(t *testing.T) {
	unsetenvForTest(t, "NO_COLOR")
	var buf bytes.Buffer
	if ShouldColor(&buf, true) {
		t.Fatal("ShouldColor(noColorFlag=true) = true, want false — the flag always wins")
	}
}

func TestShouldColor_NoColorEnvPresentDisables(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	if ShouldColor(&buf, false) {
		t.Fatal("ShouldColor with NO_COLOR set = true, want false")
	}
}

func TestShouldColor_NoColorEnvEmptyStringStillDisables(t *testing.T) {
	// no-color.org: presence is the signal, not truthiness — even an
	// empty value must still disable color.
	t.Setenv("NO_COLOR", "")
	var buf bytes.Buffer
	if ShouldColor(&buf, false) {
		t.Fatal("ShouldColor with NO_COLOR=\"\" (still present) = true, want false")
	}
}

func TestShouldColor_FalseForNonTTYWithNoOverrides(t *testing.T) {
	unsetenvForTest(t, "NO_COLOR")
	var buf bytes.Buffer
	if ShouldColor(&buf, false) {
		t.Fatal("ShouldColor(bytes.Buffer, no overrides) = true, want false — not a terminal")
	}
}

func TestColorHelpers_DisabledReturnsUnchanged(t *testing.T) {
	if got := Red(false, "x"); got != "x" {
		t.Fatalf("Red(false, %q) = %q, want unchanged", "x", got)
	}
	if got := Yellow(false, "x"); got != "x" {
		t.Fatalf("Yellow(false, %q) = %q, want unchanged", "x", got)
	}
	if got := Cyan(false, "x"); got != "x" {
		t.Fatalf("Cyan(false, %q) = %q, want unchanged", "x", got)
	}
}

func TestColorHelpers_EnabledWrapsInSGRAndReset(t *testing.T) {
	got := Red(true, "x")
	want := "\x1b[31mx\x1b[0m"
	if got != want {
		t.Fatalf("Red(true, %q) = %q, want %q", "x", got, want)
	}
}

func TestColorHelpers_DistinctCodes(t *testing.T) {
	red := Red(true, "x")
	yellow := Yellow(true, "x")
	cyan := Cyan(true, "x")
	if red == yellow || red == cyan || yellow == cyan {
		t.Fatalf("expected three distinct color codes, got red=%q yellow=%q cyan=%q", red, yellow, cyan)
	}
}
