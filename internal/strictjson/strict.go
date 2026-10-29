// Package strictjson rejects ambiguous object input before typed decoding.
package strictjson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// Decode requires valid UTF-8, one JSON value, unique object keys, and exact
// struct field names. Required fields must be present; omitempty marks optional
// fields. Null is allowed for pointer/RawMessage fields and types whose custom
// UnmarshalJSON explicitly accepts it. Callers still validate semantic ranges
// and impose a byte limit.
func Decode(data []byte, dst any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("invalid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := walk(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON: %v", err)
	}
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || reflect.ValueOf(dst).IsNil() {
		return fmt.Errorf("destination must be a pointer")
	}
	if err := shape(data, t.Elem()); err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func walk(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting exceeds limit")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key: %v", key)
			}
			seen[name] = true
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := walk(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter")
	}
	_, err = dec.Token()
	return err
}

func shape(data []byte, t reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		if t.Kind() == reflect.Pointer || t == reflect.TypeOf(json.RawMessage{}) || reflect.PointerTo(t).Implements(reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()) {
			return nil
		}
		return fmt.Errorf("null value")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if t.Kind() == reflect.Struct {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		allowed := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			allowed[name] = true
			raw, ok := object[name]
			if !ok {
				optional := false
				for _, option := range tag[1:] {
					optional = optional || option == "omitempty"
				}
				if optional {
					continue
				}
				return fmt.Errorf("missing field %s", name)
			}
			if err := shape(raw, field.Type); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
		for name := range object {
			if !allowed[name] {
				return fmt.Errorf("unknown field %s", name)
			}
		}
	} else if t.Kind() == reflect.Slice && t.Elem().Kind() != reflect.Uint8 {
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		for _, raw := range values {
			if err := shape(raw, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
