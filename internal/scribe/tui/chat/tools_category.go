package chat

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// catInfo is the category tag shown before a tool's label: its name and the
// theme tone it is drawn in.
type catInfo struct{ name, tone string }

// toolCategories maps each tool to its category tag. Tools that are not listed
// get no tag, which is how unknown and MCP tools stay plain.
var toolCategories = map[string]catInfo{
	"read":             {"file", "info"},
	"read_file":        {"file", "info"},
	"edit_file":        {"file", "info"},
	"multi_edit":       {"file", "info"},
	"write_file":       {"file", "info"},
	"undo_edit":        {"file", "info"},
	"list_dir":         {"file", "info"},
	"grep":             {"file", "info"},
	"glob":             {"file", "info"},
	"shell":            {"shell", "error"},
	"shell_output":     {"shell", "error"},
	"shell_kill":       {"shell", "error"},
	"project":          {"shell", "error"},
	"git":              {"git", "success"},
	"web_search":       {"web", "info"},
	"fetch_page":       {"web", "info"},
	"memory":           {"memory", "amber"},
	"dispatch":         {"agent", "brand"},
	"skills":           {"agent", "brand"},
	"editor":           {"editor", "info"},
	"editor_panel":     {"editor", "info"},
	"navigate":         {"code", "warning"},
	"analyze":          {"code", "warning"},
	"symbol_refs":      {"code", "warning"},
	"rename_symbol":    {"code", "warning"},
	"move_symbol":      {"code", "warning"},
	"rename_file":      {"code", "warning"},
	"refactor":         {"code", "warning"},
	"discover_pattern": {"code", "warning"},
	"diagnostics":      {"code", "warning"},
	"format_file":      {"code", "warning"},
	"plan":             {"ui", "info"},
	"write_plan":       {"ui", "info"},
	"update_plan_step": {"ui", "info"},
	"ask_user":         {"ui", "info"},
	"task_list":        {"ui", "info"},
	"code_execution":   {"smithy", "amber"},
	"find_symbol":      {"soul-map", "success"},
	"code_map":         {"soul-map", "success"},
	"file_outline":     {"soul-map", "success"},
	"code_impact":      {"soul-map", "success"},
	"ast_edit":         {"morph", "brandAlt"},
}

// categoryOf returns the category tag for a tool, and false when it has none.
func categoryOf(tool string) (catInfo, bool) {
	c, ok := toolCategories[tool]
	return c, ok
}

// backendTone colours a backend tag by the provider that answered.
func backendTone(name string) string {
	switch name {
	case "brave", "ddg":
		return "brandSecondary"
	case "jina":
		return "warning"
	case "fetch":
		return "info"
	}
	return "textDim"
}

// backendOf reads the backend a result reports in Meta["backend"], if any.
func backendOf(r *tools.Result) string {
	if r == nil || r.Meta == nil {
		return ""
	}
	s, _ := r.Meta["backend"].(string)
	return s
}

// pathArgs lists the path arguments a call names, for the location badge.
func pathArgs(m map[string]any) []string {
	var out []string
	if s := argStr(m, "path", "from", "to"); s != "" {
		out = append(out, s)
	}
	if arr, ok := m["paths"].([]any); ok {
		for _, p := range arr {
			if s, ok := p.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// locationBadge names where a call's path lies when that is outside the
// working directory: [config] under the user config dir, [tmp] under the
// temp dir, [outside] anywhere else. Relative paths are inside the project.
// It returns "" when every path is inside.
func locationBadge(workdir string, paths ...string) string {
	if workdir == "" {
		return ""
	}
	for _, p := range paths {
		if p == "" || !filepath.IsAbs(p) {
			continue
		}
		if rel, err := filepath.Rel(workdir, p); err == nil && !strings.HasPrefix(rel, "..") {
			continue
		}
		// The config dir is checked first: it is the more specific place,
		// and a home under the temp dir (sandboxes, CI) holds both.
		if cfg, err := os.UserConfigDir(); err == nil && under(p, cfg) {
			return "config"
		}
		if under(p, os.TempDir()) {
			return "tmp"
		}
		return "outside"
	}
	return ""
}

// under reports whether p is dir or inside it.
func under(p, dir string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, p)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// outsideTreePath shortens a path that lies outside the workdir to its last
// two segments, so a multi-file read stays readable. Inside paths are
// returned relative to the workdir as usual.
func (v *View) outsideTreePath(p string) string {
	rel := v.relPath(p)
	if !filepath.IsAbs(rel) {
		return rel
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	n := len(parts)
	if n <= 2 {
		return rel
	}
	return strings.Join(parts[n-2:], "/")
}

// categoryTag renders "[file]" in the category tone while running, and in
// textDim once the call has finished.
func (v *View) categoryTag(tool string, running bool) string {
	c, ok := categoryOf(tool)
	if !ok {
		return ""
	}
	col := v.th.TextDim
	if running {
		col = v.tone(c.tone)
	}
	return fg(col, "["+c.name+"]")
}

// badgeTag renders a location badge. Outside the workdir reads as a warning;
// config and tmp paths are routine, so they stay dim.
func (v *View) badgeTag(badge string) string {
	if badge == "" {
		return ""
	}
	col := v.th.TextDim
	if badge == "outside" {
		col = v.th.Warning
	}
	return fg(col, "["+badge+"]")
}

// backendTag renders "[brave]" for a result that reports its backend.
func (v *View) backendTag(r *tools.Result) string {
	b := backendOf(r)
	if b == "" {
		return ""
	}
	return fg(v.tone(backendTone(b)), "["+b+"]")
}
