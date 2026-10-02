package console

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// readable is the presentation layer for interactive terminal views. The API
// and `lectern api` keep their machine-readable JSON contract unchanged.
// Unknown fields are retained, and maps are sorted so redraws stay stable.
func readable(v any) string {
	var b strings.Builder
	writeReadable(&b, normalizeReadable(reflect.ValueOf(v)), 0, "")
	return strings.TrimRight(b.String(), "\n")
}

func normalizeReadable(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	for v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Sprint(v.Interface())
		}
		out := make(map[string]any, v.Len())
		for _, key := range v.MapKeys() {
			out[key.String()] = normalizeReadable(v.MapIndex(key))
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = normalizeReadable(v.Index(i))
		}
		return out
	default:
		return v.Interface()
	}
}

func writeReadable(b *strings.Builder, v any, depth int, key string) {
	indent := strings.Repeat("  ", depth)
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			if key == "" {
				writeScalar(b, indent, "", "(none)")
			} else {
				writeScalar(b, indent, humanLabel(key)+": ", "(none)")
			}
			return
		}
		if key != "" {
			b.WriteString(indent + humanLabel(key) + "\n")
			depth++
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			value, omit, displayKey := readableJSONField(x[k], k)
			if omit {
				continue
			}
			writeReadable(b, value, depth, displayKey)
		}
	case []any:
		if len(x) == 0 {
			prefix := ""
			if key != "" {
				prefix = humanLabel(key) + ": "
			}
			writeScalar(b, indent, prefix, "(none)")
			return
		}
		if key != "" {
			b.WriteString(indent + humanLabel(key) + "\n")
		}
		for _, item := range x {
			itemIndent := strings.Repeat("  ", depth+1)
			if _, nestedMap := item.(map[string]any); nestedMap {
				b.WriteString(itemIndent + "•\n")
				writeReadable(b, item, depth+2, "")
				continue
			}
			if _, nestedList := item.([]any); nestedList {
				b.WriteString(itemIndent + "•\n")
				writeReadable(b, item, depth+2, "")
				continue
			}
			writeScalar(b, itemIndent, "• ", item)
		}
	default:
		prefix := ""
		if key != "" {
			prefix = humanLabel(key) + ": "
		}
		writeScalar(b, indent, prefix, x)
	}
}

// readableJSONField expands JSON stored in *_json fields for interactive
// views. These fields are persisted as strings in API rows, but showing the
// storage encoding (for example, `env_json: {}`) makes the dashboard harder
// to scan. Empty JSON carries no information in a detail view, so omit it.
// Other string fields remain untouched, including explicit API output.
func readableJSONField(v any, key string) (any, bool, string) {
	if !strings.HasSuffix(strings.ToLower(key), "_json") {
		return v, false, key
	}
	s, ok := v.(string)
	if !ok {
		return v, false, key
	}
	var decoded any
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		return v, false, key
	}
	switch x := decoded.(type) {
	case nil:
		return nil, true, key
	case map[string]any:
		if len(x) == 0 {
			return nil, true, key
		}
	case []any:
		if len(x) == 0 {
			return nil, true, key
		}
	}
	return decoded, false, key[:len(key)-len("_json")]
}

func writeScalar(b *strings.Builder, indent, prefix string, v any) {
	value := "(none)"
	if v != nil {
		value = clean(fmt.Sprint(v))
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if i == 0 {
			b.WriteString(indent + prefix + line)
		} else {
			b.WriteString("\n" + indent + strings.Repeat("  ", 1) + line)
		}
	}
	b.WriteByte('\n')
}

func humanLabel(s string) string {
	parts := strings.FieldsFunc(oneLine(clean(s)), func(r rune) bool { return r == '_' || r == '-' })
	for i := range parts {
		runes := []rune(parts[i])
		if len(runes) > 0 {
			runes[0] = unicode.ToUpper(runes[0])
			parts[i] = string(runes)
		}
	}
	return strings.Join(parts, " ")
}

// itemMeta is the second line of a project, machine or routine row: what
// tells two of them apart, in place of the session row's group and status.
func itemMeta(section string, r row) string {
	switch section {
	case "projects":
		return oneLine(joinNonEmpty(" · ", str(r["repo_path"]), str(r["target_name"])))
	case "targets":
		kind := str(r["kind"])
		if kind == "local" {
			kind = "this machine"
		}
		return oneLine(joinNonEmpty(" · ", kind, str(r["host"])))
	}
	return ""
}

// itemPreview describes a project, machine or routine in plain words, with
// what can be done with it, instead of its database record.
func itemPreview(section string, r row) string {
	var lines []string
	add := func(label, value string) {
		if strings.TrimSpace(value) != "" {
			lines = append(lines, label+": "+value)
		}
	}
	switch section {
	case "projects":
		lines = append(lines, name(r), "")
		add("Folder", str(r["repo_path"]))
		add("Machine", str(r["target_name"]))
		add("Default agent", str(r["default_agent"]))
		add("Base branch", str(r["default_base_branch"]))
		add("Setup command for new worktrees", str(r["setup_cmd"]))
		add("Check command", str(r["verify_cmd"]))
		lines = append(lines, "", "Enter opens a shell in this folder.", "n on Sessions starts an agent here; v reviews its changes.", "m has the brief, notes, skills, MCP and editing.")
	case "targets":
		lines = append(lines, name(r), "")
		kind := map[string]string{"local": "This machine", "ssh": "SSH", "pct": "Proxmox container"}[str(r["kind"])]
		if kind == "" {
			kind = str(r["kind"])
		}
		add("Connection", kind)
		add("Host", str(r["host"]))
		add("User", str(r["user"]))
		if v, ok := r["max_concurrent"].(float64); ok && v > 0 {
			add("Agents at once", strconv.FormatInt(int64(v), 10))
		}
		lines = append(lines, "", "m checks the connection and which agents are installed.")
	case "routines":
		lines = append(lines, name(r), "")
		schedule := str(r["schedule"])
		if schedule == "" {
			schedule = "run by hand"
		}
		add("Schedule", schedule)
		if r["enabled"] == false {
			add("State", "paused")
		}
		add("Prompt", str(r["prompt"]))
		lines = append(lines, "", "m runs it now, pauses or edits it.")
	}
	return strings.Join(lines, "\n")
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
