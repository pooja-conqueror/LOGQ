package render

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/pooja-conqueror/LOGQ/internal/eval"
)

// JSONL writes rec to w as a single line of JSON, followed by '\n'.
//
// Field order is preserved exactly as in rec — encoding/json.Marshal on a
// Go map would silently re-sort keys alphabetically, which would quietly
// defeat the entire point of Phase 4's ordered-map decoder. Object
// serialization is therefore hand-written, walking rec.Keys() in order;
// individual scalar values are still produced via encoding/json.Marshal
// where it's the right tool (string escaping in particular — quotes,
// backslashes, control characters, unicode — is exactly what stdlib
// already gets right, no reason to hand-roll that specific subroutine).
func JSONL(w io.Writer, rec *eval.Record) error {
	var buf bytes.Buffer
	if err := writeObject(&buf, rec); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

func writeObject(buf *bytes.Buffer, rec *eval.Record) error {
	buf.WriteByte('{')
	for i, key := range rec.Keys() {
		if i > 0 {
			buf.WriteByte(',')
		}
		keyJSON, err := json.Marshal(key)
		if err != nil {
			return err
		}
		buf.Write(keyJSON)
		buf.WriteByte(':')
		if err := writeValue(buf, rec.Get(key)); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

func writeValue(buf *bytes.Buffer, v eval.Value) error {
	switch v.Kind {
	case eval.KindNull:
		buf.WriteString("null")
		return nil
	case eval.KindMissing:
		// §11.5: MISSING -> omitted key in jsonl. writeObject only ever
		// calls writeValue for a key actually present in rec, so this is
		// unreachable via normal object serialization — it exists so a
		// MISSING inside an Array (which has no "key" to omit) still
		// serializes to something well-formed instead of panicking.
		buf.WriteString("null")
		return nil
	case eval.KindBool:
		if v.B {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
		return nil
	case eval.KindNumber:
		buf.WriteString(NumberString(v))
		return nil
	case eval.KindString:
		s, err := json.Marshal(v.S)
		if err != nil {
			return err
		}
		buf.Write(s)
		return nil
	case eval.KindTimestamp:
		s, err := json.Marshal(TimestampString(v))
		if err != nil {
			return err
		}
		buf.Write(s)
		return nil
	case eval.KindDuration:
		s, err := json.Marshal(DurationString(v))
		if err != nil {
			return err
		}
		buf.Write(s)
		return nil
	case eval.KindArray:
		buf.WriteByte('[')
		for i, elem := range v.Arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeValue(buf, elem); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
		return nil
	case eval.KindObject:
		return writeObject(buf, v.Obj)
	default:
		buf.WriteString("null")
		return nil
	}
}
