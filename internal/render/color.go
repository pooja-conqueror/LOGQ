package render

import (
	"io"
	"os"
)

// SGR (ANSI Select Graphic Rendition) codes — the small, fixed palette
// this project actually uses. Hand-rolled rather than a color library:
// this is a handful of escape-sequence constants, not a real problem a
// dependency solves.
const (
	sgrReset  = "\x1b[0m"
	sgrRed    = "\x1b[31m"
	sgrYellow = "\x1b[33m"
	sgrCyan   = "\x1b[36m"
)

// IsTTY reports whether w is connected to a terminal — the standard
// stdlib-only technique (no dependency, no cgo, no isatty syscall
// binding): a terminal is a character device, and *os.File.Stat().Mode()
// exposes that portably via the os.ModeCharDevice bit. w must actually
// be an *os.File for this to mean anything at all; any other io.Writer
// (a bytes.Buffer in tests, anything wrapping a real file) is never a
// tty, which is also always the right answer for those.
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ShouldColor decides whether ANSI color should be used at all,
// combining every source of truth in the documented precedence order:
//  1. an explicit --no-color flag always wins outright;
//  2. NO_COLOR (https://no-color.org: "just check if the NO_COLOR
//     environment variable is present," any value at all, including
//     empty — presence is the whole signal, not truthiness);
//  3. otherwise, color is used only when w is actually a terminal —
//     coloring a redirected file or a pipe would corrupt anything
//     downstream trying to parse it (or just look ugly in a log file).
func ShouldColor(w io.Writer, noColorFlag bool) bool {
	if noColorFlag {
		return false
	}
	if _, present := os.LookupEnv("NO_COLOR"); present {
		return false
	}
	return IsTTY(w)
}

// colorize wraps s in the given SGR code, or returns s unchanged when
// enabled is false — the one place every color helper funnels through,
// so "don't colorize" is always a single boolean check away, never a
// scattered set of if/else branches duplicated at each call site.
func colorize(enabled bool, code, s string) string {
	if !enabled {
		return s
	}
	return code + s + sgrReset
}

// Red/Yellow/Cyan are logq's whole palette (§ "small, fixed" above) —
// used respectively for alarming counts (malformed lines), a
// still-notable-but-not-alarming status (PARTIAL, an interrupted run),
// and informational status (SNAPSHOT, a watch-mode poll marker).
func Red(enabled bool, s string) string    { return colorize(enabled, sgrRed, s) }
func Yellow(enabled bool, s string) string { return colorize(enabled, sgrYellow, s) }
func Cyan(enabled bool, s string) string   { return colorize(enabled, sgrCyan, s) }
