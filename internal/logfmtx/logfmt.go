// Package logfmtx implements a hand-written logfmt decoder (§10) — a
// small state machine over Key/PreVal/Val/QVal/QEsc/AfterVal, not a
// regex-based or split-based parser, so it can report a positioned error
// exactly where a line breaks instead of just "malformed."
package logfmtx

import (
	"fmt"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// LineError is a positioned logfmt decode error — Offset is a byte offset
// within the line. Full file:line:col context is the caller's job (the
// pipeline already knows which physical line this is); the offset locates
// the problem within it.
type LineError struct {
	Offset int
	Msg    string
}

func (e *LineError) Error() string {
	return fmt.Sprintf("offset %d: %s", e.Offset, e.Msg)
}

// isKeyByte matches the key charset [A-Za-z0-9_\-.\/] (§10).
func isKeyByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '-' || c == '.' || c == '/'
}

// DecodeLine decodes one line of logfmt (k=v key1=value1 key2="quoted
// value" ...) into a Record. One bad line never affects siblings — the
// caller (the pipeline) handles that independence by calling DecodeLine
// once per line; this function only ever validates the one line it's
// given. A whitespace-only (or empty) line decodes to a valid, empty
// Record rather than an error — degenerate, not malformed.
func DecodeLine(line []byte) (*eval.Record, error) {
	rec := eval.NewRecord()
	i, n := 0, len(line)

	for i < n {
		i = skipSpaces(line, i)
		if i >= n {
			break
		}

		keyStart := i
		for i < n && isKeyByte(line[i]) {
			i++
		}

		switch {
		case i >= n || line[i] == ' ':
			return nil, &LineError{Offset: keyStart, Msg: "bare token without '=' (strict logfmt: free text belongs in plain format)"}
		case line[i] != '=':
			return nil, &LineError{Offset: i, Msg: fmt.Sprintf("invalid character %q in key", line[i])}
		}
		key := string(line[keyStart:i])
		if len(key) == 0 {
			return nil, &LineError{Offset: i, Msg: "empty key"}
		}
		i++ // consume '='

		val, next, err := scanValue(line, i)
		if err != nil {
			return nil, err
		}
		i = next

		rec.Set(key, eval.Str(val)) // last-wins, first-seen order — same Record semantics as JSON

		// AfterVal: whitespace or end of line must follow before the next
		// key. Only meaningfully checked for quoted values in practice —
		// an unquoted value's own scan already stops exactly at a space
		// or EOL — but a quoted value followed immediately by more
		// characters (e.g. `k="a"x=1`) is exactly the case this catches.
		if i < n && line[i] != ' ' {
			return nil, &LineError{Offset: i, Msg: "expected whitespace after value"}
		}
	}

	return rec, nil
}

func skipSpaces(line []byte, i int) int {
	for i < len(line) && line[i] == ' ' {
		i++
	}
	return i
}

// scanValue implements PreVal -> (Val | QVal/QEsc). An empty value (key=
// immediately followed by whitespace or EOL) is valid, decoding to "".
func scanValue(line []byte, i int) (val string, next int, err error) {
	n := len(line)
	if i >= n || line[i] == ' ' {
		return "", i, nil
	}
	if line[i] == '"' {
		return scanQuoted(line, i)
	}
	end := i
	for end < n && line[end] != ' ' {
		end++
	}
	return string(line[i:end]), end, nil
}

// scanQuoted implements QVal/QEsc: a "..." value supporting \" \\ \n \t
// escapes (§10). An unterminated quote at EOL is a line error positioned
// at the opening quote; an unrecognized escape is a line error positioned
// at the backslash.
func scanQuoted(line []byte, start int) (val string, next int, err error) {
	n := len(line)
	i := start + 1 // consume opening quote
	var sb []byte

	for {
		if i >= n {
			return "", 0, &LineError{Offset: start, Msg: "unterminated quoted value"}
		}
		switch c := line[i]; c {
		case '"':
			return string(sb), i + 1, nil
		case '\\':
			if i+1 >= n {
				return "", 0, &LineError{Offset: start, Msg: "unterminated quoted value"}
			}
			switch e := line[i+1]; e {
			case '"':
				sb = append(sb, '"')
			case '\\':
				sb = append(sb, '\\')
			case 'n':
				sb = append(sb, '\n')
			case 't':
				sb = append(sb, '\t')
			default:
				return "", 0, &LineError{Offset: i, Msg: fmt.Sprintf("unknown escape sequence \\%c in quoted value", e)}
			}
			i += 2
		default:
			sb = append(sb, c)
			i++
		}
	}
}
