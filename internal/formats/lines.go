package formats

import (
	"bufio"
	"bytes"
	"io"
)

// DefaultMaxLine is the spec's default oversize-line cutoff (§9.1). A line
// exceeding whatever max is configured is skipped whole and counted —
// never silently truncated, and never fatal to the rest of the stream.
// --max-line (Phase 8) can raise this up to 16MB.
const DefaultMaxLine = 1 << 20 // 1MB

// bomBytes is the UTF-8 byte-order mark, stripped once at true stream
// start if present (§9.1).
var bomBytes = []byte{0xEF, 0xBB, 0xBF}

// LineReader splits a stream into lines on '\n' only (§9.1):
//
//   - a trailing '\r' immediately before '\n' is stripped (CRLF
//     normalization); a lone '\r' anywhere else in the line is ordinary
//     data and is left alone — this reader only ever splits on '\n', so no
//     other '\r' byte is ever a stripping candidate in the first place;
//   - a UTF-8 BOM is stripped once, at true stream start, never again;
//   - the final line is returned even without a trailing newline;
//   - empty lines are skipped (never returned) but counted;
//   - a line exceeding MaxLine is skipped whole and counted, and reading
//     resumes cleanly at the following line — this deliberately does NOT
//     use bufio.Scanner's default behavior, which on an oversized token
//     just stops scanning for good (Scan returns false, Err() holds
//     ErrTooLong) unless the caller both raises the buffer AND checks
//     Err() — the well-known "64KB MaxScanTokenSize trap." Built directly
//     on bufio.Reader instead, specifically to skip-and-continue rather
//     than silently stopping mid-file.
type LineReader struct {
	br          *bufio.Reader
	MaxLine     int
	bomStripped bool

	EmptyLines     int
	OversizedLines int
}

// NewLineReader wraps r. maxLine <= 0 means DefaultMaxLine.
func NewLineReader(r io.Reader, maxLine int) *LineReader {
	if maxLine <= 0 {
		maxLine = DefaultMaxLine
	}
	return &LineReader{br: bufio.NewReaderSize(r, 64*1024), MaxLine: maxLine}
}

// ReadLine returns the next non-empty line, BOM- and CRLF-stripped. It
// follows the same convention as io.Reader: a final line may be returned
// together with err == io.EOF in the same call — callers should still use
// that line before stopping. A pure io.EOF with no line (line == nil)
// means the stream is fully exhausted.
//
// The returned slice is always freshly allocated (accumulateOneLine builds
// it via append onto a nil slice, never aliasing bufio.Reader's own
// internal buffer) — safe to retain across calls without a defensive
// copy, e.g. to buffer several lines at once for format auto-detection.
func (lr *LineReader) ReadLine() ([]byte, error) {
	for {
		line, err := lr.readRawLine()
		if line == nil && err != nil {
			return nil, err
		}
		line = stripTrailingCR(line)
		if len(line) == 0 {
			lr.EmptyLines++
			if err != nil {
				return nil, err
			}
			continue
		}
		return line, err
	}
}

func stripTrailingCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

func (lr *LineReader) ensureBOMStripped() {
	if lr.bomStripped {
		return
	}
	lr.bomStripped = true
	peeked, _ := lr.br.Peek(len(bomBytes))
	if bytes.Equal(peeked, bomBytes) {
		_, _ = lr.br.Discard(len(bomBytes))
	}
}

// readRawLine returns the next line (delimiter stripped, CR not yet
// stripped) or io.EOF. It resyncs past any oversized line iteratively
// (never recursively — a file consisting of many consecutive oversized
// lines must not grow the call stack per line).
func (lr *LineReader) readRawLine() ([]byte, error) {
	lr.ensureBOMStripped()
	for {
		buf, lineErr, oversized := lr.accumulateOneLine()
		if oversized {
			lr.OversizedLines++
			if lineErr != nil {
				return nil, lineErr // stream ended during/right after an oversized line
			}
			continue // resync onto the next line
		}
		if lineErr != nil && len(buf) == 0 {
			return nil, lineErr
		}
		return buf, lineErr
	}
}

// accumulateOneLine reads up to one '\n'-delimited line (or a final
// unterminated line at EOF), bounded to MaxLine bytes of *content* — the
// delimiter itself doesn't count against the limit, and is stripped from
// the returned buf before the size check (ReadSlice includes the '\n' in
// its returned bytes, so comparing the untrimmed length would off-by-one a
// line with exactly MaxLine bytes of real content). If the line would
// exceed MaxLine, accumulation stops at that point and the remainder is
// discarded up to the next '\n' (or EOF) without ever buffering it in
// full — oversized is true and buf is not a usable partial line in that
// case.
func (lr *LineReader) accumulateOneLine() (buf []byte, lineErr error, oversized bool) {
	for {
		chunk, err := lr.br.ReadSlice('\n')
		buf = append(buf, chunk...)

		switch err {
		case nil:
			content := bytes.TrimSuffix(buf, []byte{'\n'})
			if len(content) > lr.MaxLine {
				return nil, nil, true
			}
			return content, nil, false

		case bufio.ErrBufferFull:
			if len(buf) > lr.MaxLine {
				discardErr := lr.discardUntilNewline()
				return nil, discardErr, true
			}
			continue // still under budget; ReadSlice's internal buffer just needs another pass

		default: // io.EOF or a genuine read error, no trailing '\n' found
			if len(buf) > lr.MaxLine {
				return nil, err, true
			}
			return buf, err, false
		}
	}
}

// discardUntilNewline consumes bytes without buffering them until the next
// '\n' or a read error/EOF, so an oversized line's remainder never has to
// be held in memory.
func (lr *LineReader) discardUntilNewline() error {
	for {
		_, err := lr.br.ReadSlice('\n')
		if err == nil {
			return nil
		}
		if err != bufio.ErrBufferFull {
			return err
		}
	}
}
