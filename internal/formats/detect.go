package formats

import (
	"io"

	"github.com/pooja-conqueror/LOGQ/internal/logfmtx"
)

// Format identifies which decoder a source should be read with.
type Format int

const (
	FormatJSONL Format = iota
	FormatLogfmt
	FormatPlain
)

func (f Format) String() string {
	switch f {
	case FormatJSONL:
		return "jsonl"
	case FormatLogfmt:
		return "logfmt"
	case FormatPlain:
		return "plain"
	default:
		return "unknown"
	}
}

// DetectSampleSize is the spec's default sample window (§9.2): the first
// this-many non-empty lines decide the format for an entire source.
const DetectSampleSize = 64

// Detect runs the deterministic cascade (§9.2) over an already-collected
// sample of non-empty lines (see DetectFromReader for collecting one):
//
//  1. every line parses strictly as a JSON object -> FormatJSONL
//  2. else every line parses as logfmt with at least one key=value pair
//     and zero errors -> FormatLogfmt
//  3. else -> FormatPlain
//
// No heuristics/fuzzy scoring — a single line that doesn't fit disqualifies
// that format outright, deterministically. An empty sample (a source with
// no non-empty lines at all) defaults to FormatPlain: there's nothing to
// detect, and plain is always valid since it can't fail to decode.
func Detect(sample [][]byte) Format {
	if len(sample) == 0 {
		return FormatPlain
	}

	allJSON := true
	for _, line := range sample {
		if _, err := DecodeLine(line, DefaultMaxDepth); err != nil {
			allJSON = false
			break
		}
	}
	if allJSON {
		return FormatJSONL
	}

	allLogfmt := true
	for _, line := range sample {
		rec, err := logfmtx.DecodeLine(line)
		if err != nil || rec.Len() == 0 {
			// rec.Len() == 0 covers a whitespace-only line, which logfmtx
			// treats as a valid-but-empty record rather than an error —
			// §9.2 requires at least one real key=value pair for a line to
			// count as looking like logfmt.
			allLogfmt = false
			break
		}
	}
	if allLogfmt {
		return FormatLogfmt
	}

	return FormatPlain
}

// DetectFromReader collects up to DetectSampleSize non-empty lines from
// lr, runs Detect over them, and returns both the verdict and the exact
// lines already consumed. The caller MUST process those returned lines
// itself before reading lr further — they can't be "un-read" from the
// underlying stream, so returning them is how the caller avoids losing
// the sampled prefix of the source.
func DetectFromReader(lr *LineReader) (Format, [][]byte, error) {
	sample := make([][]byte, 0, DetectSampleSize)
	for len(sample) < DetectSampleSize {
		line, err := lr.ReadLine()
		if line != nil {
			sample = append(sample, line) // safe to retain — see ReadLine's doc comment
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return FormatPlain, sample, err
		}
	}
	return Detect(sample), sample, nil
}
