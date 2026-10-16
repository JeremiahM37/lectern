// Package session persists Scribe's conversations, per-turn file checkpoints
// and prompt history.
//
// A project's sessions live under <root>/.scribe/sessions/<id>/:
//
//	meta.json       Meta (pretty JSON, 0600)
//	messages.jsonl  one llm.Message per line, append-only during a session
//	ui.jsonl        opaque UI records (json.RawMessage per line), so the
//	                transcript re-renders exactly on resume
//	plan.json       optional
//	checkpoints.json, blobs/<sha256>   written by Checkpoints (checkpoint.go)
//
// A global index at config.Home()/sessions.json maps session id to project
// root so a session can be resumed from any directory.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

// scrub masks secrets in the bytes of a session file. Transcripts, UI records
// and plans hold tool output and prompts verbatim, so each of their writes
// goes through here. Checkpoint blobs do not: undo must restore exact bytes.
func scrub(b []byte) []byte {
	s := string(b)
	if out := redact.Default().Redact(s); out != s {
		return []byte(out)
	}
	return b
}

// ErrNotFound means no session matches the id or prefix.
var ErrNotFound = errors.New("session not found")

// ErrAmbiguous means an id prefix matches more than one session.
var ErrAmbiguous = errors.New("session id prefix is ambiguous")

// Meta is the per-session header stored in meta.json.
type Meta struct {
	ID       string    `json:"id"`
	Title    string    `json:"title,omitempty"`
	Created  time.Time `json:"created"`
	Updated  time.Time `json:"updated"`
	Model    string    `json:"model,omitempty"`
	Mode     string    `json:"mode,omitempty"`
	Workdir  string    `json:"workdir"`
	Turns    int       `json:"turns"`
	Tokens   llm.Usage `json:"tokens"`
	CostUSD  float64   `json:"cost_usd"`
	Tags     []string  `json:"tags,omitempty"`
	ParentID string    `json:"parent_id,omitempty"`
	// LecternSessionID links the session to the Lectern session hosting it.
	LecternSessionID string `json:"lectern_session_id,omitempty"`

	// Messages and SizeBytes describe messages.jsonl. They are filled in by
	// Store.List for the session picker and are never written to meta.json.
	Messages  int   `json:"-"`
	SizeBytes int64 `json:"-"`
}

// Store reads and writes the sessions of one project.
type Store struct {
	Root string // absolute project root

	mu sync.Mutex
}

// NewStore returns the store for the project at root.
func NewStore(root string) *Store { return &Store{Root: root} }

func (s *Store) sessionsDir() string {
	return filepath.Join(config.ProjectDir(s.Root), "sessions")
}

// Dir returns the directory of session id (it need not exist).
func (s *Store) Dir(id string) string { return filepath.Join(s.sessionsDir(), id) }

// Create writes a new session. An empty ID gets a fresh UUID v4; zero
// timestamps and an empty Workdir are filled in. m is updated in place.
func (s *Store) Create(m *Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		m.ID = uuid.NewString()
	}
	if _, err := os.Stat(filepath.Join(s.Dir(m.ID), "meta.json")); err == nil {
		return fmt.Errorf("session %s already exists", m.ID)
	}
	now := time.Now().UTC()
	if m.Created.IsZero() {
		m.Created = now
	}
	if m.Updated.IsZero() {
		m.Updated = now
	}
	if m.Workdir == "" {
		m.Workdir = s.Root
	}
	if err := os.MkdirAll(s.Dir(m.ID), 0o700); err != nil {
		return err
	}
	if err := writeMeta(s.Dir(m.ID), m); err != nil {
		return err
	}
	return registerGlobal(m.ID, s.Root)
}

// Open resolves idPrefix (case-insensitive, unique prefix of the id, or an
// exact id) and returns its Meta.
func (s *Store) Open(idPrefix string) (Meta, error) {
	id, err := s.resolve(idPrefix)
	if err != nil {
		return Meta{}, err
	}
	return s.readMeta(id)
}

// ResolveID returns the full id for idPrefix.
func (s *Store) ResolveID(idPrefix string) (string, error) { return s.resolve(idPrefix) }

func (s *Store) resolve(prefix string) (string, error) {
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		return "", fmt.Errorf("%w: empty id", ErrNotFound)
	}
	ids, err := s.ids()
	if err != nil {
		return "", err
	}
	var hits []string
	for _, id := range ids {
		l := strings.ToLower(id)
		if l == prefix {
			return id, nil
		}
		if strings.HasPrefix(l, prefix) {
			hits = append(hits, id)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("%w: %s", ErrNotFound, prefix)
	case 1:
		return hits[0], nil
	default:
		sort.Strings(hits)
		return "", fmt.Errorf("%w: %q matches %d sessions (%s)", ErrAmbiguous, prefix, len(hits), strings.Join(hits, ", "))
	}
}

// ids lists directories that contain a readable meta.json.
func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.sessionsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(s.sessionsDir(), e.Name(), "meta.json")); err == nil {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// List returns this project's sessions, newest Updated first. Corrupt
// entries are skipped.
func (s *Store) List() []Meta {
	ids, _ := s.ids()
	out := make([]Meta, 0, len(ids))
	for _, id := range ids {
		m, err := s.readMeta(id)
		if err != nil {
			continue
		}
		m.Messages, m.SizeBytes = s.messageStats(id)
		out = append(out, m)
	}
	sortMeta(out)
	return out
}

// messageStats counts the messages in a session's messages.jsonl (one per
// line) and reports the file size. A missing file is an empty session.
func (s *Store) messageStats(id string) (int, int64) {
	f, err := os.Open(filepath.Join(s.Dir(id), "messages.jsonl"))
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, 0
	}
	n := 0
	buf := make([]byte, 64<<10)
	for {
		k, err := f.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if err != nil {
			break
		}
	}
	return n, st.Size()
}

func sortMeta(ms []Meta) {
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Updated.After(ms[j].Updated) })
}

// SaveMeta writes m as the session's meta. Updated is set to now.
func (s *Store) SaveMeta(m *Meta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m.ID == "" {
		return errors.New("SaveMeta: empty id")
	}
	m.Updated = time.Now().UTC()
	return writeMeta(s.Dir(m.ID), m)
}

func (s *Store) readMeta(id string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("session %s: corrupt meta.json: %w", id, err)
	}
	return m, nil
}

func writeMeta(dir string, m *Meta) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(dir, "meta.json"), scrub(append(b, '\n')), 0o600)
}

// touch rewrites meta.json with Updated=now and, when the session has no
// title yet, a title derived from the first user message in msgs. Caller
// holds s.mu.
func (s *Store) touch(id string, msgs []llm.Message) error {
	m, err := s.readMeta(id)
	if err != nil {
		return err
	}
	m.Updated = time.Now().UTC()
	if m.Title == "" {
		for _, msg := range msgs {
			if msg.Role == llm.RoleUser && strings.TrimSpace(msg.Text()) != "" {
				m.Title = DeriveTitle(msg.Text())
				break
			}
		}
	}
	return writeMeta(s.Dir(id), &m)
}

// AppendMessages appends msgs to the session's messages.jsonl.
func (s *Store) AppendMessages(id string, msgs []llm.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(id); err != nil {
		return err
	}
	path := filepath.Join(s.Dir(id), "messages.jsonl")
	if err := repairTail(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(scrub(buf.Bytes())); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return s.touch(id, msgs)
}

// repairTail makes sure a non-empty file ends in a newline, so an append
// after a crash-truncated last line starts on its own line.
func repairTail(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return err
	}
	last := make([]byte, 1)
	if _, err := f.ReadAt(last, st.Size()-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	_, err = f.WriteAt([]byte{'\n'}, st.Size())
	return err
}

// ReplaceMessages atomically rewrites messages.jsonl (used after compaction).
func (s *Store) ReplaceMessages(id string, msgs []llm.Message) error {
	var buf bytes.Buffer
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(id); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(s.Dir(id), "messages.jsonl"), scrub(buf.Bytes()), 0o600); err != nil {
		return err
	}
	return s.touch(id, msgs)
}

// LoadMessages returns the session's messages. Lines that do not parse (a
// write cut short by a crash) are skipped, so later messages still load.
func (s *Store) LoadMessages(id string) ([]llm.Message, error) {
	var out []llm.Message
	err := readLines(filepath.Join(s.Dir(id), "messages.jsonl"), func(line []byte) {
		var m llm.Message
		if json.Unmarshal(line, &m) == nil {
			out = append(out, m)
		}
	})
	return out, err
}

// AppendUI appends opaque UI records to ui.jsonl. Each record must be valid
// JSON; it is stored compacted on one line.
func (s *Store) AppendUI(id string, recs ...json.RawMessage) error {
	if len(recs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	for _, r := range recs {
		var c bytes.Buffer
		if err := json.Compact(&c, r); err != nil {
			return fmt.Errorf("ui record is not valid JSON: %w", err)
		}
		buf.Write(c.Bytes())
		buf.WriteByte('\n')
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(id); err != nil {
		return err
	}
	path := filepath.Join(s.Dir(id), "ui.jsonl")
	if err := repairTail(path); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(scrub(buf.Bytes())); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return s.touch(id, nil)
}

// LoadUI returns the UI records in order, skipping unparseable lines.
func (s *Store) LoadUI(id string) ([]json.RawMessage, error) {
	var out []json.RawMessage
	err := readLines(filepath.Join(s.Dir(id), "ui.jsonl"), func(line []byte) {
		if json.Valid(line) {
			out = append(out, append(json.RawMessage(nil), line...))
		}
	})
	return out, err
}

// SavePlan stores plan.json for the session.
func (s *Store) SavePlan(id string, plan json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !json.Valid(plan) {
		return errors.New("plan is not valid JSON")
	}
	return atomicWrite(filepath.Join(s.Dir(id), "plan.json"), scrub(append([]byte(plan), '\n')), 0o600)
}

// LoadPlan returns plan.json, or an error wrapping os.ErrNotExist.
func (s *Store) LoadPlan(id string) (json.RawMessage, error) {
	b, err := os.ReadFile(filepath.Join(s.Dir(id), "plan.json"))
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

// Delete removes the session directory (including its checkpoints and
// blobs) and its entry in the global index.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readMeta(id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.Dir(id)); err != nil {
		return err
	}
	return unregisterGlobal(id)
}

// Fork copies session id into a new session containing the first
// atMessage messages (0 to len). The fork records id as ParentID, starts
// with zero usage and carries no checkpoints, since their file snapshots
// belong to the original. UI records are copied only when the fork keeps
// every message, because UI records are not indexed by message.
func (s *Store) Fork(id string, atMessage int) (string, error) {
	src, err := s.readMeta(id)
	if err != nil {
		return "", err
	}
	msgs, err := s.LoadMessages(id)
	if err != nil {
		return "", err
	}
	if atMessage < 0 || atMessage > len(msgs) {
		return "", fmt.Errorf("fork point %d out of range (session has %d messages)", atMessage, len(msgs))
	}
	ui, err := s.LoadUI(id)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	child := Meta{
		ID:       uuid.NewString(),
		Title:    src.Title,
		Created:  now,
		Updated:  now,
		Model:    src.Model,
		Mode:     src.Mode,
		Workdir:  src.Workdir,
		Tags:     append([]string(nil), src.Tags...),
		ParentID: id,
	}
	if child.Title != "" {
		child.Title += " (fork)"
	}
	if err := s.Create(&child); err != nil {
		return "", err
	}
	if err := s.ReplaceMessages(child.ID, msgs[:atMessage]); err != nil {
		return "", err
	}
	if atMessage == len(msgs) && len(ui) > 0 {
		if err := s.AppendUI(child.ID, ui...); err != nil {
			return "", err
		}
	}
	if plan, err := s.LoadPlan(id); err == nil {
		if err := s.SavePlan(child.ID, plan); err != nil {
			return "", err
		}
	}
	return child.ID, nil
}

// Export renders session id as "markdown" or "json". The markdown transcript
// summarises tool calls and results in one line each; reasoning is kept in
// collapsed blocks.
func (s *Store) Export(id, format string) ([]byte, error) {
	m, err := s.readMeta(id)
	if err != nil {
		return nil, err
	}
	msgs, err := s.LoadMessages(id)
	if err != nil {
		return nil, err
	}
	switch format {
	case "json":
		return json.MarshalIndent(struct {
			Meta     Meta          `json:"meta"`
			Messages []llm.Message `json:"messages"`
		}{m, msgs}, "", "  ")
	case "markdown", "md":
		return []byte(renderMarkdown(m, msgs)), nil
	default:
		return nil, fmt.Errorf("unknown export format %q (want markdown or json)", format)
	}
}

// Search returns sessions whose title or first user message contains query
// (case-insensitive), newest first. An empty query matches everything.
func (s *Store) Search(query string) []Meta {
	q := strings.ToLower(strings.TrimSpace(query))
	var out []Meta
	for _, m := range s.List() {
		if q == "" || strings.Contains(strings.ToLower(m.Title), q) || strings.Contains(strings.ToLower(s.firstUserText(m.ID)), q) {
			out = append(out, m)
		}
	}
	return out
}

func (s *Store) firstUserText(id string) string {
	msgs, _ := s.LoadMessages(id)
	for _, m := range msgs {
		if m.Role == llm.RoleUser {
			return m.Text()
		}
	}
	return ""
}

// DeriveTitle makes a session title from a prompt: the trimmed first line,
// shortened to 60 characters, or "Empty session" when there is no text.
func DeriveTitle(text string) string {
	t := strings.TrimSpace(text)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	if t == "" {
		return "Empty session"
	}
	r := []rune(t)
	if len(r) > 60 {
		return string(r[:57]) + "..."
	}
	return t
}

// readLines calls fn for every non-empty line of path. A missing file is not
// an error.
func readLines(path string, fn func(line []byte)) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			fn(bytes.TrimRight(line, "\r\n"))
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// atomicWrite writes data to a temp file beside path, fsyncs it and renames
// it into place.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, perm); err != nil {
		return err
	}
	return os.Rename(name, path)
}
