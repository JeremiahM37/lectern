package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/trackers"
)

// Reactions, the merge queue view and rich-text description edits for the
// Tasks hub (docs/trackers.md). All of them change a tracker, so each needs
// a signed-in human.

type reactIn struct {
	// Subject is a comment's id from the timeline; empty reacts to the
	// pull request or issue itself.
	Subject string `json:"subject"`
	Emoji   string `json:"emoji"`
}

func decodeReact(w http.ResponseWriter, r *http.Request) (reactIn, bool) {
	var in reactIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return in, false
	}
	if !trackers.ValidEmoji(in.Emoji) {
		httpError(w, 422, "emoji must be one of %s", strings.Join(trackers.Emojis, ", "))
		return in, false
	}
	return in, true
}

func (s *Server) forgeReact(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "reacting") {
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
	in, ok := decodeReact(w, r)
	if !ok {
		return
	}
	if err := f.React(r.Context(), kind, n, in.Subject, in.Emoji); err != nil {
		unsupportedOr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// unsupportedOr answers 501 for something the host does not have, and the
// usual tracker error otherwise.
func unsupportedOr(w http.ResponseWriter, err error) {
	if errors.Is(err, trackers.ErrUnsupported) {
		httpError(w, 501, "%s", err.Error())
		return
	}
	trackerErr(w, err)
}

func (s *Server) trackerIssueReact(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "reacting") {
		return
	}
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	in, ok := decodeReact(w, r)
	if !ok {
		return
	}
	if c.Kind != "linear" {
		httpError(w, 501, "%s issues have no emoji reactions", forgeName[c.Kind])
		return
	}
	l := s.linearClient(c)
	d, err := l.Issue(r.Context(), r.PathValue("key"))
	if err != nil {
		trackerErr(w, err)
		return
	}
	if in.Subject != "" {
		known := false
		for _, e := range d.Timeline {
			known = known || e.ID == in.Subject
		}
		if !known {
			httpError(w, 422, "that comment is not on %s", d.ID)
			return
		}
	}
	if err := l.React(r.Context(), d.UID, in.Subject, in.Emoji); err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// trackerIssueDescription replaces a Linear or Jira issue's description with
// Markdown from the editor. When the Jira description holds formatting the
// editor cannot keep, the caller must say so (confirm_lossy) — the page
// warns first.
func (s *Server) trackerIssueDescription(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "editing an issue") {
		return
	}
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Body         string `json:"body"`
		ConfirmLossy bool   `json:"confirm_lossy"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if len(in.Body) > 200000 {
		httpError(w, 422, "description is too long")
		return
	}
	key := r.PathValue("key")
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var err error
	switch c.Kind {
	case "linear":
		l := s.linearClient(c)
		var d *trackers.IssueDetail
		if d, err = l.Issue(ctx, key); err == nil {
			err = l.SetDescription(ctx, firstNonEmptyStr(d.UID, key), in.Body)
		}
	case "jira":
		j := s.jiraClient(c)
		var d *trackers.IssueDetail
		if d, err = j.Issue(ctx, key); err == nil {
			if d.BodyLossy && !in.ConfirmLossy {
				httpError(w, 409, "the description has formatting the editor cannot keep (a table, panel, colour or attachment); saving replaces it — send confirm_lossy to go ahead")
				return
			}
			err = j.SetDescription(ctx, key, in.Body)
		}
	default:
		httpError(w, 400, "not a Linear or Jira connection")
		return
	}
	if err != nil {
		trackerErr(w, err)
		return
	}
	d, err := s.trackerIssueDetail(ctx, c, key)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	writeJSON(w, 200, d)
}

// forgeQueue is the merge queue (GitHub) or merge train (GitLab) of a base
// branch — the repository's default branch unless one is named.
func (s *Server) forgeQueue(w http.ResponseWriter, r *http.Request) {
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	q, ok := f.(trackers.Queuer)
	if !ok {
		writeJSON(w, 200, map[string]any{"supported": false, "kind": f.Kind(), "entries": []trackers.QueueEntry{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	base := strings.TrimSpace(r.URL.Query().Get("base"))
	if base == "" {
		b, err := q.DefaultBranch(ctx)
		if err != nil {
			trackerErr(w, err)
			return
		}
		base = b
	}
	entries, err := q.Queue(ctx, base)
	if err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"supported": true, "kind": f.Kind(), "base": base, "entries": entries})
}

// forgeDequeue takes a pull request out of the queue. The entry is looked up
// again, so only something actually queued on that branch can be removed.
func (s *Server) forgeDequeue(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "changing the merge queue") {
		return
	}
	_, f, ok := s.forgeCtx(w, r)
	if !ok {
		return
	}
	q, ok := f.(trackers.Queuer)
	if !ok {
		httpError(w, 501, "%s has no merge queue", forgeName[f.Kind()])
		return
	}
	var in struct {
		Base string `json:"base"`
		ID   string `json:"id"`
	}
	if err := decodeBody(r, &in); err != nil || in.ID == "" || in.Base == "" {
		httpError(w, 422, "base and id are required")
		return
	}
	entries, err := q.Queue(r.Context(), in.Base)
	if err != nil {
		trackerErr(w, err)
		return
	}
	for _, e := range entries {
		if e.ID == in.ID {
			if err := q.Dequeue(r.Context(), e); err != nil {
				trackerErr(w, err)
				return
			}
			writeJSON(w, 200, map[string]any{"ok": true})
			return
		}
	}
	httpError(w, 404, "that pull request is not in the %s queue", in.Base)
}
