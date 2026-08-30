package render

import (
	"encoding/csv"
	"io"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// CSV renders records as RFC 4180 CSV via stdlib encoding/csv, which
// already correctly implements the format's quoting/escaping rules
// (quote-on-need for comma/quote/CR-LF, "" escaping, \r\n terminators) —
// no craft gap here worth hand-rolling around, unlike jsonl's
// ordered-decode problem or logfmt's total absence of any formal grammar.
//
// Same buffering tradeoff as Table, for the same reason (the header must
// print before any row, and depends on having seen the records first).
type CSV struct {
	recs []*eval.Record
}

func NewCSV() *CSV { return &CSV{} }

func (c *CSV) Add(rec *eval.Record) { c.recs = append(c.recs, rec) }

// Flush writes the header and every buffered record to w. Per §11.3,
// MISSING and Null both render as an empty cell — the MISSING-vs-Null
// distinction jsonl output preserves is deliberately lost here; use
// -o jsonl instead when that distinction matters.
func (c *CSV) Flush(w io.Writer) error {
	cols := deriveColumns(c.recs) // alignment is irrelevant to CSV; only column order matters

	cw := csv.NewWriter(w)
	// encoding/csv defaults to bare '\n' terminators — RFC 4180 (and this
	// project's own §11.3) calls for '\r\n', which is NOT the default and
	// has to be requested explicitly.
	cw.UseCRLF = true
	if err := cw.Write(cols); err != nil {
		return err
	}
	for _, rec := range c.recs {
		row := make([]string, len(cols))
		for i, col := range cols {
			row[i] = csvCellString(rec.Get(col))
		}
		if err := cw.Write(row); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// csvCellString is CellString with both MISSING and Null remapped to an
// empty cell — CellString's "(missing)"/"null" labels are table-specific.
func csvCellString(v eval.Value) string {
	if v.Kind == eval.KindMissing || v.Kind == eval.KindNull {
		return ""
	}
	return eval.CellString(v)
}
