package api

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// workspaceFilesScript runs on the workspace's own target through the
// executor, so symlinks and path escapes are judged on the machine that holds
// the files (docs/files.md). Every file endpoint goes through it, or through
// its Go port (`lectern helper workspace-files`) when the target has lectern.
//
//go:embed workspace_files.py
var workspaceFilesScript string

// workspaceFileLimit matches the script's CAP: the largest file that can be
// read, edited or saved through the browser.
const workspaceFileLimit = 25 << 20

type workspaceRef struct {
	ex   executor.Executor
	dir  string
	mode string
}

// workspaceFor resolves the attachment's workspace from durable records; a
// client never supplies the root. Sandboxed tasks have no file access.
func (s *Server) workspaceFor(w http.ResponseWriter, r *http.Request) (workspaceRef, bool) {
	_, target, err := s.resolveAttachment(r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		httpError(w, 404, "%s", err)
		return workspaceRef{}, false
	}
	dir, _, err := s.terminalDirectory(r.PathValue("kind"), r.PathValue("id"))
	if err != nil || !path.IsAbs(dir) || target.Kind == "sandbox" {
		httpError(w, 409, "files unavailable for this workspace")
		return workspaceRef{}, false
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return workspaceRef{}, false
	}
	ref := workspaceRef{ex: ex, dir: dir}
	// Continuations may share an owned root without owning the allocation.
	// Resolve its role from durable records, not filenames supplied by clients.
	workspace, loadErr := s.Sessions.WorkspaceAt(target.ID, dir)
	if loadErr != nil {
		respondErr(w, loadErr)
		return workspaceRef{}, false
	}
	if workspace != nil {
		ref.mode = "grouped"
	}
	return ref, true
}

// runWorkspaceScript returns the script's JSON and exit status: 0 success,
// 1 refusal ({"error"}), 3 write conflict ({"conflict"}).
func runWorkspaceScript(ctx context.Context, ref workspaceRef, timeout float64, action, rel string, extra ...string) (map[string]json.RawMessage, int, error) {
	args := append([]string{ref.dir, rel, action, ref.mode}, extra...)
	parts := []string{"python3 -c", shellq.Quote(workspaceFilesScript)}
	for _, arg := range args {
		parts = append(parts, shellq.Quote(arg))
	}
	cmd := helpers.Command(ref.ex, "workspace-files", args, strings.Join(parts, " "))
	result, err := ref.ex.Run(ctx, cmd, executor.RunOpts{Timeout: timeout})
	if err != nil {
		return nil, 0, err
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(result.Stdout), &out); err != nil {
		return nil, result.RC, errors.New("unreadable file response")
	}
	return out, result.RC, nil
}

// workspaceFileAction runs one script action and answers failures itself.
func (s *Server) workspaceFileAction(w http.ResponseWriter, r *http.Request, action, rel string, extra ...string) (map[string]json.RawMessage, bool) {
	ref, ok := s.workspaceFor(w, r)
	if !ok {
		return nil, false
	}
	timeout := 60.0
	if action == "archive" {
		timeout = 120
	}
	out, rc, err := runWorkspaceScript(r.Context(), ref, timeout, action, rel, extra...)
	return out, answerScript(w, out, rc, err)
}

func answerScript(w http.ResponseWriter, out map[string]json.RawMessage, rc int, err error) bool {
	switch {
	case err != nil:
		httpError(w, 502, "could not read workspace files")
		return false
	case rc == 3:
		var conflict map[string]any
		json.Unmarshal(out["conflict"], &conflict)
		writeJSON(w, 409, conflict)
		return false
	case rc != 0:
		var message string
		json.Unmarshal(out["error"], &message)
		if message == "" {
			message = "the file operation failed"
		}
		httpError(w, 400, "%s", message)
		return false
	}
	return true
}

// Editing a workspace is a consequential action, like deciding an approval:
// in tailscale mode a local process (an agent on this machine) is not a
// person and may not write through the browser API. Token holders and signed
// in owners can; LECTERN_AUTH=none allows everyone, as for every such gate.
func (s *Server) workspaceWriter(w http.ResponseWriter, r *http.Request) bool {
	principal, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(principal) {
		httpError(w, 403, "changing workspace files requires your signed-in identity, not an automated caller")
		return false
	}
	return true
}

func (s *Server) workspaceStat(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.workspaceFileAction(w, r, "stat", r.URL.Query().Get("path")); ok {
		writeJSON(w, 200, out)
	}
}

// workspaceWrite saves a file. X-Lectern-Base names what the editor started
// from: the SHA-256 it read, "absent" to create a new file, or "any" to
// overwrite deliberately. A different file on disk is a 409 carrying its hash.
func (s *Server) workspaceWrite(w http.ResponseWriter, r *http.Request) {
	if !s.workspaceWriter(w, r) {
		return
	}
	base := r.Header.Get("X-Lectern-Base")
	if base != "absent" && base != "any" && (len(base) != 64 || strings.Trim(base, "0123456789abcdef") != "") {
		httpError(w, 428, "send X-Lectern-Base with the file hash you opened, absent or any")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" {
		httpError(w, 400, "choose a file")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, workspaceFileLimit+1))
	if err != nil || len(body) > workspaceFileLimit {
		httpError(w, 413, "file exceeds 25 MiB limit")
		return
	}
	ref, ok := s.workspaceFor(w, r)
	if !ok {
		return
	}
	// The bytes travel as a file, never as shell text. The script checks the
	// base, then moves them into place atomically and removes this copy.
	var random [16]byte
	rand.Read(random[:])
	staged := "/tmp/.lectern-edit-" + hex.EncodeToString(random[:])
	if err := ref.ex.WriteFile(r.Context(), staged, body); err != nil {
		httpError(w, 502, "could not transfer the file to its target")
		return
	}
	out, rc, err := runWorkspaceScript(r.Context(), ref, 60, "write", rel, staged, base)
	if err != nil {
		ref.ex.Run(context.WithoutCancel(r.Context()), "rm -f "+shellq.Quote(staged), executor.RunOpts{Timeout: 20})
	}
	if answerScript(w, out, rc, err) {
		writeJSON(w, 200, out)
	}
}

// workspaceOp creates, renames/moves or deletes an entry. A rename never
// replaces an existing entry, and the workspace root cannot be touched.
func (s *Server) workspaceOp(w http.ResponseWriter, r *http.Request) {
	if !s.workspaceWriter(w, r) {
		return
	}
	var req struct {
		Op   string `json:"op"`
		Path string `json:"path"`
		To   string `json:"to"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	var extra []string
	switch req.Op {
	case "mkdir", "create", "delete":
	case "rename":
		if req.To == "" {
			httpError(w, 400, "choose the new name")
			return
		}
		extra = []string{req.To}
	default:
		httpError(w, 400, "unknown file operation")
		return
	}
	if out, ok := s.workspaceFileAction(w, r, req.Op, req.Path, extra...); ok {
		writeJSON(w, 200, out)
	}
}

// workspaceArchive downloads a folder as a zip, built on the target.
func (s *Server) workspaceArchive(w http.ResponseWriter, r *http.Request) {
	rel := r.URL.Query().Get("path")
	out, ok := s.workspaceFileAction(w, r, "archive", rel)
	if !ok {
		return
	}
	var encoded string
	json.Unmarshal(out["data"], &encoded)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		respondErr(w, err)
		return
	}
	name := path.Base(path.Clean("/" + rel))
	if name == "/" || name == "." {
		name = "workspace"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".zip"}))
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// workspaceIndex lists every file for Quick Open: tracked and untracked
// files first, gitignored ones as a separate list.
func (s *Server) workspaceIndex(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.workspaceFileAction(w, r, "index", "."); ok {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, out)
	}
}

func (s *Server) workspaceGitStatus(w http.ResponseWriter, r *http.Request) {
	if out, ok := s.workspaceFileAction(w, r, "gitstatus", "."); ok {
		writeJSON(w, 200, out)
	}
}

// workspaceSearch is project-wide text search: ripgrep when the target has
// it, else git grep, else a plain walk. Results are capped at 2,000 lines.
func (s *Server) workspaceSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	flag := func(name string) string {
		if q.Get(name) == "1" || q.Get(name) == "true" {
			return "1"
		}
		return "0"
	}
	query := q.Get("q")
	if strings.TrimSpace(query) == "" || len(query) > 1000 {
		httpError(w, 400, "type something to search for")
		return
	}
	if out, ok := s.workspaceFileAction(w, r, "search", ".", query, flag("regex"), flag("case"), flag("word"), q.Get("include"), flag("ignored")); ok {
		writeJSON(w, 200, out)
	}
}

// watchSlots bounds the long-polls held open at once; each keeps a process
// running on its target. Past the bound the browser falls back to polling.
var watchSlots = make(chan struct{}, 32)

// workspaceWatch is a long-poll over the folders a client shows: it answers
// when one of them (or git's HEAD/index) changes, using inotify on Linux
// targets and a half-second scan elsewhere, or after `timeout` seconds.
func (s *Server) workspaceWatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dirs    []string `json:"dirs"`
		Token   string   `json:"token"`
		Timeout float64  `json:"timeout"`
	}
	if err := decodeBody(r, &req); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	if req.Timeout <= 0 || req.Timeout > 50 {
		req.Timeout = 25
	}
	if len(req.Dirs) > 64 {
		req.Dirs = req.Dirs[:64]
	}
	dirs, _ := json.Marshal(req.Dirs)
	if req.Dirs == nil {
		dirs = []byte("[]")
	}
	select {
	case watchSlots <- struct{}{}:
		defer func() { <-watchSlots }()
	default:
		httpError(w, 429, "too many file watches; polling instead")
		return
	}
	ref, ok := s.workspaceFor(w, r)
	if !ok {
		return
	}
	out, rc, err := runWorkspaceScript(r.Context(), ref, req.Timeout+20, "watch", ".", string(dirs), req.Token, strconv.FormatFloat(req.Timeout, 'f', -1, 64))
	if answerScript(w, out, rc, err) {
		writeJSON(w, 200, out)
	}
}
