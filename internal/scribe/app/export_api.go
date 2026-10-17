package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

// apiExportMissing is the answer when a session has no request dump to
// export. It names both ways to turn the dump on, because the TUI toggle is
// the one most people will reach for and the environment switch is the one
// that works for a headless run.
const apiExportMissing = "this session has no API request dump yet. " +
	"Turn dumping on with /session export api on (or set SCRIBE_REQUEST_DUMP=1), " +
	"send a message, then export again"

// apiExportDoc is the file /session export api writes. Steps are the lines of
// the session's requests.jsonl, one per model request, in the order sent.
// SkippedLines counts lines that were not whole JSON, which happens when the
// app stopped partway through a write; they are dropped rather than failing
// the whole export.
type apiExportDoc struct {
	Generated    string            `json:"generated"`
	SessionID    string            `json:"session_id"`
	SkippedLines int               `json:"skipped_lines"`
	Steps        []json.RawMessage `json:"steps"`
}

// exportAPIDump writes the session's request dump as one JSON document under
// <workdir>/.scribe/api-export and returns its path. The dump itself is
// already redacted when each request is written (agent.DumpRequests); the
// whole document is redacted again here, so a line written by any other path
// cannot carry a secret into a file meant for sharing.
func (b *scribeBackend) exportAPIDump(a *App) (string, error) {
	raw, err := os.ReadFile(a.dumpPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New(apiExportMissing)
	}
	if err != nil {
		return "", err
	}
	doc := apiExportDoc{
		Generated: time.Now().UTC().Format(time.RFC3339),
		SessionID: a.Meta.ID,
	}
	doc.Steps, doc.SkippedLines = splitDumpLines(raw)
	if len(doc.Steps) == 0 {
		return "", errors.New(apiExportMissing)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	data := []byte(redact.Default().Redact(string(out)) + "\n")

	dir := filepath.Join(a.Workdir, ".scribe", "api-export")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, exportName(a)+"-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// splitDumpLines reads the JSON-lines dump into its steps. Blank lines are
// ignored. A line that is not valid JSON is counted and skipped.
func splitDumpLines(raw []byte) ([]json.RawMessage, int) {
	var steps []json.RawMessage
	skipped := 0
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			skipped++
			continue
		}
		steps = append(steps, json.RawMessage(line))
	}
	return steps, skipped
}
