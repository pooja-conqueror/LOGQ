// Package render implements logq's output renderers — raw passthrough and
// jsonl now, table and csv in Phase 7 (commit 26).
package render

import "io"

// Raw writes line verbatim to w, followed by a single '\n'.
//
// Per §11.6's losslessness axiom, line must be the ORIGINAL bytes as read
// from the source (the line reader's post-CRLF-strip form only) — never a
// re-serialization through the parsed Record, which could reorder fields
// or reformat numbers/strings differently than the source line ever did.
// Filtering a file through logq with `-o raw` (the default for
// passthrough queries) must round-trip byte-perfectly for every matched
// line: `logq 'x' f.log > filtered.log` reproduces exactly the matched
// lines, byte for byte.
func Raw(w io.Writer, line []byte) error {
	if _, err := w.Write(line); err != nil {
		return err
	}
	_, err := w.Write([]byte{'\n'})
	return err
}
