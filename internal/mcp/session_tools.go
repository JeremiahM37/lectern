package mcp

// Helpers for the three tools that turn a brainstorm into a running,
// context-loaded interactive session: list_sessions, start_session and
// send_to_session (defined in tools.go, alongside the rest of the surface).
//
// The interesting sequencing lives here rather than inline in the tool
// bodies: a session that will receive files/notes/context cannot be told
// about them until it exists AND has finished setting up (worktree creation,
// the agent actually starting), so start_session creates it primeless, polls
// GET /sessions/{id} until it is at a prompt, THEN uploads and sends one
// message — see attachContext/pollSessionReady below.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxLocalAttachment mirrors internal/api/attachments.go's maxAttachmentSize:
// there is no point staging a read past what the server will accept.
const maxLocalAttachment = 25 << 20

// maxInlineFileBytes is the decoded-size ceiling for one inline_files entry.
// Kept well under maxLocalAttachment and the HTTP transport's own
// MaxRequestBytes (16 MiB, see http.go): an inline file travels inside the
// tool call's own JSON-RPC request body — a chat model pasting or attaching
// a document, not streaming one off disk — so a much tighter per-file cap
// keeps one call from consuming the whole request budget.
const maxInlineFileBytes = 5 << 20

// maxInlineFiles bounds how many inline_files entries one call may carry.
const maxInlineFiles = 10

// inlineFileArg is one element of the inline_files argument to
// start_session/send_to_session: a chat-uploaded or pasted document, given
// as {name, content, encoding}. This is the chat-friendly counterpart to
// `files` (a path on the machine this MCP process runs on, refused for a
// remote caller — see Server.Remote): claude.ai has no filesystem of
// Lectern's to point at, so it hands over the bytes themselves.
type inlineFileArg struct {
	Name     string
	Content  string
	Encoding string // "text" (default) or "base64"
}

// parseInlineFiles decodes the inline_files JSON argument's shape and
// validates the count; each entry's content is decoded lazily by
// decodeInline, at attach time, so a bad entry is reported against the file
// it belongs to rather than failing the whole batch up front for a size
// nobody has computed yet.
func parseInlineFiles(v any) ([]inlineFileArg, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	if len(arr) > maxInlineFiles {
		return nil, fmt.Errorf("inline_files has %d entries, over the limit of %d", len(arr), maxInlineFiles)
	}
	out := make([]inlineFileArg, 0, len(arr))
	for i, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("inline_files[%d] must be an object with name/content/encoding", i)
		}
		f := inlineFileArg{
			Name:     strings.TrimSpace(argStr(m, "name")),
			Content:  argStr(m, "content"),
			Encoding: strings.TrimSpace(argStr(m, "encoding")),
		}
		if f.Name == "" {
			return nil, fmt.Errorf("inline_files[%d] is missing name", i)
		}
		if f.Encoding == "" {
			f.Encoding = "text"
		}
		if f.Encoding != "text" && f.Encoding != "base64" {
			return nil, fmt.Errorf("inline_files[%d] (%s): encoding must be \"text\" or \"base64\", got %q", i, f.Name, f.Encoding)
		}
		out = append(out, f)
	}
	return out, nil
}

// decodeInline turns one inline_files entry into bytes, enforcing the
// decoded-size limit after decoding — base64 hides the real size until then.
func decodeInline(f inlineFileArg) ([]byte, error) {
	if f.Encoding == "base64" {
		data, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid base64: %w", f.Name, err)
		}
		if len(data) > maxInlineFileBytes {
			return nil, fmt.Errorf("%s is %d bytes decoded, over the %d MiB inline_files limit", f.Name, len(data), maxInlineFileBytes>>20)
		}
		return data, nil
	}
	data := []byte(f.Content)
	if len(data) > maxInlineFileBytes {
		return nil, fmt.Errorf("%s is %d bytes, over the %d MiB inline_files limit", f.Name, len(data), maxInlineFileBytes>>20)
	}
	return data, nil
}

// resolveAttachArgs reads the files/inline_files arguments common to
// start_session and send_to_session, refusing the local `files` parameter
// for a remote (web-connector) caller: a Server built for the HTTP
// transport has Remote set, and "local" for that process means the machine
// Lectern's MCP server itself runs on, which a remote caller has no access
// to and must not be able to make this process read from.
func (s *Server) resolveAttachArgs(args map[string]any) (files []string, inline []inlineFileArg, err error) {
	files = stringSlice(args["files"])
	if s.Remote && len(files) > 0 {
		return nil, nil, fmt.Errorf("files is a list of paths on the machine Lectern's MCP server runs on; " +
			"the web connector has no access to that filesystem and cannot read them on your behalf — use " +
			"inline_files instead (name, content, and encoding \"text\" or \"base64\")")
	}
	inline, err = parseInlineFiles(args["inline_files"])
	if err != nil {
		return nil, nil, err
	}
	return files, inline, nil
}

// pollInterval/sessionReadyTimeout are vars, not consts, purely so tests can
// shrink them — production always sees 2s/180s.
var (
	pollInterval        = 2 * time.Second
	sessionReadyTimeout = 180 * time.Second
)

// resolveProjectByName finds a project by its exact name (see list_projects),
// the same lookup create_task and delegate_build each do inline.
func (s *Server) resolveProjectByName(name string) (map[string]any, error) {
	projects, err := s.list("/projects")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range projects {
		pn, _ := p["name"].(string)
		names = append(names, pn)
		if pn == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no project named %q — have: %s", name, strings.Join(names, ", "))
}

// pollSessionReady waits for a freshly created session to be somewhere safe
// to talk to: not still starting, and not mid-worktree-setup. It fails fast
// and clearly on the two ways a session gives up before that (setup_state
// failed, or the tmux process already gone), rather than spinning to the
// timeout on a session that is never coming back.
func (s *Server) pollSessionReady(id int64) (map[string]any, error) {
	deadline := time.Now().Add(sessionReadyTimeout)
	for {
		sess, err := s.object(fmt.Sprintf("/sessions/%d", id))
		if err != nil {
			return nil, err
		}
		status, _ := sess["status"].(string)
		setupState, _ := sess["setup_state"].(string)
		if setupState == "failed" {
			return nil, fmt.Errorf("session #%d's workspace setup failed: %v", id, sess["setup_error"])
		}
		if status == "dead" {
			return nil, fmt.Errorf("session #%d ended before it was ready to receive context", id)
		}
		if setupState != "creating" && (status == "waiting" || status == "idle") {
			return sess, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("session #%d did not settle within %s (status=%q, setup_state=%q) — "+
				"check it in Lectern before retrying", id, sessionReadyTimeout, status, setupState)
		}
		time.Sleep(pollInterval)
	}
}

// attached is one thing staged onto a session's workdir and about to be
// listed in the message that tells the agent where to find it.
type attached struct {
	Path     string // absolute path on the target, as returned by the attachments endpoint
	NotePath string // set only for kind "note": the Grimoire vault path it came from
}

// attachContext uploads local files, inline (chat-provided) files, an inline
// context blob, and Grimoire notes to a session, in that order, stopping at
// the first failure — a partial attach is reported precisely (which ones
// landed) rather than silently retried or hidden.
func (s *Server) attachContext(sessionID int64, context string, files, notes []string, inline []inlineFileArg) ([]attached, error) {
	dest := fmt.Sprintf("/sessions/%d/attachments", sessionID)
	var out []attached
	for _, f := range files {
		if err := validateLocalFile(f); err != nil {
			return out, err
		}
		fh, err := os.Open(f)
		if err != nil {
			return out, fmt.Errorf("could not open %s: %w", f, err)
		}
		resp, err := s.upload(dest, filepath.Base(f), fh)
		fh.Close()
		if err != nil {
			return out, fmt.Errorf("attaching %s: %w", f, err)
		}
		p, _ := resp["path"].(string)
		out = append(out, attached{Path: p})
	}
	for _, f := range inline {
		data, err := decodeInline(f)
		if err != nil {
			return out, err
		}
		name := path.Base(f.Name)
		if name == "" || name == "." || name == "/" {
			name = "attachment"
		}
		resp, err := s.upload(dest, name, bytes.NewReader(data))
		if err != nil {
			return out, fmt.Errorf("attaching %s: %w", f.Name, err)
		}
		p, _ := resp["path"].(string)
		out = append(out, attached{Path: p})
	}
	if strings.TrimSpace(context) != "" {
		resp, err := s.upload(dest, "lectern-context.md", strings.NewReader(context))
		if err != nil {
			return out, fmt.Errorf("attaching context: %w", err)
		}
		p, _ := resp["path"].(string)
		out = append(out, attached{Path: p})
	}
	for _, n := range notes {
		note, err := fetchGrimoireNote(n)
		if err != nil {
			return out, err
		}
		name := path.Base(note.Path)
		if name == "" || name == "." || name == "/" {
			name = "note.md"
		}
		resp, err := s.upload(dest, name, strings.NewReader(note.Body))
		if err != nil {
			return out, fmt.Errorf("attaching note %s: %w", n, err)
		}
		p, _ := resp["path"].(string)
		out = append(out, attached{Path: p, NotePath: note.Path})
	}
	return out, nil
}

// validateLocalFile checks a path the way the server would, before spending a
// round trip on it: absolute, exists, a plain file, within the size limit.
func validateLocalFile(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%s is not an absolute path", p)
	}
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("%s: %w", p, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory, not a file", p)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", p)
	}
	if info.Size() > maxLocalAttachment {
		return fmt.Errorf("%s is %d bytes, over the 25 MiB attachment limit", p, info.Size())
	}
	return nil
}

// composeMessage builds the one message a session actually receives: the
// caller's own words, followed by where every attachment landed, so the
// agent can go read them without being told twice (once in the parameters,
// once by lectern).
func composeMessage(lead string, files []attached) string {
	var b strings.Builder
	b.WriteString(lead)
	if len(files) > 0 {
		if lead != "" {
			b.WriteString("\n\n")
		}
		b.WriteString("Context files:\n")
		for _, f := range files {
			if f.NotePath != "" {
				fmt.Fprintf(&b, "- %s (Grimoire note: %s)\n", f.Path, f.NotePath)
			} else {
				fmt.Fprintf(&b, "- %s\n", f.Path)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// summarize turns free text into a short title fragment, e.g. for a Grimoire
// note title derived from a prompt rather than an explicit name.
func summarize(text string, maxWords int) string {
	words := strings.Fields(text)
	if len(words) <= maxWords {
		return strings.Join(words, " ")
	}
	return strings.Join(words[:maxWords], " ") + "…"
}

// stringSlice pulls a []string out of a decoded-JSON []any argument,
// dropping anything that isn't a non-empty string rather than erroring —
// tool arguments are written by a model, and a stray null is not worth
// failing the whole call over.
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if str, ok := x.(string); ok && strings.TrimSpace(str) != "" {
			out = append(out, str)
		}
	}
	return out
}

// attachHint is the one-line "how do I look at this" a tool result ends
// with — cmd/lectern/client.go's `lectern attach KIND ID`.
func attachHint(id int64) string {
	return fmt.Sprintf("lectern attach session %d", id)
}

// orDefault returns v unless it's empty, in which case it returns def —
// several packages in this repo have their own copy of this rather than a
// shared one; this is this package's.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// startSessionResult is the JSON start_session hands back: enough to find
// and watch the session, plus where everything it was given landed.
func startSessionResult(sess map[string]any, files []attached, notePath, grimoireErr string) map[string]any {
	id, _ := sess["id"].(float64)
	out := map[string]any{
		"id": sess["id"], "name": sess["name"], "status": sess["status"],
		"attach_hint": attachHint(int64(id)),
	}
	if len(files) > 0 {
		paths := make([]string, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		out["attached_paths"] = paths
	}
	if notePath != "" {
		out["grimoire_note"] = notePath
	}
	if grimoireErr != "" {
		out["grimoire_error"] = grimoireErr
	}
	return out
}

// resolveSessionRef finds one session by numeric id or by name, for
// send_to_session — a caller mid-conversation knows what it named the
// session (or what the operator called it), not its database id. An
// ambiguous or missing name fails with the candidates list_sessions would
// show, so the model can disambiguate on its own next call instead of
// guessing.
func (s *Server) resolveSessionRef(ref string) (map[string]any, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("give session: a session id, or its exact/unique name")
	}
	if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
		sess, err := s.object(fmt.Sprintf("/sessions/%d", id))
		if err != nil {
			return nil, fmt.Errorf("no session #%d: %w", id, err)
		}
		return sess, nil
	}
	rows, err := s.list("/sessions?all=true")
	if err != nil {
		return nil, err
	}
	var exact, contains []map[string]any
	lower := strings.ToLower(ref)
	for _, r := range rows {
		name, _ := r["name"].(string)
		switch {
		case name == ref:
			exact = append(exact, r)
		case strings.Contains(strings.ToLower(name), lower):
			contains = append(contains, r)
		}
	}
	candidates := exact
	if len(candidates) == 0 {
		candidates = contains
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no session named %q — call list_sessions to see what's open", ref)
	}
	if len(candidates) > 1 {
		sort.Slice(candidates, func(i, j int) bool {
			ni, _ := candidates[i]["id"].(float64)
			nj, _ := candidates[j]["id"].(float64)
			return ni < nj
		})
		var names []string
		for _, c := range candidates {
			names = append(names, fmt.Sprintf("#%v %q", c["id"], c["name"]))
		}
		return nil, fmt.Errorf("%q matches more than one open session: %s — call again with the numeric id",
			ref, strings.Join(names, ", "))
	}
	return candidates[0], nil
}
