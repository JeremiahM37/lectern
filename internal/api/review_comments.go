package api

// Persistent inline review comments and "viewed" marks for a session's
// review workspace (docs/review.md). Comments live on the server so a phone
// and a desktop see the same drafts, and so a sent comment can be followed
// across the agent's edits: the client re-finds each comment's line by its
// code and surrounding context, and shows which ones the agent changed.

import (
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// reviewState is GET /api/sessions/{id}/review/state.
func (s *Server) reviewState(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	comments, err := s.DB.ReviewComments(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	viewed, err := s.DB.ReviewFileMarks(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"comments": comments, "viewed": viewed})
}

type reviewCommentIn struct {
	Repo          string  `json:"repo"`
	File          string  `json:"file"`
	Side          string  `json:"side"`
	Line          int     `json:"line"`
	Code          string  `json:"code"`
	ContextBefore string  `json:"context_before"`
	ContextAfter  string  `json:"context_after"`
	Text          *string `json:"text"`
	Status        string  `json:"status"`
}

func clipText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// addReviewComment is POST /api/sessions/{id}/review/comments.
func (s *Server) addReviewComment(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var body reviewCommentIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	text := ""
	if body.Text != nil {
		text = strings.TrimSpace(*body.Text)
	}
	if strings.TrimSpace(body.File) == "" || text == "" || body.Line < 0 {
		httpError(w, 422, "a comment needs a file, a line and text")
		return
	}
	if body.Side != "old" {
		body.Side = "new"
	}
	principal, _ := auth.FromContext(r.Context())
	c, err := s.DB.InsertReviewComment(&store.ReviewComment{
		SessionID: row.ID, Repo: body.Repo, File: body.File, Side: body.Side, Line: body.Line,
		Code: clipText(body.Code, 2000), ContextBefore: clipText(body.ContextBefore, 4000),
		ContextAfter: clipText(body.ContextAfter, 4000), Text: clipText(text, 8000),
		Author: principal.Login,
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, c)
}

func (s *Server) reviewCommentParam(w http.ResponseWriter, r *http.Request) (*store.ReviewComment, bool) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return nil, false
	}
	cid, err := pathID(r, "cid")
	if err != nil {
		httpError(w, 404, "no such comment")
		return nil, false
	}
	c, err := s.DB.ReviewComment(cid)
	if err != nil || c.SessionID != row.ID {
		httpError(w, 404, "no such comment")
		return nil, false
	}
	return c, true
}

// updateReviewComment is PATCH /api/sessions/{id}/review/comments/{cid}: edit
// the text, re-anchor it (line/code/context), resolve it, or reopen it.
// Reopening keeps the round, so the next send says it is raised again.
func (s *Server) updateReviewComment(w http.ResponseWriter, r *http.Request) {
	c, ok := s.reviewCommentParam(w, r)
	if !ok {
		return
	}
	var body reviewCommentIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{}
	if body.Text != nil {
		text := strings.TrimSpace(*body.Text)
		if text == "" {
			httpError(w, 422, "comment text cannot be empty")
			return
		}
		fields["text"] = clipText(text, 8000)
	}
	if body.Line > 0 {
		fields["line"] = body.Line
		fields["code"] = clipText(body.Code, 2000)
		fields["context_before"] = clipText(body.ContextBefore, 4000)
		fields["context_after"] = clipText(body.ContextAfter, 4000)
	}
	switch body.Status {
	case "":
	case "resolved":
		fields["status"], fields["resolved_at"] = "resolved", store.Now()
	case "draft":
		fields["status"], fields["resolved_at"] = "draft", nil
	default:
		httpError(w, 422, "status must be resolved or draft")
		return
	}
	if err := s.DB.Update("review_comments", c.ID, fields); err != nil {
		respondErr(w, err)
		return
	}
	out, err := s.DB.ReviewComment(c.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// deleteReviewComment is DELETE /api/sessions/{id}/review/comments/{cid}.
func (s *Server) deleteReviewComment(w http.ResponseWriter, r *http.Request) {
	c, ok := s.reviewCommentParam(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteReviewComment(c.ID); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"deleted": c.ID})
}

// setReviewViewed is PUT /api/sessions/{id}/review/viewed: mark one file
// viewed at the fingerprint of the diff the reviewer saw, or clear it.
func (s *Server) setReviewViewed(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Repo        string `json:"repo"`
		Path        string `json:"path"`
		Fingerprint string `json:"fingerprint"`
	}
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(body.Path) == "" || len(body.Fingerprint) > 128 {
		httpError(w, 422, "a file path is required")
		return
	}
	if err := s.DB.SetReviewFileMark(row.ID, body.Repo, body.Path, body.Fingerprint); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// storedReviewBatch turns stored comments into one send: the comments in
// wire form (a reopened one names the round it was first raised in) and the
// round this send becomes.
func storedReviewBatch(all []*store.ReviewComment, ids []int64) ([]reviewComment, []int64, int, error) {
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	round := 0
	for _, c := range all {
		if c.Round > round {
			round = c.Round
		}
	}
	round++
	var out []reviewComment
	var sent []int64
	for _, c := range all {
		if !want[c.ID] {
			continue
		}
		if c.Status != "draft" {
			return nil, nil, 0, invalid("comment %d was already sent; reopen it to send it again", c.ID)
		}
		out = append(out, reviewComment{File: c.File, Line: c.Line, Side: c.Side, Text: c.Text,
			Code: c.Code, PreviousRound: c.Round})
		sent = append(sent, c.ID)
		delete(want, c.ID)
	}
	if len(want) > 0 {
		return nil, nil, 0, invalid("some of those comments do not exist")
	}
	return out, sent, round, nil
}
