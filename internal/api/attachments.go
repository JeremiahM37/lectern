package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

const maxAttachmentSize = 25 << 20

func (s *Server) uploadSessionAttachment(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.Status == sessions.StatusDead {
		httpError(w, 409, "this session has ended")
		return
	}
	s.uploadAttachment(w, r, row.TargetID, row.Workdir)
}

func (s *Server) uploadTaskAttachment(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	project, err := s.DB.Project(task.ProjectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	// The repository survives individual task worktrees and follow-up attempts.
	s.uploadAttachment(w, r, project.TargetID, project.RepoPath)
}

// Upload stages context only. The operator sends the returned path through the
// normal composer, so uploading never types into a shell or starts an agent turn.
func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request, targetID int64, workdir string) {
	if _, err := s.attachTarget(targetID, workdir); err != nil {
		stageRespond(w, err)
		return
	}
	s.uploadMu.Lock()
	if s.uploadCount >= 4 {
		s.uploadMu.Unlock()
		httpError(w, 429, "uploads are busy; try again shortly")
		return
	}
	s.uploadCount++
	s.uploadMu.Unlock()
	defer func() { s.uploadMu.Lock(); s.uploadCount--; s.uploadMu.Unlock() }()
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentSize+(64<<10))
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpError(w, 413, "file exceeds 25 MiB")
		} else {
			httpError(w, 400, "invalid upload; choose one file up to 25 MiB")
		}
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 {
		httpError(w, 400, "choose one file per upload")
		return
	}
	f := files[0]
	if f.Size > maxAttachmentSize {
		httpError(w, 413, "file exceeds 25 MiB")
		return
	}
	file, err := f.Open()
	if err != nil {
		httpError(w, 400, "could not read uploaded file")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAttachmentSize+1))
	if err != nil || len(data) > maxAttachmentSize {
		httpError(w, 413, "could not read file within the 25 MiB limit")
		return
	}
	staged, err := s.stageAttachments(r.Context(), targetID, workdir, []stagedFile{{Name: f.Filename, Data: data}})
	if err != nil {
		stageRespond(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"name": staged[0].Name, "path": staged[0].Path, "size": len(data)})
}

// stagedFile is one file to put beside a session's work, and where it went.
type stagedFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Size int    `json:"size"`
	Data []byte `json:"-"`
}

type stageError struct {
	status int
	msg    string
}

func (e *stageError) Error() string { return e.msg }

func stageFail(status int, format string, args ...any) error {
	return &stageError{status: status, msg: fmt.Sprintf(format, args...)}
}

func stageRespond(w http.ResponseWriter, err error) {
	var se *stageError
	if errors.As(err, &se) {
		httpError(w, se.status, "%s", se.msg)
	} else {
		respondErr(w, err)
	}
}

// attachTarget is where attachments for workdir can go, or why they cannot.
func (s *Server) attachTarget(targetID int64, workdir string) (executor.Executor, error) {
	target, err := s.DB.Target(targetID)
	if err != nil {
		return nil, err
	}
	if target.Kind == "sandbox" {
		return nil, stageFail(409, "file attachments are not available for disposable sandbox tasks")
	}
	if !path.IsAbs(workdir) || strings.ContainsAny(workdir, "\x00\r\n") {
		return nil, stageFail(409, "this session needs an absolute working directory before attaching files")
	}
	return s.Reg.For(target)
}

// attachmentName keeps a file's base name, minus anything that could steer a
// path or a terminal.
func attachmentName(raw string) (string, error) {
	name := path.Base(strings.ReplaceAll(raw, "\\", "/"))
	name = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, name)
	if name == "" || name == "." || name == ".." || len(name) > 180 {
		return "", stageFail(400, "choose a filename between 1 and 180 bytes")
	}
	return name, nil
}

// stageAttachments writes files into one fresh, private directory under
// workdir/.lectern/context on the target: the one path every attachment takes,
// whether a person uploaded it or Design Mode made it. Nothing is typed or
// sent; callers pass the returned paths on themselves.
func (s *Server) stageAttachments(parent context.Context, targetID int64, workdir string, files []stagedFile) ([]stagedFile, error) {
	ex, err := s.attachTarget(targetID, workdir)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	dir := path.Join(workdir, ".lectern", "context", hex.EncodeToString(nonce[:]))
	// Wrapped SSH targets can need several small writes to fit their outer shell.
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	run := func(cmd string) error {
		res, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30})
		if err != nil {
			return err
		}
		if !res.OK() {
			return fmt.Errorf("target could not store the file: %s", strings.TrimSpace(res.Stderr))
		}
		return nil
	}
	out := make([]stagedFile, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		name, err := attachmentName(f.Name)
		if err != nil {
			return nil, err
		}
		if seen[name] {
			return nil, stageFail(400, "two files are both named %s", name)
		}
		if len(f.Data) > maxAttachmentSize {
			return nil, stageFail(413, "file exceeds 25 MiB")
		}
		seen[name] = true
		out = append(out, stagedFile{Name: name, Path: path.Join(dir, name), Size: len(f.Data), Data: f.Data})
	}
	if err := run(fmt.Sprintf("test -d %s && umask 077 && mkdir -p %s && mkdir -m 700 %s", shellq.Quote(workdir), shellq.Quote(path.Dir(dir)), shellq.Quote(dir))); err != nil {
		return nil, stageFail(502, "upload failed: %s", err)
	}
	complete := false
	defer func() {
		if !complete {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			// Only this request's randomly named directory is removed.
			ex.Run(cleanup, "rm -rf -- "+shellq.Quote(dir), executor.RunOpts{Timeout: 15})
		}
	}()
	stage := path.Join(dir, ".upload")
	if seen[".upload"] {
		stage += ".partial"
	}
	for _, f := range out {
		if err := ex.WriteFile(ctx, stage, f.Data); err != nil {
			return nil, stageFail(502, "upload failed: %s", err)
		}
		if err := run(fmt.Sprintf("test \"$(wc -c < %s)\" -eq %d && chmod 600 %s && mv -- %s %s", shellq.Quote(stage), len(f.Data), shellq.Quote(stage), shellq.Quote(stage), shellq.Quote(f.Path))); err != nil {
			return nil, stageFail(502, "upload could not be verified: %s", err)
		}
	}
	// Lectern worktrees already exclude .lectern; adopted repositories may
	// not. Use the local exclude file, preserving the project's .gitignore.
	ignore := fmt.Sprintf("if git -C %s rev-parse --git-dir >/dev/null 2>&1; then ex_file=$(git -C %s rev-parse --path-format=absolute --git-path info/exclude) && { grep -qxF '.lectern/' \"$ex_file\" 2>/dev/null || printf '\\n.lectern/\\n' >> \"$ex_file\"; }; fi", shellq.Quote(workdir), shellq.Quote(workdir))
	if err := run(ignore); err != nil {
		return nil, stageFail(502, "could not exclude context files from git: %s", err)
	}
	complete = true
	for i := range out {
		out[i].Data = nil
	}
	return out, nil
}
