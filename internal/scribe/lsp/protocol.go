package lsp

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
)

// Position is an LSP position: 0-based line, and character counted in
// UTF-16 code units.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is an LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// TextEdit replaces Range with NewText.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// WorkspaceEdit is the server's description of a multi-file change. Either
// Changes or DocumentChanges is used; DocumentChanges wins when both exist.
type WorkspaceEdit struct {
	Changes         map[string][]TextEdit `json:"changes,omitempty"`
	DocumentChanges []json.RawMessage     `json:"documentChanges,omitempty"`
}

// Diagnostic is one published diagnostic.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity,omitempty"`
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

// Severity names, by LSP severity number.
func severityName(s int) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	}
	return "error"
}

var symbolKinds = [...]string{
	"file", "module", "namespace", "package", "class", "method", "property",
	"field", "constructor", "enum", "interface", "function", "variable",
	"constant", "string", "number", "boolean", "array", "object", "key",
	"null", "enum-member", "struct", "event", "operator", "type-parameter",
}

func symbolKindName(k int) string {
	if k >= 1 && k <= len(symbolKinds) {
		return symbolKinds[k-1]
	}
	return "symbol"
}

// pathToURI converts an absolute path to a file:// URI.
func pathToURI(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// uriToPath converts a file:// URI to a path. Other schemes are returned
// unchanged and ok is false.
func uriToPath(uri string) (string, bool) {
	if !strings.HasPrefix(uri, "file://") {
		return uri, false
	}
	u, err := url.Parse(uri)
	if err != nil {
		return uri, false
	}
	p := u.Path
	if len(p) >= 3 && p[0] == '/' && p[2] == ':' {
		p = p[1:] // windows drive letter
	}
	return filepath.FromSlash(p), true
}
