package pipeline

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// Fields projects a record down to just the listed paths (§7: "Projection.
// Later stages see projected shape."). A path that doesn't resolve
// becomes MISSING and is simply not included in the output record —
// projecting a nonexistent field never manufactures one that was never
// there.
type Fields struct {
	Paths []*query.PathRef
}

// NewFields validates and builds a Fields stage from the parsed
// FieldsStage AST. Per S-8, two paths that derive the same output column
// name are a compile-time error — validated once here, before any record
// is ever read, not discovered partway through a run.
func NewFields(fs *query.FieldsStage) (*Fields, error) {
	seen := map[string]bool{}
	for _, p := range fs.Paths {
		key := pathKey(p)
		if seen[key] {
			return nil, fmt.Errorf("duplicate output column %q in fields stage", key)
		}
		seen[key] = true
	}
	return &Fields{Paths: fs.Paths}, nil
}

func (f *Fields) Process(rec *eval.Record) (*eval.Record, bool, bool) {
	out := eval.NewRecord()
	for _, p := range f.Paths {
		v := rec.Resolve(p)
		if v.Kind == eval.KindMissing {
			continue
		}
		out.Set(pathKey(p), v)
	}
	return out, true, false
}

// pathKey renders a PathRef back to its flat dotted/bracket text form —
// e.g. b.c, items[0] — used as the projected field's output key, matching
// §11.4's column-naming rule: "fields a,b.c: columns literally a, b.c."
func pathKey(p *query.PathRef) string {
	var sb strings.Builder
	for i, seg := range p.Segs {
		if seg.IsIndex {
			sb.WriteByte('[')
			sb.WriteString(strconv.FormatInt(seg.Index, 10))
			sb.WriteByte(']')
			continue
		}
		if i > 0 {
			sb.WriteByte('.')
		}
		sb.WriteString(seg.Ident)
	}
	return sb.String()
}
