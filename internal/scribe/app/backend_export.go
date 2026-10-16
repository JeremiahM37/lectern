package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
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
	name := exportName(a)
	switch exportKind(format) {
	case "json":
		data, err = a.Store.Export(a.Meta.ID, "json")
		path = filepath.Join(dir, name+".json")
	case "diagnostic":
		data, err = b.diagnosticExport(a)
		path = filepath.Join(dir, "diagnostic-"+time.Now().Format("20060102-150405")+".json")
	default:
		data, err = a.Store.Export(a.Meta.ID, "markdown")
		path = filepath.Join(dir, name+".md")
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

// ExportMemories implements tui.MemoryTransfer. It writes the records of
// scope ("project", "global" or "all") to a timestamped JSON file under the
// exports folder and returns its path and how many records it holds.
func (b *scribeBackend) ExportMemories(scope string) (string, int, error) {
	a := b.current()
	if a.Memory == nil {
		return "", 0, errors.New("memory is off for this session")
	}
	scope = normScope(scope)
	var recs []memory.Record
	for _, db := range a.Memory.DBs() {
		if scope != "all" && db.Scope() != scope {
			continue
		}
		var buf bytes.Buffer
		if err := db.Export(&buf); err != nil {
			return "", 0, err
		}
		var part []memory.Record
		if err := json.Unmarshal(buf.Bytes(), &part); err != nil {
			return "", 0, err
		}
		recs = append(recs, part...)
	}
	if len(recs) == 0 {
		return "", 0, fmt.Errorf("there are no %s memories to export", scope)
	}
	dir := filepath.Join(a.Workdir, ".scribe", "exports")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(dir, "memories-"+scope+"-"+time.Now().Format("20060102-150405")+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return "", 0, err
	}
	return path, len(recs), nil
}

// ImportMemories implements tui.MemoryTransfer. It reads the newest export of
// scope back in. Each record goes to the store its scope names (a project
// record with no project store falls to global) and merges through the
// normal dedupe. It returns the file read and the counts.
func (b *scribeBackend) ImportMemories(scope string) (string, int, int, error) {
	a := b.current()
	if a.Memory == nil {
		return "", 0, 0, errors.New("memory is off for this session")
	}
	scope = normScope(scope)
	dir := filepath.Join(a.Workdir, ".scribe", "exports")
	entries, _ := os.ReadDir(dir)
	var newest string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "memories-"+scope+"-") && strings.HasSuffix(n, ".json") && n > newest {
			newest = n
		}
	}
	if newest == "" {
		return "", 0, 0, fmt.Errorf("no %s export found in %s. Export first with e", scope, dir)
	}
	path := filepath.Join(dir, newest)
	raw, err := os.ReadFile(path)
	if err != nil {
		return path, 0, 0, err
	}
	var recs []memory.Record
	if err := json.Unmarshal(raw, &recs); err != nil {
		return path, 0, 0, fmt.Errorf("%s is not a memory export: %w", newest, err)
	}
	// Group by target store, then let each store import its own group.
	groups := map[*memory.DB][]memory.Record{}
	for _, r := range recs {
		if scope != "all" && r.Scope != "" && r.Scope != scope {
			continue
		}
		db, err := a.Memory.DB(targetScope(a, r.Scope))
		if err != nil {
			return path, 0, 0, err
		}
		groups[db] = append(groups[db], r)
	}
	var added, merged int
	for db, group := range groups {
		buf, err := json.Marshal(group)
		if err != nil {
			return path, added, merged, err
		}
		ad, me, err := db.Import(bytes.NewReader(buf))
		added, merged = added+ad, merged+me
		if err != nil {
			return path, added, merged, err
		}
	}
	return path, added, merged, nil
}

// EmergencySave implements tui.EmergencySaver. It writes the in-memory
// history straight to the session store, without aborting the turn or
// waiting for it, for use after a crash. It is best effort.
func (b *scribeBackend) EmergencySave() {
	a := b.current()
	if a == nil || a.opts.Ephemeral || a.Store == nil {
		return
	}
	_ = a.Store.ReplaceMessages(a.Meta.ID, a.Agent.Messages())
}

// MemoryCleanupDone implements tui.MemoryCleanupDoner: a completed cleanup
// action restarts the counter behind the context-bar hint.
func (b *scribeBackend) MemoryCleanupDone() { _ = memory.NoteCleanupDone() }

// normScope maps an argument to project, global or all. Anything unknown
// means project, the default store.
func normScope(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "global":
		return "global"
	case "all":
		return "all"
	default:
		return "project"
	}
}

// targetScope is the store an imported record goes to. A project record with
// no project store open falls to the global store, so nothing is dropped.
func targetScope(a *App, scope string) string {
	if scope == "global" || a.Memory.Project == nil {
		return "global"
	}
	return "project"
}

// exportName is the file stem for a session export: its title slugified
// (spec 05 §2.4), with the first eight characters of the session id appended
// so two sessions with the same title do not overwrite each other. An empty
// title leaves just the id.
func exportName(a *App) string {
	a.mu.Lock()
	title, id := a.Meta.Title, a.Meta.ID
	a.mu.Unlock()
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	slug := slugify(title)
	if slug == "" {
		return id
	}
	return slug + "-" + short
}

// slugify turns a title into a lowercase, hyphen-separated filename stem of
// at most 60 characters. Runs of other characters become one hyphen.
func slugify(s string) string {
	var b strings.Builder
	dash := true // suppresses a leading hyphen
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 60 {
		out = strings.TrimRight(out[:60], "-")
	}
	return out
}

// coChangeFrom adapts the repo map's cached co-change graph to the memory
// store. The map mines git history once, at build; each lookup is a read.
func coChangeFrom(m *repomap.Map) func(string) []string {
	return func(file string) []string {
		var out []string
		for _, p := range m.CoChange(file) {
			out = append(out, p.File)
		}
		return out
	}
}
