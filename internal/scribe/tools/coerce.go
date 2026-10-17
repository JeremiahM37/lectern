package tools

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
)

// Models send near-miss argument shapes: "tags": "a" where a list is
// asked for, "limit": "10" for a number, "pinned": "true" for a boolean, or a
// number where a string is asked for. Decode accepts those top-level shapes
// when the strict decode fails, so a call is not lost over its shape. Anything
// that cannot be read still fails, with the usual message.

// coerceArgs rewrites the top-level fields of args whose JSON shape is a near
// miss for the Go type of the matching field in v. It reports whether it
// changed anything. v must be a pointer to a struct.
func coerceArgs(args json.RawMessage, v any) (json.RawMessage, bool) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(args, &obj); err != nil || obj == nil {
		return nil, false
	}
	changed := false
	st := rv.Elem().Type()
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == "-" {
			continue
		} else if tag != "" {
			name = tag
		}
		key, ok := matchKey(obj, name)
		if !ok {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		if nv, ok := coerceValue(obj[key], ft); ok {
			obj[key] = nv
			changed = true
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}

// matchKey finds name in obj, exactly or ignoring case, as encoding/json does.
func matchKey(obj map[string]json.RawMessage, name string) (string, bool) {
	if _, ok := obj[name]; ok {
		return name, true
	}
	for k := range obj {
		if strings.EqualFold(k, name) {
			return k, true
		}
	}
	return "", false
}

// coerceValue returns the JSON for raw read as type t, when raw is a scalar
// of a different kind that can be read as t. It reports false when raw is
// already right, or cannot be read.
func coerceValue(raw json.RawMessage, t reflect.Type) (json.RawMessage, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '[' || raw[0] == '{' || string(raw) == "null" {
		return nil, false
	}
	isStr := raw[0] == '"'
	var text string
	if isStr {
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, false
		}
	} else {
		text = string(raw) // a number or a boolean
	}
	text = strings.TrimSpace(text)

	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() != reflect.String {
			return nil, false
		}
		var items []string
		if isStr {
			// Split on newlines only: a comma is often part of one item
			// ("Yes, keep it", a command, a path), and splitting it would
			// change what the call does.
			for _, part := range strings.Split(text, "\n") {
				if p := strings.TrimSpace(part); p != "" {
					items = append(items, p)
				}
			}
		} else {
			items = []string{text}
		}
		out, err := json.Marshal(items)
		return out, err == nil
	case reflect.String:
		if isStr {
			return nil, false
		}
		out, err := json.Marshal(text)
		return out, err == nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if !isStr {
			return nil, false
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, false
		}
		return []byte(strconv.FormatInt(n, 10)), true
	case reflect.Float32, reflect.Float64:
		if !isStr {
			return nil, false
		}
		f, err := strconv.ParseFloat(text, 64)
		if err != nil {
			return nil, false
		}
		out, err := json.Marshal(f)
		return out, err == nil
	case reflect.Bool:
		if !isStr {
			return nil, false
		}
		switch strings.ToLower(text) {
		case "true", "yes", "y", "1":
			return []byte("true"), true
		case "false", "no", "n", "0":
			return []byte("false"), true
		}
	}
	return nil, false
}

// pathAliases are the names models use for a "path" argument. Live qwen sent
// edits with the file under another key; the call then failed with "path is
// required" although the file was named.
var pathAliases = []string{"file_path", "filePath", "filepath", "filename", "file_name", "file"}

// fillPathAlias sets an empty top-level string field tagged "path" from the
// first alias key in args that holds a string. It changes nothing when the
// struct has no such field, the field is set, or no alias is present.
func fillPathAlias(args json.RawMessage, v any) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Elem().Kind() != reflect.Struct {
		return
	}
	sv := rv.Elem()
	st := sv.Type()
	field := -1
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == "path" && f.Type.Kind() == reflect.String && f.IsExported() {
			field = i
			break
		}
	}
	if field < 0 || sv.Field(field).String() != "" {
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(args, &obj) != nil {
		return
	}
	if _, ok := obj["path"]; ok {
		return // the model sent path itself, even if empty
	}
	for _, k := range pathAliases {
		var s string
		if raw, ok := obj[k]; ok && json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			sv.Field(field).SetString(strings.TrimSpace(s))
			return
		}
	}
}
