package theme

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultBase is the palette a user theme extends when it sets no _extends.
const DefaultBase = ScribeID

// maxExtendDepth bounds _extends chains so a cycle cannot loop.
const maxExtendDepth = 4

var hexColor = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// LoadUserThemes reads Scribe's user themes from dir (normally
// config.Home()/themes). It replaces any previously loaded user themes. A
// missing directory is not an error. Files that fail to parse are skipped and
// reported in the returned error, which still lists every theme that did load.
func LoadUserThemes(dir string) error {
	return LoadUserThemesFrom(dir, "")
}

// LoadUserThemesFrom is LoadUserThemes plus the legacy combined file
// (config.Home()/themes.json, a JSON object of id to theme). An id present in
// both is merged, and the per-id file wins key by key. legacy may be "".
func LoadUserThemesFrom(dir, legacy string) error {
	raw := map[string]map[string]any{}
	var problems []string
	if legacy != "" {
		data, err := os.ReadFile(legacy)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(legacy), err))
		default:
			var all map[string]any
			if err := json.Unmarshal(data, &all); err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(legacy), err))
			}
			for id, v := range all {
				if m, ok := v.(map[string]any); ok {
					raw[id] = m
				} else {
					problems = append(problems, fmt.Sprintf("%s: %s is not an object", filepath.Base(legacy), id))
				}
			}
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if old, ok := raw[id]; ok {
			for k, v := range m {
				old[k] = v
			}
			continue
		}
		raw[id] = m
	}
	loaded := map[string]palette{}
	for id, m := range raw {
		if isBuiltin(id) {
			continue // built-ins cannot be shadowed
		}
		p, err := buildUserPalette(id, m, raw, 0)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s.json: %v", id, err))
			continue
		}
		loaded[id] = p
	}
	setCustoms(loaded)
	if len(problems) > 0 {
		return errors.New("theme files skipped: " + strings.Join(problems, "; "))
	}
	return nil
}

// buildUserPalette merges a user theme over its base. Token keys are kebab-case
// (bg-app); unknown keys and non-string values are ignored, as are colour values
// that are neither #rgb, #rrggbb nor an ANSI index.
func buildUserPalette(id string, m map[string]any, raw map[string]map[string]any, depth int) (palette, error) {
	if depth > maxExtendDepth {
		return palette{}, errors.New("_extends chain too deep")
	}
	baseID, _ := m["_extends"].(string)
	if baseID == "" {
		baseID = DefaultBase
	}
	var base palette
	if bm, ok := raw[baseID]; ok && baseID != id && !isBuiltin(baseID) {
		p, err := buildUserPalette(baseID, bm, raw, depth+1)
		if err != nil {
			return palette{}, err
		}
		base = p
	} else if p, ok := paletteFor(baseID); ok {
		base = p
	} else {
		return palette{}, fmt.Errorf("unknown _extends %q", baseID)
	}
	out := palette{Label: base.Label, Variant: base.Variant, Tokens: map[string]string{}}
	for k, v := range base.Tokens {
		out.Tokens[k] = v
	}
	if s, ok := m["_label"].(string); ok && s != "" {
		out.Label = s
	}
	out.Description = ""
	if s, ok := m["_description"].(string); ok {
		out.Description = s
	}
	if s, ok := m["_variant"].(string); ok && (s == "dark" || s == "light") {
		out.Variant = s
	}
	known := map[string]bool{}
	for _, k := range append(append([]string{}, UITokens...), SyntaxTokens...) {
		known[k] = true
	}
	for key, val := range m {
		if strings.HasPrefix(key, "_") {
			continue
		}
		s, ok := val.(string)
		if !ok {
			continue
		}
		camel := kebabToCamel(key)
		if !known[camel] {
			continue
		}
		if s != "" && !hexColor.MatchString(s) && !isANSIIndex(s) {
			continue
		}
		out.Tokens[camel] = s
	}
	return out, nil
}

// isBuiltin reports whether id is a palette the binary ships (or a reserved id).
func isBuiltin(id string) bool {
	if id == "auto" {
		return true
	}
	if _, ok := scribePalettes[id]; ok {
		return true
	}
	_, ok := builtinPalettes[id]
	return ok
}

func isANSIIndex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) <= 3
}

// kebabToCamel converts "bg-primary" to "bgPrimary".
func kebabToCamel(s string) string {
	var b strings.Builder
	upper := false
	for _, r := range s {
		if r == '-' {
			upper = true
			continue
		}
		if upper && r >= 'a' && r <= 'z' {
			r -= 'a' - 'A'
		}
		upper = false
		b.WriteRune(r)
	}
	return b.String()
}

func setCustoms(m map[string]palette) {
	mu.Lock()
	defer mu.Unlock()
	customs = m
}
