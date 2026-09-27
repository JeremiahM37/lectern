package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/trackers"
)

// The pull request page and its actions: read the PR (timeline, checks,
// reviewers, labels, stack, merge options), drill into a failing job's log,
// see which files conflict, and — as a signed-in human — merge, arm or
// cancel auto-merge, request or remove reviewers, label, comment, close or
// reopen. Every call goes through the project's target and its gh/glab.

// forgeCtx resolves the project and its forge for a request.
func (s *Server) forgeCtx(w http.ResponseWriter, r *http.Request) (*store.Project, trackers.Forge, bool) {
	proj, ok := s.trackerProject(w, r)
	if !ok {
		return nil, nil, false
	}
	f, err := s.projectForge(r.Context(), proj)
	if err != nil {
		httpError(w, 424, "%s", err.Error())
		return nil, nil, false
	}
	return proj, f, true
}

func pathNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || n <= 0 {
		httpError(w, 404, "no such pull request or issue")
		return 0, false
	}
	return n, true
}

// itemKind maps the {kind} path segment to the forge's kind.
func itemKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	switch r.PathValue("kind") {
	case "prs":
		return "pr", true
	case "issues":
		return "issue", true
	}
	httpError(w, 404, "use prs or issues")
	return "", false
}

type prPageView struct {
	*trackers.PRDetail
	ProjectID int64 `json:"project_id"`
	// CI is the CI loop's watch on this PR, if it has one.
	CI *ciloop.View `json:"ci,omitempty"`
}

func (s *Server) forgePR(w http.ResponseWriter, r *http.Request) {
	proj, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	d, err := f.PR(ctx, n)
	if err != nil {
		trackerErr(w, err)
		return
	}
	v := prPageView{PRDetail: d, ProjectID: proj.ID}
	if s.CILoop != nil {
		v.CI = s.CILoop.Watching(d.URL)
	}
	writeJSON(w, 200, v)
}

func (s *Server) forgeIssue(w http.ResponseWriter, r *http.Request) {
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	d, err := f.Issue(r.Context(), n)
	if err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

// forgeMeta is what the label and reviewer pickers offer. Either half may
// fail on its own (no permission to list collaborators, say); the picker
// then falls back to typing a name.
func (s *Server) forgeMeta(w http.ResponseWriter, r *http.Request) {
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	out := map[string]any{"repo": f.Repo(), "kind": f.Kind()}
	if labels, err := f.Labels(r.Context()); err == nil {
		out["labels"] = labels
	} else {
		out["labels"], out["labels_error"] = []trackers.Label{}, err.Error()
	}
	if users, err := f.Users(r.Context()); err == nil {
		out["users"] = users
	} else {
		out["users"], out["users_error"] = []trackers.User{}, err.Error()
	}
	writeJSON(w, 200, out)
}

// forgeConflicts lists the files a conflicting PR conflicts on, worked out
// in the project's own clone on the target (see trackers.ConflictingFiles).
func (s *Server) forgeConflicts(w http.ResponseWriter, r *http.Request) {
	proj, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	d, err := f.PR(ctx, n)
	if err != nil {
		trackerErr(w, err)
		return
	}
	ex, err := s.trackerExec(proj)
	if err != nil {
		respondErr(w, err)
		return
	}
	base, head := f.FetchRefs(d)
	c := trackers.ConflictingFiles(ctx, ex, proj.RepoPath, base, head)
	writeJSON(w, 200, map[string]any{"mergeable": d.Mergeable, "conflicts": c})
}

// forgeCheckLog is the check drill-down: one job's log, cleaned and
// redacted by the CI loop's own pipeline, with a much longer tail than the
// loop sends an agent.
func (s *Server) forgeCheckLog(w http.ResponseWriter, r *http.Request) {
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	check := r.URL.Query().Get("check")
	if check == "" {
		httpError(w, 422, "check is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	d, err := f.PR(ctx, n)
	if err != nil {
		trackerErr(w, err)
		return
	}
	raw, err := f.JobLog(ctx, d, check)
	if err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"check": check, "log": ciloop.ViewLog(raw)})
}

type mergeIn struct {
	trackers.MergeRequest
	// Confirm must be true: the UI sets it only from its confirmation step,
	// so a stray or replayed request without it cannot merge anything.
	Confirm bool `json:"confirm"`
}

func (s *Server) forgeMerge(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "merging a pull request") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	var in mergeIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if !in.Confirm {
		httpError(w, 428, "merging needs an explicit confirmation (confirm: true)")
		return
	}
	if strings.TrimSpace(in.HeadSHA) == "" {
		httpError(w, 428, "merging needs the head commit you confirmed (head_sha), so a branch that moved since is not merged")
		return
	}
	if !oneOf(in.Method, "merge", "squash", "rebase") {
		httpError(w, 422, "method must be merge, squash or rebase")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 150*time.Second)
	defer cancel()
	out, err := f.Merge(ctx, n, in.MergeRequest)
	if err != nil {
		trackerErr(w, err)
		return
	}
	s.Log.Info("trackers: pull request merge requested", "repo", f.Repo().Path, "pr", n, "method", in.Method, "auto", in.Auto)
	d, err := f.PR(ctx, n)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true, "output": out})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "output": out, "pr": d})
}

func (s *Server) forgeDisableAutoMerge(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "changing auto-merge") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	if err := f.DisableAutoMerge(r.Context(), n); err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

type editListIn struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}

func cleanNames(in []string) []string {
	out := []string{}
	for _, s := range in {
		s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "@"))
		if s != "" && !strings.ContainsAny(s, ",\n") {
			out = append(out, s)
		}
	}
	return out
}

func (s *Server) forgeReviewers(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "changing reviewers") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	var in editListIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if err := f.EditReviewers(r.Context(), n, cleanNames(in.Add), cleanNames(in.Remove)); err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) forgeLabels(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "changing labels") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	kind, ok := itemKind(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	var in editListIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if err := f.EditLabels(r.Context(), kind, n, cleanNames(in.Add), cleanNames(in.Remove)); err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) forgeComment(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "commenting") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	kind, ok := itemKind(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	body, ok := commentBody(w, r)
	if !ok {
		return
	}
	if err := f.Comment(r.Context(), kind, n, body); err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) forgeState(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "closing or reopening") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	kind, ok := itemKind(w, r)
	if !ok {
		return
	}
	n, ok := pathNumber(w, r)
	if !ok {
		return
	}
	var in struct {
		Open *bool `json:"open"`
	}
	if err := decodeBody(r, &in); err != nil || in.Open == nil {
		httpError(w, 422, "open (true to reopen, false to close) is required")
		return
	}
	if err := f.SetState(r.Context(), kind, n, *in.Open); err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
