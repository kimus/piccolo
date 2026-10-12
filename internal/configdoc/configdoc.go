// Package configdoc reads and merge-patches PicoClaw config.json while
// preserving user-owned content: unknown keys, their original ordering, and
// untouched subtrees are kept byte-identical wherever possible. Piccolo
// NEVER round-trips config.json through its subset model structs here.
package configdoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Value is any JSON value in a document. Objects are *Doc, arrays are
// []Value, numbers are json.Number, everything else is the Go primitive.
type Value any

// Doc is a JSON object that remembers key order.
type Doc struct {
	keys   []string
	values map[string]Value
}

// NewDoc returns an empty ordered object.
func NewDoc() *Doc {
	return &Doc{values: map[string]Value{}}
}

// Keys returns the object keys in document order.
func (d *Doc) Keys() []string { return d.keys }

// Get returns the raw value stored under key.
func (d *Doc) Get(key string) (Value, bool) {
	v, ok := d.values[key]
	return v, ok
}

// GetObject returns key as an object if it is one.
func (d *Doc) GetObject(key string) (*Doc, bool) {
	v, ok := d.values[key]
	o, ok2 := v.(*Doc)
	return o, ok && ok2
}

// Set inserts or updates key, preserving position on update.
func (d *Doc) Set(key string, v Value) {
	if _, exists := d.values[key]; !exists {
		d.keys = append(d.keys, key)
	}
	d.values[key] = v
}

// Delete removes a key if present.
func (d *Doc) Delete(key string) {
	if _, exists := d.values[key]; !exists {
		return
	}
	delete(d.values, key)
	for i, k := range d.keys {
		if k == key {
			d.keys = append(d.keys[:i], d.keys[i+1:]...)
			break
		}
	}
}

// Load parses an ordered config.json from disk.
func Load(path string) (*Doc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse builds a Doc from JSON bytes; the root must be an object.
func Parse(data []byte) (*Doc, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("config.json root is not a JSON object")
	}
	d := &Doc{values: map[string]Value{}}
	if err := decodeInto(dec, d); err != nil {
		return nil, err
	}
	return d, nil
}

// decodeInto fills an existing Doc from a decoder positioned after '{'.
func decodeInto(dec *json.Decoder, d *Doc) error {
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("expected object key, got %T", keyTok)
		}
		v, err := decodeValue(dec)
		if err != nil {
			return err
		}
		d.Set(key, v)
	}
	_, err := dec.Token() // consume '}'
	return err
}

func decodeValue(dec *json.Decoder) (Value, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			d := &Doc{values: map[string]Value{}}
			if err := decodeInto(dec, d); err != nil {
				return nil, err
			}
			return d, nil
		case '[':
			var arr []Value
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // ']'
				return nil, err
			}
			if arr == nil {
				arr = []Value{}
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected delimiter %v", t)
		}
	default:
		return t, nil
	}
}

// Marshal serializes the document with 2-space indent and a trailing newline.
func (d *Doc) Marshal() []byte {
	var buf bytes.Buffer
	d.write(&buf, 0)
	buf.WriteByte('\n')
	return buf.Bytes()
}

func (d *Doc) write(buf *bytes.Buffer, depth int) {
	pad := bytes.Repeat([]byte("  "), depth+1)
	closePad := bytes.Repeat([]byte("  "), depth)
	buf.WriteByte('{')
	for i, k := range d.keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
		buf.Write(pad)
		buf.WriteString(jsonString(k))
		buf.WriteString(": ")
		writeValue(buf, d.values[k], depth+1)
	}
	if len(d.keys) > 0 {
		buf.WriteByte('\n')
		buf.Write(closePad)
	}
	buf.WriteByte('}')
}

func writeValue(buf *bytes.Buffer, v Value, depth int) {
	switch t := v.(type) {
	case *Doc:
		t.write(buf, depth)
	case []Value:
		if len(t) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteByte('[')
		pad := bytes.Repeat([]byte("  "), depth+1)
		closePad := bytes.Repeat([]byte("  "), depth)
		for i, el := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
			buf.Write(pad)
			writeValue(buf, el, depth+1)
		}
		buf.WriteByte('\n')
		buf.Write(closePad)
		buf.WriteByte(']')
	case json.Number:
		buf.WriteString(t.String())
	case string:
		buf.WriteString(jsonString(t))
	case bool:
		buf.WriteString(strconv.FormatBool(t))
	case nil:
		buf.WriteString("null")
	default:
		// Fallback: standard encoder (should not happen for parsed docs).
		raw, err := json.Marshal(t)
		if err != nil {
			buf.WriteString("null")
			return
		}
		buf.Write(raw)
	}
}

func jsonString(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// GetString returns a string value at key.
func (d *Doc) GetString(key string) (string, bool) {
	v, ok := d.values[key]
	s, ok2 := v.(string)
	return s, ok && ok2
}

// GetBool returns a bool value at key.
func (d *Doc) GetBool(key string) (bool, bool) {
	v, ok := d.values[key]
	b, ok2 := v.(bool)
	return b, ok && ok2
}

// GetStringList returns an array-of-strings value at key.
func (d *Doc) GetStringList(key string) ([]string, bool) {
	v, exists := d.values[key]
	arr, ok2 := v.([]Value)
	if !exists || !ok2 {
		return nil, false
	}
	var out []string
	for _, el := range arr {
		s, ok := el.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// StrList converts strings to a Doc array value.
func StrList(items ...string) []Value {
	arr := make([]Value, 0, len(items))
	for _, s := range items {
		arr = append(arr, s)
	}
	return arr
}

// Obj builds a Doc from ordered key/value pairs.
func Obj(pairs ...any) *Doc {
	d := NewDoc()
	for i := 0; i+1 < len(pairs); i += 2 {
		d.Set(pairs[i].(string), pairs[i+1])
	}
	return d
}

// Write atomically writes the document, first backing up the existing file
// (if any) to <path>.bak. Mode mirrors the original file, defaulting 0644.
func Write(path string, d *Doc) error {
	mode := os.FileMode(0o644)
	var original []byte
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
		var err error
		original, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s for backup: %w", path, err)
		}
	}
	if original != nil {
		bak := path + ".bak"
		if err := os.WriteFile(bak, original, mode); err != nil {
			return fmt.Errorf("write %s: %w", filepath.Base(bak), err)
		}
	}
	tmp := path + ".piccolo-tmp"
	if err := os.WriteFile(tmp, d.Marshal(), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
