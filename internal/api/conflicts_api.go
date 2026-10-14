package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/awareness"
	"github.com/JeremiahM37/lectern/v2/internal/checks"
	"github.com/JeremiahM37/lectern/v2/internal/conflicts"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// Semantic-conflict prediction, wired to the live session and attempt data.
// The pure parsing and prediction live in internal/conflicts; this file knows
// how to find each side's diff, and how to validate a pair by actually
// applying both and running the project's check command.
//
// Nothing here blocks or reorders an agent. GET /api/conflicts is a user
// request and may wait for git. The hook path reads only the cached result
// (conflictNote), and a refresh for that project runs in the background.

// conflictNoteMaxAge is how old a project's cached scan may be before a hook
// starts a background refresh. Hooks fire on every prompt, so this bounds git
// work to one scan per project per minute at most.
const conflictNoteMaxAge = time.Minute

// validateOutputBytes and validateOutputLines bound the check output kept in
// the validation response, the same tail shape the check runner stores.
const validateOutputBytes = 16000
const validateOutputLines = 200

func (s *Server) conflictScanner() *conflicts.Scanner {
	s.conflictsOnce.Do(func() {
		s.conflictsScan = conflicts.NewScanner(s.collectConflictSubjects)
	})
	return s.conflictsScan
}

// sessionRepoDirs lists the checkout directories a session's live diff is
// read from, mirroring the case split in sessionRepoDiffs. It does no git work,
// so the scanner can fingerprint a session without capturing its diff.
func sessionRepoDirs(row *store.Session) []string {
	var plan worktree.Interactive
	hasPlan := row.WorktreeJSON != "" && json.Unmarshal([]byte(row.WorktreeJSON), &plan) == nil
	switch {
	case hasPlan && len(plan.Repositories) > 0:
		var dirs []string
		for _, repo := range plan.Repositories {
			if repo.Worktree != nil {
				dirs = append(dirs, repo.Worktree.Path)
			}
		}
		return dirs
	case hasPlan:
		return []string{plan.Path}
	case row.Workdir != "":
		return []string{row.Workdir}
	}
	return nil
}

// conflictSubjectForSession builds the scanner's view of one live session.
func (s *Server) conflictSubjectForSession(row *store.Session) (conflicts.Subject, error) {
	ex, err := s.sessionExecutor(row)
	if err != nil {
		return conflicts.Subject{}, err
	}
	return conflicts.Subject{
		Ref: conflicts.Ref{Kind: "session", ID: row.ID, Title: row.Name},
		Fingerprint: func(ctx context.Context) (string, error) {
			dirs := sessionRepoDirs(row)
			if len(dirs) == 0 {
				return "", errNotGitRepo
			}
			h := sha256.New()
			for _, dir := range dirs {
				fpr, err := checks.WorktreeFingerprint(ctx, ex, dir)
				if err != nil {
					return "", err
				}
				h.Write([]byte(dir + "\x00" + fpr + "\x00"))
			}
			return hex.EncodeToString(h.Sum(nil)), nil
		},
		Diff: func(ctx context.Context) (string, error) {
			// A background scan: never write the index under a live agent.
			repos, err := s.sessionRepoDiffs(withReadOnlyDiff(ctx), ex, row)
			if err != nil {
				return "", err
			}
			return concatPatches(repos), nil
		},
	}, nil
}

// conflictSubjectForAttempt builds the scanner's view of one running attempt.
func (s *Server) conflictSubjectForAttempt(att *store.Attempt, task *store.Task, proj *store.Project) (conflicts.Subject, error) {
	ex, err := s.projectExecutor(proj)
	if err != nil {
		return conflicts.Subject{}, err
	}
	base := resolveBaseRef(task.BaseBranch, proj)
	dir := att.WorktreePath
	return conflicts.Subject{
		Ref: conflicts.Ref{Kind: "attempt", ID: att.ID, Title: task.Title},
		Fingerprint: func(ctx context.Context) (string, error) {
			if dir == "" {
				return "", errNotGitRepo
			}
			return checks.WorktreeFingerprint(ctx, ex, dir)
		},
		Diff: func(ctx context.Context) (string, error) {
			d, err := liveDiffRepo(withReadOnlyDiff(ctx), ex, dir, base)
			if err != nil {
				return "", err
			}
			return concatPatches([]repoDiff{d}), nil
		},
	}, nil
}

// projectExecutor is the executor for a project's target.
func (s *Server) projectExecutor(proj *store.Project) (executor.Executor, error) {
	target, err := s.DB.Target(proj.TargetID)
	if err != nil {
		return nil, err
	}
	return s.Reg.For(target)
}

// collectConflictSubjects lists a project's live sessions and running attempts.
// A session that has no project is not part of any project's scan.
func (s *Server) collectConflictSubjects(ctx context.Context, projectID int64) ([]conflicts.Subject, error) {
	proj, err := s.DB.Project(projectID)
	if err != nil {
		return nil, err
	}
	var out []conflicts.Subject
	live, err := s.DB.LiveSessions()
	if err != nil {
		return nil, err
	}
	for _, row := range live {
		if row.ProjectID == nil || *row.ProjectID != projectID {
			continue
		}
		// A worktree still being created is git's to own: reading it now can
		// collide with the checkout (index.lock) and fail the session's setup.
		if row.SetupState == "creating" {
			continue
		}
		sub, err := s.conflictSubjectForSession(row)
		if err != nil {
			continue
		}
		out = append(out, sub)
	}
	attempts, err := s.DB.AttemptsWhere(
		"status='running' AND task_id IN (SELECT id FROM tasks WHERE project_id=?)", projectID)
	if err != nil {
		return nil, err
	}
	for _, att := range attempts {
		task, err := s.DB.Task(att.TaskID)
		if err != nil {
			continue
		}
		sub, err := s.conflictSubjectForAttempt(att, task, proj)
		if err != nil {
			continue
		}
		out = append(out, sub)
	}
	return out, nil
}

// listConflicts is GET /api/conflicts?project_id=N. It returns every pair the
// scanner predicts as more than file overlap, as
// [{a:{kind,id,title}, b:{...}, level, reasons}].
func (s *Server) listConflicts(w http.ResponseWriter, r *http.Request) {
	pid, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("project_id")), 10, 64)
	if err != nil || pid <= 0 {
		httpError(w, 400, "project_id is required")
		return
	}
	pairs, err := s.conflictScanner().Scan(r.Context(), pid)
	if err != nil {
		respondErr(w, err)
		return
	}
	if pairs == nil {
		pairs = []conflicts.Pair{}
	}
	writeJSON(w, 200, pairs)
}

// conflictNote is the advisory line the hook path adds to a session's
// briefing. It reads only the cached scan, and warms the cache in the
// background when it is stale. It never waits on git.
func (s *Server) conflictNote(sess *store.Session) string {
	if sess == nil || sess.ProjectID == nil {
		return ""
	}
	sc := s.conflictScanner()
	sc.Warm(*sess.ProjectID, conflictNoteMaxAge)
	ref := conflicts.Ref{Kind: "session", ID: sess.ID}
	var lines []string
	for _, p := range conflicts.ForRef(sc.Last(*sess.ProjectID), ref) {
		if p.Level != conflicts.LevelSemantic {
			continue
		}
		other := p.B
		if other.Kind == ref.Kind && other.ID == ref.ID {
			other = p.A
		}
		reason := ""
		if len(p.Reasons) > 0 {
			reason = p.Reasons[0]
		}
		lines = append(lines, fmt.Sprintf(
			"Possible semantic conflict with %s: %s. Coordinate or validate before merging.",
			other.Label(), reason))
		if len(lines) == 2 {
			break
		}
	}
	return strings.Join(lines, "\n")
}

// ---- validate ---------------------------------------------------------------

// conflictSideIn names one side of a validation request. Exactly one of the two
// IDs is set.
type conflictSideIn struct {
	SessionID int64 `json:"session_id"`
	AttemptID int64 `json:"attempt_id"`
}

type validateIn struct {
	A conflictSideIn `json:"a"`
	B conflictSideIn `json:"b"`
}

// conflictSide is one resolved side of a validation: where its work lives and
// the exact patch it contributes against the shared base.
type conflictSide struct {
	ref      conflicts.Ref
	ex       executor.Executor
	targetID int64
	dir      string // checkout the diff was read from (same repo as the other side)
	base     string // merge-base sha the diff is taken against
	patch    string
	proj     *store.Project
}

// resolveConflictSide reads one side's live diff. A side must be a single
// repository: validation applies to one worktree, so a multi-repository
// workspace is refused rather than validated partially.
func (s *Server) resolveConflictSide(ctx context.Context, in conflictSideIn) (*conflictSide, error) {
	switch {
	case in.SessionID != 0:
		row, err := s.DB.Session(in.SessionID)
		if err != nil {
			return nil, errNotFound
		}
		ex, err := s.sessionExecutor(row)
		if err != nil {
			return nil, err
		}
		repos, err := s.sessionRepoDiffs(ctx, ex, row)
		if err != nil {
			return nil, err
		}
		if len(repos) != 1 {
			return nil, errValidateSingleRepo
		}
		return &conflictSide{
			ref:      conflicts.Ref{Kind: "session", ID: row.ID, Title: row.Name},
			ex:       ex,
			targetID: row.TargetID,
			dir:      repos[0].Dir,
			base:     repos[0].BaseRef,
			patch:    concatPatches(repos),
			proj:     s.projectOrNil(row.ProjectID),
		}, nil
	case in.AttemptID != 0:
		att, err := s.DB.Attempt(in.AttemptID)
		if err != nil {
			return nil, errNotFound
		}
		task, err := s.DB.Task(att.TaskID)
		if err != nil {
			return nil, errNotFound
		}
		proj, err := s.DB.Project(task.ProjectID)
		if err != nil {
			return nil, err
		}
		ex, err := s.projectExecutor(proj)
		if err != nil {
			return nil, err
		}
		d, err := liveDiffRepo(ctx, ex, att.WorktreePath, resolveBaseRef(task.BaseBranch, proj))
		if err != nil {
			return nil, err
		}
		return &conflictSide{
			ref:      conflicts.Ref{Kind: "attempt", ID: att.ID, Title: task.Title},
			ex:       ex,
			targetID: proj.TargetID,
			dir:      d.Dir,
			base:     d.BaseRef,
			patch:    concatPatches([]repoDiff{d}),
			proj:     proj,
		}, nil
	}
	return nil, errNotFound
}

var (
	errNotFound           = errors.New("no such session or attempt")
	errValidateSingleRepo = errors.New("validation needs each side to be a single repository")
)

// validateResult is the response of POST /api/conflicts/validate.
type validateResult struct {
	Applied         bool   `json:"applied"`
	TextualConflict bool   `json:"textual_conflict"`
	CheckStatus     string `json:"check_status"` // passed|failed|error|skipped
	Command         string `json:"command,omitempty"`
	OutputTail      string `json:"output_tail"`
	// Reason says why the check was skipped or the apply stopped, in words.
	Reason string `json:"reason,omitempty"`
}

// validateConflict is POST /api/conflicts/validate. It answers "would these two
// pass together?" by applying both diffs to a scratch worktree at the shared
// merge-base and running the project's check command there. It is a human
// action because it runs the project's command on the target.
func (s *Server) validateConflict(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "checking two sessions together") {
		return
	}
	var in validateIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 400, "body must be {a:{session_id|attempt_id}, b:{...}}")
		return
	}
	ctx := r.Context()
	a, err := s.resolveConflictSide(ctx, in.A)
	if err != nil {
		conflictSideErr(w, err)
		return
	}
	b, err := s.resolveConflictSide(ctx, in.B)
	if err != nil {
		conflictSideErr(w, err)
		return
	}
	if a.targetID != b.targetID {
		httpError(w, 409, "both sides must run on the same target")
		return
	}
	keyA, _, okA := awareness.ResolveRepoKey(ctx, a.ex, a.targetID, a.dir)
	keyB, _, okB := awareness.ResolveRepoKey(ctx, b.ex, b.targetID, b.dir)
	if !okA || !okB || keyA != keyB {
		httpError(w, 409, "both sides must be worktrees of the same repository")
		return
	}
	if a.base != b.base {
		httpError(w, 409, "the two sides were taken from different merge-bases; bring one onto the other's base first")
		return
	}
	res, err := s.runValidation(ctx, a, b)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// conflictSideErr maps a side-resolution failure to a status code.
func conflictSideErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		httpError(w, 404, "%s", err.Error())
	case errors.Is(err, errValidateSingleRepo), errors.Is(err, errNotGitRepo):
		httpError(w, 409, "%s", err.Error())
	default:
		respondErr(w, err)
	}
}

// runValidation builds the scratch worktree, applies both patches, runs the
// check command, and removes everything again. The removal is deferred and uses
// its own context, so a cancelled request still cleans up.
func (s *Server) runValidation(ctx context.Context, a, b *conflictSide) (validateResult, error) {
	q := executor.ShellQuote
	ex := a.ex
	mk, err := ex.Run(ctx, `mktemp -d "${TMPDIR:-/tmp}/lectern-conflict-XXXXXX"`, executor.RunOpts{Timeout: 30})
	if err != nil {
		return validateResult{}, err
	}
	if !mk.OK() || strings.TrimSpace(mk.Stdout) == "" {
		return validateResult{}, fmt.Errorf("could not create a scratch directory: %s", strings.TrimSpace(mk.Stderr))
	}
	tmp := strings.TrimSpace(mk.Stdout)
	wt := tmp + "/wt"

	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := fmt.Sprintf("git -C %s worktree remove --force %s >/dev/null 2>&1; git -C %s worktree prune >/dev/null 2>&1; rm -rf %s",
			q(a.dir), q(wt), q(a.dir), q(tmp))
		if _, err := ex.Run(cctx, cmd, executor.RunOpts{Timeout: 60}); err != nil {
			slog.Default().Warn("conflicts: scratch cleanup failed", "dir", tmp, "err", err)
		}
	}()

	add, err := ex.Run(ctx, fmt.Sprintf("git -C %s worktree add --detach %s %s", q(a.dir), q(wt), q(a.base)),
		executor.RunOpts{Timeout: 120})
	if err != nil {
		return validateResult{}, err
	}
	if !add.OK() {
		return validateResult{}, fmt.Errorf("could not create a scratch worktree: %s", strings.TrimSpace(add.Stderr))
	}

	res := validateResult{CheckStatus: "skipped"}
	for _, side := range []struct {
		name  string
		patch string
	}{{"A", a.patch}, {"B", b.patch}} {
		if strings.TrimSpace(side.patch) == "" {
			continue
		}
		pf := tmp + "/" + strings.ToLower(side.name) + ".patch"
		if err := ex.WriteFile(ctx, pf, []byte(side.patch)); err != nil {
			return validateResult{}, err
		}
		ap, err := ex.Run(ctx, fmt.Sprintf("git -C %s apply --3way %s", q(wt), q(pf)),
			executor.RunOpts{Timeout: 120})
		if err != nil {
			return validateResult{}, err
		}
		if !ap.OK() {
			res.TextualConflict = true
			res.Reason = "applying " + side.name + "'s changes failed: a textual conflict with the other side"
			res.OutputTail = clipTail(strings.TrimSpace(ap.Stderr + ap.Stdout))
			return res, nil
		}
	}
	res.Applied = true

	var cmd string
	ok := false
	if s.Checks != nil {
		cmd, ok = s.Checks.CommandIn(ctx, ex, a.proj, wt)
	}
	if !ok {
		res.Reason = "no check command is configured for this project"
		return res, nil
	}
	res.Command = cmd
	out, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: wt, Timeout: checks.DefaultTimeout.Seconds()})
	if err != nil {
		res.CheckStatus = "error"
		res.OutputTail = clipTail(err.Error())
		return res, nil
	}
	res.OutputTail = clipTail(out.Stdout + out.Stderr)
	if out.OK() {
		res.CheckStatus = "passed"
	} else {
		res.CheckStatus = "failed"
	}
	return res, nil
}

// clipTail keeps the last lines of check output, bounded in bytes, the way the
// check runner's own stored output is bounded.
func clipTail(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > validateOutputLines {
		lines = lines[len(lines)-validateOutputLines:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > validateOutputBytes {
		out = out[len(out)-validateOutputBytes:]
	}
	return out
}
