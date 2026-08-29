package formats

import "github.com/pooja-conqueror/LOGQ/internal/eval"

// DecodePlainLine wraps a whole line as a Record with a single `.msg`
// field, enabling `~` regex filtering (and any other query) against
// unstructured log text that has no key=value or JSON structure at all.
// Unlike DecodeLine (JSONL) and logfmtx.DecodeLine, this can never fail —
// a plain line isn't parsed at all, just wrapped, so there's no malformed
// case to report.
func DecodePlainLine(line []byte) *eval.Record {
	rec := eval.NewRecord()
	rec.Set("msg", eval.Str(string(line)))
	return rec
}
