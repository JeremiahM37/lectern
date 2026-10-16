package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

// exportKind maps the /session export argument to what is written. The
// spec's rule: "json" and "all"/"diagnostic" are special; anything else,
// including no argument, is Markdown.
func exportKind(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return "json"
	case "all", "diagnostic":
		return "diagnostic"
	default:
		return "markdown"
	}
}

// Export writes the current session to <workdir>/.scribe/exports and returns
// the path. format is "markdown" (or "md", or anything unrecognised), "json",
// or "all"/"diagnostic" for a diagnostic dump.
func (b *scribeBackend) Export(format string) (string, error) {
	a := b.current()
	if a.opts.Ephemeral {
		return "", errors.New("this session is not saved, so there is nothing to export")
	}
	dir := filepath.Join(a.Workdir, ".scribe", "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var (
		data []byte
		err  error
		path string
	)
	switch exportKind(format) {
	case "json":
		data, err = a.Store.Export(a.Meta.ID, "json")
		path = filepath.Join(dir, a.Meta.ID+".json")
	case "diagnostic":
		data, err = b.diagnosticExport(a)
		path = filepath.Join(dir, "diagnostic-"+time.Now().Format("20060102-150405")+".json")
	default:
		data, err = a.Store.Export(a.Meta.ID, "markdown")
		path = filepath.Join(dir, a.Meta.ID+".md")
	}
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// ExportMarkdown returns the current session as Markdown without writing a
// file, for copying to the clipboard.
func (b *scribeBackend) ExportMarkdown() (string, error) {
	a := b.current()
	if a.opts.Ephemeral {
		return "", errors.New("this session is not saved, so there is nothing to export")
	}
	data, err := a.Store.Export(a.Meta.ID, "markdown")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// diagnosticExport is the full dump for bug reports: the system prompt the
// model was given, the tools it could call, and the session itself. It is
// redacted with the same literal and pattern rules the agent applies to tool
// output, because it is meant to be shared.
func (b *scribeBackend) diagnosticExport(a *App) ([]byte, error) {
	session, err := a.Store.Export(a.Meta.ID, "json")
	if err != nil {
		return nil, err
	}
	var sess json.RawMessage = session
	if !json.Valid(session) {
		sess, _ = json.Marshal(string(session))
	}
	mode := a.Agent.Mode()
	doc := struct {
		Generated    string          `json:"generated"`
		SessionID    string          `json:"session_id"`
		Model        string          `json:"model"`
		Mode         string          `json:"mode"`
		SystemPrompt string          `json:"system_prompt"`
		Tools        []string        `json:"tools"`
		Session      json.RawMessage `json:"session"`
	}{
		Generated:    time.Now().UTC().Format(time.RFC3339),
		SessionID:    a.Meta.ID,
		Model:        a.Agent.CurrentModel().Ref,
		Mode:         mode,
		SystemPrompt: a.systemPrompt("")(mode),
		Tools:        a.Tools.SortedNames(),
		Session:      sess,
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("diagnostic export: %w", err)
	}
	return []byte(redact.Default().Redact(string(out)) + "\n"), nil
}
