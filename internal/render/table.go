package render

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode/utf8"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// maxTableColWidth and columnSampleSize implement §11.2/§11.4's column
// derivation and width rules.
const (
	maxTableColWidth = 40
	columnSampleSize = 1000
)

// Table renders records as an aligned text table via stdlib
// text/tabwriter.
//
// Honest limit: unlike Raw/JSONL, Table cannot stream row-by-row — the
// header must print before any data row, and the header depends on
// having seen the records first (§11.4: columns are derived from up to
// the first 1000 records). So Table buffers every record added, not just
// the first 1000; only the DERIVATION samples that far. This is
// documented in README, not hidden — it's the real tradeoff for a
// human-scannable table over streaming raw/jsonl passthrough.
type Table struct {
	recs []*eval.Record
}

func NewTable() *Table { return &Table{} }

func (t *Table) Add(rec *eval.Record) { t.recs = append(t.recs, rec) }

// Flush writes the header and every buffered record as an aligned table.
func (t *Table) Flush(w io.Writer) error {
	cols := deriveColumns(t.recs)
	rightAlign := deriveNumericColumns(t.recs, cols)

	// Precompute every cell (header + data), clamped. text/tabwriter's own
	// AlignRight flag is table-wide, not per-column, so mixed alignment
	// (numeric columns right-aligned, the rest left) has to be done by
	// hand: pre-pad right-aligned cells to their column's max width before
	// handing everything to tabwriter, so it sees already-uniform cells
	// for those columns and its own left-alignment padding does the rest.
	rows := make([][]string, 0, len(t.recs)+1)
	rows = append(rows, cols)
	for _, rec := range t.recs {
		row := make([]string, len(cols))
		for i, col := range cols {
			row[i] = clampCell(eval.CellString(rec.Get(col)))
		}
		rows = append(rows, row)
	}

	colWidth := make([]int, len(cols))
	for _, row := range rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > colWidth[i] {
				colWidth[i] = n
			}
		}
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				fmt.Fprint(tw, "\t")
			}
			if rightAlign[cols[i]] {
				cell = padLeft(cell, colWidth[i])
			}
			fmt.Fprint(tw, cell)
		}
		fmt.Fprintln(tw)
	}
	return tw.Flush()
}

// deriveColumns implements §11.4's column-name/order rule for passthrough
// output: the union of keys across the first min(len(recs), 1000)
// records, in first-seen order (scanning records in sequence, appending
// each newly-encountered key as it's first seen). The spec's phrase
// "first-seen order [...] then alphabetical" is read here as: first-seen
// order is the actual, fully deterministic rule — sequential scanning
// never produces a genuine tie needing an alphabetical tiebreak, so none
// is implemented. (When a `fields a,b.c` stage has already run, each
// record's own Keys() already IS exactly the projected column set in
// list order — this function still works unchanged in that case, since
// "union of first-seen keys" trivially equals those same keys.)
func deriveColumns(recs []*eval.Record) []string {
	sampleN := min(len(recs), columnSampleSize)

	seen := map[string]bool{}
	var cols []string
	for _, rec := range recs[:sampleN] {
		for _, k := range rec.Keys() {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	return cols
}

// deriveNumericColumns reports which columns should render right-aligned:
// every non-missing value seen for that column, across the same sample
// window as deriveColumns, is a Number.
func deriveNumericColumns(recs []*eval.Record, cols []string) map[string]bool {
	sampleN := min(len(recs), columnSampleSize)

	numericOnly := make(map[string]bool, len(cols))
	sawAny := make(map[string]bool, len(cols))
	for _, col := range cols {
		numericOnly[col] = true
	}
	for _, rec := range recs[:sampleN] {
		for _, col := range cols {
			v := rec.Get(col)
			if v.Kind == eval.KindMissing {
				continue
			}
			sawAny[col] = true
			if v.Kind != eval.KindNumber {
				numericOnly[col] = false
			}
		}
	}
	result := make(map[string]bool, len(cols))
	for _, col := range cols {
		result[col] = numericOnly[col] && sawAny[col]
	}
	return result
}

// clampCell truncates s to maxTableColWidth runes, appending "…" if
// truncated. Rune-count truncation, not display-width — CJK/wide glyphs
// aren't specially measured, so a column of wide characters will
// visually run wider than a Latin-text column at the "same" 40-rune
// clamp. A documented Honest Limit, not silently wrong.
func clampCell(s string) string {
	if utf8.RuneCountInString(s) <= maxTableColWidth {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxTableColWidth-1]) + "…"
}

func padLeft(s string, width int) string {
	n := utf8.RuneCountInString(s)
	if n >= width {
		return s
	}
	return strings.Repeat(" ", width-n) + s
}

// CellString has moved to eval.CellString — see its doc comment there for
// why (the canonical value-rendering rules "feed groups, table, csv" per
// §11.5, so they live in the shared lower layer, not here).
