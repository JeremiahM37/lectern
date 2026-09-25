// Replay evals (docs/replay-evals.md): building eval cases out of a
// project's own merged-PR history, so "which agent/model is best for MY
// repository" is ground-truthed against what actually landed. This file is
// the target-facing orchestration — everything that needs a real gh/git call
// — around the pure logic in internal/replay.
package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/replay"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

const (
	defaultReplayN = 20
	maxReplayN     = 100
)

type replayImportIn struct {
	ProjectID       int64  `json:"project_id"`
	N               int    `json:"n"`
	MaxChangedLines int    `json:"max_changed_lines"`
	Name            string `json:"name"` // only required by createReplaySuite
}

func (in *replayImportIn) normalize() {
	if in.N <= 0 {
		in.N = defaultReplayN
	}
	if in.N > maxReplayN {
		in.N = maxReplayN
	}
}

// previewReplaySuite runs the whole import pipeline and reports every
// candidate — accepted or skipped, with why — without persisting anything.
// This is "New replay suite from merged PRs" step 1 in the UI: pick a
// project/N/size cap, see what would be imported before committing to it.
func (s *Server) previewReplaySuite(w http.ResponseWriter, r *http.Request) {
	var in replayImportIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	in.normalize()
	project, err := s.DB.Project(in.ProjectID)
	if err != nil {
		httpError(w, 400, "no such project")
		return
	}
	candidates, source, err := s.buildReplayCandidates(r.Context(), project, in.N, in.MaxChangedLines)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"source": source, "candidates": replayCandidateViews(candidates)})
}

// createReplaySuite runs the same pipeline and persists every accepted
// candidate as a replay eval case in a brand-new suite. It always creates a
// fresh suite (unlike YAML import, which replaces one of the same name) —
// re-running against a fast-moving repo is expected to produce a new suite
// with the newest PRs, not silently overwrite an older one someone may still
// be comparing runs against.
func (s *Server) createReplaySuite(w http.ResponseWriter, r *http.Request) {
	var in replayImportIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	in.normalize()
	if strings.TrimSpace(in.Name) == "" {
		httpError(w, 422, "name is required")
		return
	}
	project, err := s.DB.Project(in.ProjectID)
	if err != nil {
		httpError(w, 400, "no such project")
		return
	}
	candidates, source, err := s.buildReplayCandidates(r.Context(), project, in.N, in.MaxChangedLines)
	if err != nil {
		respondErr(w, err)
		return
	}
	accepted := 0
	for _, c := range candidates {
		if c.Accepted {
			accepted++
		}
	}
	if accepted == 0 {
		writeJSON(w, 409, map[string]any{
			"error":  "no candidate PRs survived filtering — see skip reasons",
			"source": source, "candidates": replayCandidateViews(candidates),
		})
		return
	}
	suite, err := s.DB.InsertEvalSuite(&store.EvalSuite{
		Name: in.Name, ProjectID: project.ID,
		Description: fmt.Sprintf("Replay suite from the last %d merged PRs (source: %s)", in.N, source),
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	for _, c := range candidates {
		if !c.Accepted || c.Case == nil {
			continue
		}
		if _, err := s.DB.InsertEvalCase(&store.EvalCase{
			SuiteID: suite.ID, Name: c.Case.Name, Prompt: c.Case.Prompt, BaseRef: c.Case.BaseRef,
			CheckCommand: c.Case.CheckCommand, TimeoutS: c.Case.TimeoutS,
			IsReplay: true, SourcePRNumber: c.Case.SourcePRNumber, ReferenceDiff: c.Case.ReferenceDiff,
		}); err != nil {
			respondErr(w, err)
			return
		}
	}
	writeJSON(w, 201, map[string]any{
		"suite": suite, "source": source, "candidates": replayCandidateViews(candidates),
	})
}

// replayCandidateViews shapes the pipeline's output for the API response.
// The reference diff itself is left out — the preview only needs to show
// intent (name/prompt/check_command), not the full patch; the run/result
// view is where a reference diff earns its place, shown next to an
// attempt's own diff (see replayCaseView).
func replayCandidateViews(candidates []replay.Candidate) []map[string]any {
	out := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		row := map[string]any{
			"pr_number": c.PR.Number, "title": c.PR.Title, "accepted": c.Accepted,
			"skip_reason": c.SkipReason, "changed_lines": c.ChangedLines,
		}
		if c.Case != nil {
			row["case"] = map[string]any{
				"name": c.Case.Name, "prompt": c.Case.Prompt, "base_ref": c.Case.BaseRef,
				"check_command": c.Case.CheckCommand, "matched_tests": c.Case.MatchedTests,
			}
		}
		out = append(out, row)
	}
	return out
}

// replayCaseView adds a display-ready, pre-split reference diff to a replay
// case for a response that shows it next to an attempt's own diff — see
// evalRunView and getEvalSuite in evals.go. Kept out of store.EvalCase's own
// JSON (ReferenceDiff is tagged json:"-") so every OTHER eval endpoint that
// merely lists cases doesn't carry a potentially large diff it never
// displays.
type replayCaseView struct {
	*store.EvalCase
	ReferenceFiles []worktree.FilePatch `json:"reference_files,omitempty"`
}

func replayCaseViews(cases []*store.EvalCase) []replayCaseView {
	out := make([]replayCaseView, 0, len(cases))
	for _, c := range cases {
		v := replayCaseView{EvalCase: c}
		if c.IsReplay && c.ReferenceDiff != "" {
			v.ReferenceFiles = worktree.SplitPatch(c.ReferenceDiff)
		}
		out = append(out, v)
	}
	return out
}

// buildReplayCandidates fetches a project's merged PRs (gh, falling back to
// walking merge commits) and runs each through internal/replay's two-phase
// pipeline: PreFilter decides accept/skip from cheap metadata alone, and
// only a PR that survives has its parent commit resolved and its diff
// fetched (the two git calls this package avoids for anything that would be
// filtered out anyway).
func (s *Server) buildReplayCandidates(ctx context.Context, project *store.Project, n, maxChangedLines int,
) ([]replay.Candidate, string, error) {
	target, err := s.DB.Target(project.TargetID)
	if err != nil {
		return nil, "", err
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil, "", err
	}
	fetched, source, err := s.fetchReplayPRs(ctx, ex, project, n)
	if err != nil {
		return nil, "", err
	}
	opts := replay.Options{MaxChangedLines: maxChangedLines}
	candidates := make([]replay.Candidate, 0, len(fetched))
	for _, item := range fetched {
		pr := item.pr
		skip, lang, testFiles := replay.PreFilter(pr, project.VerifyCmd, opts)
		if skip != "" {
			candidates = append(candidates, replay.Candidate{PR: pr, SkipReason: skip, ChangedLines: replay.ChangedLines(pr.Files)})
			continue
		}
		parentSHA := item.parentSHA
		if parentSHA == "" {
			parentSHA, err = s.replayParentSHA(ctx, ex, project, pr.MergeSHA)
			if err != nil {
				candidates = append(candidates, replay.Candidate{PR: pr,
					SkipReason:   "could not resolve the commit before this PR: " + err.Error(),
					ChangedLines: replay.ChangedLines(pr.Files)})
				continue
			}
		}
		diff, err := s.replayDiff(ctx, ex, project, parentSHA, pr.MergeSHA)
		if err != nil {
			candidates = append(candidates, replay.Candidate{PR: pr,
				SkipReason: "could not fetch the PR's diff: " + err.Error(), ChangedLines: replay.ChangedLines(pr.Files)})
			continue
		}
		c := replay.BuildCase(pr, project.VerifyCmd, lang, testFiles, parentSHA, diff)
		candidates = append(candidates, replay.Candidate{
			PR: pr, Accepted: true, Case: &c, ChangedLines: replay.ChangedLines(pr.Files)})
	}
	return candidates, source, nil
}

// prFetchResult carries a PR plus, when the fetch path already had to
// resolve it (the git-log fallback needs the parent commit just to compute
// Files via --numstat), its parent SHA — so the expensive phase above never
// redoes a git call the fetch already made. Empty for the gh path, where
// Files comes free from JSON and the parent commit is only resolved for
// PRs that survive PreFilter.
type prFetchResult struct {
	pr        replay.PR
	parentSHA string
}

// fetchReplayPRs lists a project's last N merged PRs via `gh pr list`, and
// falls back to walking first-parent merge commits directly when gh is
// unavailable (not installed, not logged in, or the repo has no GitHub
// remote) — covering the same target-agnostic contract the rest of the
// eval-import code already keeps (internal/api/evals.go's importEvalSuites
// runs on the target's own gh/git, never the control plane's).
func (s *Server) fetchReplayPRs(ctx context.Context, ex executor.Executor, project *store.Project, n int,
) ([]prFetchResult, string, error) {
	cmd := fmt.Sprintf(
		"gh pr list --state merged --limit %d --json number,title,body,mergeCommit,baseRefName,files,closingIssuesReferences",
		n)
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: project.RepoPath, Timeout: 60})
	if err == nil && res.OK() {
		if prs, perr := replay.ParseGHPRList([]byte(res.Stdout)); perr == nil {
			out := make([]prFetchResult, len(prs))
			for i, pr := range prs {
				out[i] = prFetchResult{pr: pr}
			}
			return out, "gh", nil
		}
	}

	logCmd := fmt.Sprintf("git log --merges --first-parent -n %d --pretty=format:%s",
		n, shellq.Quote(replay.MergeLogFormat))
	logRes, lerr := ex.Run(ctx, logCmd, executor.RunOpts{Cwd: project.RepoPath, Timeout: 60})
	if lerr != nil {
		return nil, "", fmt.Errorf("gh unavailable and git log fallback failed: %w", lerr)
	}
	if !logRes.OK() {
		return nil, "", fmt.Errorf("gh unavailable and git log --merges failed: %s", strings.TrimSpace(logRes.Stderr))
	}
	prs := replay.ParseMergeLog(logRes.Stdout)
	out := make([]prFetchResult, 0, len(prs))
	for _, pr := range prs {
		// The fallback has no `files` metadata at all — unlike gh, even
		// deciding the cheap filters (size cap, docs-only) needs a git call
		// here, so it happens eagerly for every candidate rather than only
		// for survivors. This is the documented cost of not having gh
		// (docs/replay-evals.md).
		parentSHA, perr := s.replayParentSHA(ctx, ex, project, pr.MergeSHA)
		if perr != nil {
			out = append(out, prFetchResult{pr: pr})
			continue
		}
		files, ferr := s.replayNumstat(ctx, ex, project, parentSHA, pr.MergeSHA)
		if ferr == nil {
			pr.Files = files
		}
		out = append(out, prFetchResult{pr: pr, parentSHA: parentSHA})
	}
	return out, "git-log", nil
}

func (s *Server) replayParentSHA(ctx context.Context, ex executor.Executor, project *store.Project, mergeSHA string) (string, error) {
	res, err := ex.Run(ctx, "git rev-parse "+shellq.Quote(mergeSHA+"^1"),
		executor.RunOpts{Cwd: project.RepoPath, Timeout: 20})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", fmt.Errorf("git rev-parse %s^1: %s", mergeSHA, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

func (s *Server) replayNumstat(ctx context.Context, ex executor.Executor, project *store.Project, parentSHA, mergeSHA string) ([]replay.FileStat, error) {
	res, err := ex.Run(ctx, fmt.Sprintf("git diff --numstat %s %s", shellq.Quote(parentSHA), shellq.Quote(mergeSHA)),
		executor.RunOpts{Cwd: project.RepoPath, Timeout: 30})
	if err != nil {
		return nil, err
	}
	if !res.OK() {
		return nil, fmt.Errorf("git diff --numstat: %s", strings.TrimSpace(res.Stderr))
	}
	return parseNumstat(res.Stdout), nil
}

func parseNumstat(out string) []replay.FileStat {
	var files []replay.FileStat
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		add := atoiOrZero(parts[0]) // "-" (binary file) parses to 0, which is right
		del := atoiOrZero(parts[1])
		files = append(files, replay.FileStat{Path: parts[2], Additions: add, Deletions: del})
	}
	return files
}

func atoiOrZero(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func (s *Server) replayDiff(ctx context.Context, ex executor.Executor, project *store.Project, parentSHA, mergeSHA string) (string, error) {
	res, err := ex.Run(ctx, fmt.Sprintf("git diff %s %s", shellq.Quote(parentSHA), shellq.Quote(mergeSHA)),
		executor.RunOpts{Cwd: project.RepoPath, Timeout: 60})
	if err != nil {
		return "", err
	}
	if !res.OK() {
		return "", fmt.Errorf("git diff %s %s: %s", parentSHA, mergeSHA, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}
