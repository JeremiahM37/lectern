package api

import (
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/awareness"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// sessionPeers is GET /api/sessions/{id}/peers (docs/agent-events.md
// "Cross-agent awareness" point 5): the same peer list a hook-driven
// briefing/warning is built from, for a caller that has no hook context of
// its own (Codex today, the operator's UI, the `active_work` MCP tool).
// self_files is this session's OWN recent edits, included so the frontend
// can render the "overlaps" chip (intersect self_files against each peer's
// files) without a second round trip.
func (s *Server) sessionPeers(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "session not found")
		return
	}
	sess, err := s.DB.Session(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	if s.Awareness == nil {
		writeJSON(w, 200, map[string]any{"peers": []awareness.Peer{}, "self_files": []awareness.PeerFile{}})
		return
	}
	peers, err := s.Awareness.Peers(sess)
	if err != nil {
		respondErr(w, err)
		return
	}
	now := store.Now()
	since := now - awareness.RecentEditsWindow.Seconds()
	self, err := s.Awareness.RecentFiles(sess.ID, since, now)
	if err != nil {
		respondErr(w, err)
		return
	}
	if peers == nil {
		peers = []awareness.Peer{}
	}
	if self == nil {
		self = []awareness.PeerFile{}
	}
	writeJSON(w, 200, map[string]any{"peers": peers, "self_files": self})
}

// peersByRepoPath is GET /api/peers?common_dir=... — the fallback the
// `active_work` MCP tool uses when it has no LECTERN_SESSION_ID to key off
// (docs/agent-events.md: "or for a given repo path"). common_dir is the
// git-common-dir absolute path the MCP tool resolved locally on the
// caller's own machine (see internal/mcp/tools.go); this endpoint matches
// it against every live session/project's cached repo_key regardless of
// target id — see awareness.PeersForCommonDir's doc comment for why that is
// an acceptable simplification here.
func (s *Server) peersByRepoPath(w http.ResponseWriter, r *http.Request) {
	commonDir := r.URL.Query().Get("common_dir")
	if commonDir == "" {
		httpError(w, 400, "common_dir is required")
		return
	}
	if s.Awareness == nil {
		writeJSON(w, 200, map[string]any{"peers": []awareness.Peer{}})
		return
	}
	peers, err := s.Awareness.PeersForCommonDir(commonDir)
	if err != nil {
		respondErr(w, err)
		return
	}
	if peers == nil {
		peers = []awareness.Peer{}
	}
	writeJSON(w, 200, map[string]any{"peers": peers})
}

// awarenessDuplicatePrompts is GET /api/awareness/duplicate-prompts: pairs
// of live sessions in one repo whose recent prompts look like the same
// task (docs/agent-events.md point 6, the Needs-you "possible duplicate
// work" notice).
func (s *Server) awarenessDuplicatePrompts(w http.ResponseWriter, r *http.Request) {
	if s.Awareness == nil {
		writeJSON(w, 200, []awareness.DuplicatePair{})
		return
	}
	pairs, err := s.Awareness.DuplicatePrompts()
	if err != nil {
		respondErr(w, err)
		return
	}
	if pairs == nil {
		pairs = []awareness.DuplicatePair{}
	}
	writeJSON(w, 200, pairs)
}
