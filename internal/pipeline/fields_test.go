package pipeline

import (
	"testing"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
	"github.com/pooja-conqueror/LOGQ/internal/query"
)

// mustFields parses src (a full "| fields ..." query tail) and builds a
// real Fields stage from it — exercising the actual parser -> pipeline
// path, not a hand-built AST.
func mustFields(t *testing.T, src string) *Fields {
	t.Helper()
	q, err := query.ParseQuery(src)
	if err != nil {
		t.Fatalf("ParseQuery(%q) error = %v", src, err)
	}
	fs, err := NewFields(q.Stages[0].(*query.FieldsStage))
	if err != nil {
		t.Fatalf("NewFields error = %v", err)
	}
	return fs
}

func testRecord() *eval.Record {
	inner := eval.NewRecord()
	inner.Set("path", eval.Str("/api/x"))
	rec := eval.NewRecord()
	rec.Set("msg", eval.Str("hello"))
	rec.Set("url", eval.Object(inner))
	rec.Set("items", eval.Array([]eval.Value{eval.Str("a"), eval.Str("b")}))
	return rec
}

func TestFields_SinglePath(t *testing.T) {
	fs := mustFields(t, `| fields msg`)
	out, keep, done := fs.Process(testRecord())
	if !keep || done {
		t.Fatalf("keep=%v done=%v, want true,false", keep, done)
	}
	if out.Len() != 1 || out.Get("msg").S != "hello" {
		t.Fatalf("out = %+v", out)
	}
}

func TestFields_MultiplePathsPreserveListOrder(t *testing.T) {
	fs := mustFields(t, `| fields items, msg`)
	out, _, _ := fs.Process(testRecord())
	keys := out.Keys()
	if len(keys) != 2 || keys[0] != "items" || keys[1] != "msg" {
		t.Fatalf("Keys() = %v, want [items msg] (fields-list order, not source order)", keys)
	}
}

func TestFields_NestedPathFlattensToTextKey(t *testing.T) {
	fs := mustFields(t, `| fields url.path`)
	out, _, _ := fs.Process(testRecord())
	if out.Len() != 1 {
		t.Fatalf("Len() = %d, want 1", out.Len())
	}
	if got := out.Get("url.path"); got.S != "/api/x" {
		t.Fatalf(`Get("url.path") = %+v, want "/api/x"`, got)
	}
	// The original nested shape must not survive — it's flattened.
	if out.Get("url").Kind != eval.KindMissing {
		t.Fatal("the flat key must replace the nested structure, not sit alongside it")
	}
}

func TestFields_IndexedPathFlattensToBracketKey(t *testing.T) {
	fs := mustFields(t, `| fields items[0]`)
	out, _, _ := fs.Process(testRecord())
	if got := out.Get("items[0]"); got.S != "a" {
		t.Fatalf(`Get("items[0]") = %+v, want "a"`, got)
	}
}

func TestFields_MissingPathNotIncluded(t *testing.T) {
	fs := mustFields(t, `| fields msg, nope`)
	out, keep, _ := fs.Process(testRecord())
	if !keep {
		t.Fatal("a record with one missing projected field must still be kept")
	}
	if out.Len() != 1 {
		t.Fatalf("Len() = %d, want 1 — the missing field must not appear at all", out.Len())
	}
	if out.Get("nope").Kind != eval.KindMissing {
		t.Fatal("projecting a nonexistent field must not manufacture one")
	}
}

func TestFields_AllPathsMissingStillKeepsEmptyRecord(t *testing.T) {
	fs := mustFields(t, `| fields nope1, nope2`)
	out, keep, done := fs.Process(testRecord())
	if !keep || done {
		t.Fatalf("keep=%v done=%v, want true,false — fields is a transform, never a filter", keep, done)
	}
	if out.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", out.Len())
	}
}

func TestNewFields_DuplicatePathRejected(t *testing.T) {
	q, err := query.ParseQuery(`| fields a, a`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	_, err = NewFields(q.Stages[0].(*query.FieldsStage))
	if err == nil {
		t.Fatal("NewFields should reject 'fields a, a' — duplicate output column")
	}
}

func TestNewFields_DuplicateViaQuotingStillRejected(t *testing.T) {
	// a.b and a."b" render to the identical flat key "a.b" even though
	// they're written differently — still a real collision.
	q, err := query.ParseQuery(`| fields a.b, a."b"`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	_, err = NewFields(q.Stages[0].(*query.FieldsStage))
	if err == nil {
		t.Fatal("NewFields should reject two paths that render to the same output column")
	}
}

func TestNewFields_DistinctPathsAccepted(t *testing.T) {
	q, err := query.ParseQuery(`| fields a, b, c.d`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if _, err := NewFields(q.Stages[0].(*query.FieldsStage)); err != nil {
		t.Fatalf("NewFields error = %v, want no error for distinct paths", err)
	}
}
