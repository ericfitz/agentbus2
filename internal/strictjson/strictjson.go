// Package strictjson refuses what encoding/json accepts silently when it
// decodes into a struct: a member name that matches a field only
// case-insensitively, and a repeated member or map key (the last one wins).
// Generalized from internal/credential/process.go checkMembers.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

var unmarshaler = reflect.TypeFor[json.Unmarshaler]()

// Check walks the single JSON value in b against Go type t (what the caller
// decodes b into). In every object that decodes into a struct, each member
// must be a field's exact json name and appear once; in every object that
// decodes into a map, each key must appear once. It recurses through struct
// fields, map values, slice and array elements and pointers. A type with its
// own UnmarshalJSON, an interface and json.RawMessage are opaque. Check
// judges nothing else (types, null, unknown names are the decoder's job),
// except that a value for a struct t must be an object. Errors name the path
// to the offending member, as `issuers[1].subjects: key "s" repeated`.
func Check(b []byte, t reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && !opaque(t) {
		if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
			return errors.New("not a JSON object")
		}
		return object(dec, t, "")
	}
	return value(dec, t, "")
}

func opaque(t reflect.Type) bool {
	return t.Kind() == reflect.Interface || t.Implements(unmarshaler) || reflect.PointerTo(t).Implements(unmarshaler)
}

// value checks the next value of dec against t.
func value(dec *json.Decoder, t reflect.Type, path string) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if opaque(t) {
		var raw json.RawMessage
		return dec.Decode(&raw)
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		switch t.Kind() {
		case reflect.Struct:
			return object(dec, t, path)
		case reflect.Map:
			return mapObject(dec, t.Elem(), path)
		}
		return skip(dec)
	case json.Delim('['):
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
			return skip(dec)
		}
		for i := 0; dec.More(); i++ {
			if err := value(dec, t.Elem(), fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		_, err := dec.Token() // ']'
		return err
	}
	return nil
}

// object checks the members of an object whose '{' was read, for struct t.
func object(dec *json.Decoder, t reflect.Type, path string) error {
	fields := map[string]reflect.Type{}
	collect(t, fields)
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := tok.(string)
		ft, ok := fields[k]
		if !ok {
			return fmt.Errorf("%sunknown member %q", prefix(path), k)
		}
		if seen[k] {
			return fmt.Errorf("%smember %q repeated", prefix(path), k)
		}
		seen[k] = true
		if err := value(dec, ft, join(path, k)); err != nil {
			return err
		}
	}
	_, err := dec.Token() // '}'
	return err
}

// mapObject checks the keys of an object whose '{' was read, for a map.
func mapObject(dec *json.Decoder, elem reflect.Type, path string) error {
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		k, _ := tok.(string)
		if seen[k] {
			return fmt.Errorf("%skey %q repeated", prefix(path), k)
		}
		seen[k] = true
		if err := value(dec, elem, fmt.Sprintf("%s[%q]", path, k)); err != nil {
			return err
		}
	}
	_, err := dec.Token() // '}'
	return err
}

// collect adds t's json member names, promoting untagged embedded structs as
// encoding/json does.
func collect(t reflect.Type, fields map[string]reflect.Type) {
	for i := range t.NumField() {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collect(ft, fields)
				continue
			}
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		fields[name] = f.Type
	}
}

// skip consumes the rest of a container whose opening delimiter was read.
func skip(dec *json.Decoder) error {
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			depth++
		case json.Delim('}'), json.Delim(']'):
			depth--
		}
	}
	return nil
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func prefix(path string) string {
	if path == "" {
		return ""
	}
	return path + ": "
}
