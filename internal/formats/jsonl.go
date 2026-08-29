// Package formats implements logq's hand-written log format decoders —
// JSONL, plain text (Phase 5), and the auto-detection cascade between them
// (Phase 5). No parsing/serialization package anywhere in this codebase.
package formats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// DefaultMaxDepth is the spec's default JSON nesting cap (§9.3): a JSON
// document nested deeper than this is treated as a malformed line, never a
// panic or a silent truncation. Overridable via --max-depth (Phase 8).
const DefaultMaxDepth = 32

// ErrNotAnObject is returned when the top-level JSON value on a line isn't
// an object — JSONL requires exactly one JSON object per line. Exported so
// the format auto-detector (Phase 5) can distinguish "this line isn't
// JSONL at all" from a genuinely malformed JSON object.
var ErrNotAnObject = errors.New("top-level JSON value is not an object")

// DecodeResult holds a successfully decoded record plus counters the
// caller aggregates into the end-of-run summary (Phase 10's --on-error /
// stderr counters).
type DecodeResult struct {
	Record  *eval.Record
	DupKeys int // fields that appeared more than once in this line (last wins)
}

// DecodeLine decodes one line of JSONL into a Record.
//
// Three things this does deliberately, per spec (§9.3):
//
//   - Key order is preserved (decoding via json.Decoder.Token() into
//     eval.Record's ordered map, not json.Unmarshal into map[string]any,
//     which is unordered and would lose source document order entirely).
//   - Numbers use json.Number discipline: int64 when losslessly
//     representable as one, float64 otherwise — this is exactly what
//     avoids the classic bug where decoding straight to float64 silently
//     corrupts a large integer (e.g. a snowflake ID) that doesn't fit a
//     float64 mantissa exactly.
//   - Duplicate keys resolve last-wins (matching eval.Record.Set) with the
//     count surfaced via DecodeResult.DupKeys rather than silently
//     dropped.
//
// maxDepth bounds nesting (objects and arrays both count; the top-level
// object is depth 1) — exceeding it is a malformed-line error, enforced
// during decode, not discovered after the fact. Trailing bytes after the
// object (anything but trailing whitespace) are also a malformed-line
// error, not silently ignored.
func DecodeLine(line []byte, maxDepth int) (DecodeResult, error) {
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return DecodeResult{}, fmt.Errorf("malformed line: %w", err)
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return DecodeResult{}, ErrNotAnObject
	}

	rec, dupKeys, err := decodeObjectBody(dec, 1, maxDepth)
	if err != nil {
		return DecodeResult{}, err
	}

	if extra, err := dec.Token(); err != io.EOF {
		if err != nil {
			return DecodeResult{}, fmt.Errorf("malformed line: %w", err)
		}
		return DecodeResult{}, fmt.Errorf("malformed line: trailing data after JSON object: %v", extra)
	}

	return DecodeResult{Record: rec, DupKeys: dupKeys}, nil
}

// decodeObjectBody decodes an object's key/value pairs, assuming the
// caller already consumed the opening '{'; it consumes the matching '}'.
// depth is this object's own nesting depth (top level = 1).
func decodeObjectBody(dec *json.Decoder, depth, maxDepth int) (*eval.Record, int, error) {
	if depth > maxDepth {
		return nil, 0, fmt.Errorf("malformed line: nesting depth exceeds max-depth %d", maxDepth)
	}
	rec := eval.NewRecord()
	seen := map[string]bool{}
	dupKeys := 0

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, 0, fmt.Errorf("malformed line: %w", err)
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, 0, fmt.Errorf("malformed line: expected a string object key, got %v", keyTok)
		}
		if seen[key] {
			dupKeys++
		}
		seen[key] = true

		val, nestedDup, err := decodeValue(dec, depth, maxDepth)
		if err != nil {
			return nil, 0, err
		}
		dupKeys += nestedDup
		rec.Set(key, val) // last-wins, first-seen order preserved (eval.Record.Set)
	}

	if _, err := dec.Token(); err != nil { // closing '}'
		return nil, 0, fmt.Errorf("malformed line: %w", err)
	}
	return rec, dupKeys, nil
}

// decodeArrayBody decodes array elements, assuming the caller already
// consumed the opening '['; it consumes the matching ']'.
func decodeArrayBody(dec *json.Decoder, depth, maxDepth int) ([]eval.Value, int, error) {
	if depth > maxDepth {
		return nil, 0, fmt.Errorf("malformed line: nesting depth exceeds max-depth %d", maxDepth)
	}
	var vals []eval.Value
	dupKeys := 0
	for dec.More() {
		v, nestedDup, err := decodeValue(dec, depth, maxDepth)
		if err != nil {
			return nil, 0, err
		}
		dupKeys += nestedDup
		vals = append(vals, v)
	}
	if _, err := dec.Token(); err != nil { // closing ']'
		return nil, 0, fmt.Errorf("malformed line: %w", err)
	}
	return vals, dupKeys, nil
}

// decodeValue decodes exactly one JSON value (scalar, object, or array).
func decodeValue(dec *json.Decoder, depth, maxDepth int) (eval.Value, int, error) {
	tok, err := dec.Token()
	if err != nil {
		return eval.Value{}, 0, fmt.Errorf("malformed line: %w", err)
	}

	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			rec, dup, err := decodeObjectBody(dec, depth+1, maxDepth)
			if err != nil {
				return eval.Value{}, 0, err
			}
			return eval.Object(rec), dup, nil
		case '[':
			arr, dup, err := decodeArrayBody(dec, depth+1, maxDepth)
			if err != nil {
				return eval.Value{}, 0, err
			}
			return eval.Array(arr), dup, nil
		default:
			return eval.Value{}, 0, fmt.Errorf("malformed line: unexpected delimiter %v", v)
		}
	case string:
		return eval.Str(v), 0, nil
	case json.Number:
		return numberFromJSONNumber(v), 0, nil
	case bool:
		return eval.Bool(v), 0, nil
	case nil:
		return eval.Null, 0, nil
	default:
		return eval.Value{}, 0, fmt.Errorf("malformed line: unexpected token %v", tok)
	}
}

// numberFromJSONNumber implements the spec's json.Number discipline:
// integer when the literal is losslessly parseable as int64, float64
// otherwise. This — not decoding straight to float64 — is what prevents a
// large integer (e.g. a snowflake ID) from silently losing precision on
// its way through a float64 mantissa.
func numberFromJSONNumber(n json.Number) eval.Value {
	if i, err := n.Int64(); err == nil {
		return eval.Int(i)
	}
	f, err := n.Float64()
	if err != nil {
		// Syntactically valid JSON the decoder still couldn't represent as
		// float64 (e.g. a literal outside float64's exponent range) — an
		// extremely rare, pathological case. Treat as Null rather than
		// fail the whole line over one unrepresentable field.
		return eval.Null
	}
	return eval.Float(f)
}
