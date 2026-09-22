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

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/shellq"
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
	target, err := s.DB.Target(targetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if target.Kind == "sandbox" {
		httpError(w, 409, "file attachments are not available for disposable sandbox tasks")
		return
	}
	if !path.IsAbs(workdir) || strings.ContainsAny(workdir, "\x00\r\n") {
		httpError(w, 409, "this session needs an absolute working directory before attaching files")
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
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
	name := path.Base(strings.ReplaceAll(f.Filename, "\\", "/"))
	name = strings.Map(func(c rune) rune {
		if unicode.IsControl(c) {
			return -1
		}
		return c
	}, name)
	if name == "" || name == "." || name == ".." || len(name) > 180 {
		httpError(w, 400, "choose a filename between 1 and 180 bytes")
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
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		respondErr(w, err)
		return
	}
	dir := path.Join(workdir, ".lectern", "context", hex.EncodeToString(nonce[:]))
	dest := path.Join(dir, name)
	stage := path.Join(dir, ".upload")
	if stage == dest {
		stage += ".partial"
	}
	// Wrapped SSH targets can need several small writes to fit their outer shell.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
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
	if err := run(fmt.Sprintf("test -d %s && umask 077 && mkdir -p %s && mkdir -m 700 %s", shellq.Quote(workdir), shellq.Quote(path.Dir(dir)), shellq.Quote(dir))); err != nil {
		httpError(w, 502, "upload failed: %s", err)
		return
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
	if err := ex.WriteFile(ctx, stage, data); err != nil {
		httpError(w, 502, "upload failed: %s", err)
		return
	}
	if err := run(fmt.Sprintf("test \"$(wc -c < %s)\" -eq %d && chmod 600 %s && mv -- %s %s", shellq.Quote(stage), len(data), shellq.Quote(stage), shellq.Quote(stage), shellq.Quote(dest))); err != nil {
		httpError(w, 502, "upload could not be verified: %s", err)
		return
	}
	// Lectern worktrees already exclude .lectern; adopted repositories may
	// not. Use the local exclude file, preserving the project's .gitignore.
	ignore := fmt.Sprintf("if git -C %s rev-parse --git-dir >/dev/null 2>&1; then ex_file=$(git -C %s rev-parse --path-format=absolute --git-path info/exclude) && { grep -qxF '.lectern/' \"$ex_file\" 2>/dev/null || printf '\\n.lectern/\\n' >> \"$ex_file\"; }; fi", shellq.Quote(workdir), shellq.Quote(workdir))
	if err := run(ignore); err != nil {
		httpError(w, 502, "could not exclude context files from git: %s", err)
		return
	}
	complete = true
	writeJSON(w, 201, map[string]any{"name": name, "path": dest, "size": len(data)})
}
