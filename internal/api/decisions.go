// Decision log routes (internal/decisionlog, docs/decision-log.md). Both are
// read-only: the rows are written as decisions happen, never on request.
package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/decisionlog"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// decisionWindow parses ?days=, defaulting to 30 and bounded like the other
// reliability windows.
func decisionWindow(w http.ResponseWriter, r *http.Request) (int, bool) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 365 {
			httpError(w, 422, "days must be between 1 and 365")
			return 0, false
		}
		days = n
	}
	return days, true
}

// listDecisions is GET /api/decisions?days=30&kind=. Rows are newest first.
// kind, when given, must be one of the recorded kinds, so a typo is an error
// rather than an empty list that looks like "nothing happened".
func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) {
	days, ok := decisionWindow(w, r)
	if !ok {
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind != "" && !knownDecisionKind(kind) {
		httpError(w, 422, "kind must be one of: %s", strings.Join(decisionKinds, ", "))
		return
	}
	since := store.Now() - float64(days)*86400
	rows, err := s.DB.Decisions(since, kind, 0)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"days": days, "kind": kind, "rows": rows})
}

// decisionSummary is GET /api/decisions/summary?days=30: per kind, the count,
// the decision values, latency p50 and p90, and the latency p50 by how many
// sessions were live at the time.
func (s *Server) decisionSummary(w http.ResponseWriter, r *http.Request) {
	days, ok := decisionWindow(w, r)
	if !ok {
		return
	}
	since := store.Now() - float64(days)*86400
	rows, err := s.DB.Decisions(since, "", 0)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, decisionlog.Summarize(rows, days))
}

var decisionKinds = []string{
	decisionlog.KindApproval, decisionlog.KindTurnResponse, decisionlog.KindInterrupt,
	decisionlog.KindPermissionPrompt, decisionlog.KindReviewCommit, decisionlog.KindReviewPush,
	decisionlog.KindReviewCommentsSent, decisionlog.KindReviewDiscard, decisionlog.KindTaskReview,
}

func knownDecisionKind(kind string) bool {
	for _, k := range decisionKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// committedFiles counts the files in the commit HEAD now points at. It runs
// only when the decision log is on, and reads 0 on any error: the count is
// detail for the log, never a reason to fail a commit that already happened.
func (s *Server) committedFiles(ctx context.Context, ex executor.Executor, dir string) int {
	if s.Decisions == nil || !decisionlog.Enabled() {
		return 0
	}
	res, err := ex.Run(ctx, "git diff-tree --no-commit-id --name-only -r HEAD",
		executor.RunOpts{Cwd: dir, Timeout: 15})
	if err != nil || !res.OK() {
		return 0
	}
	n := 0
	for _, line := range strings.Split(res.Stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}
