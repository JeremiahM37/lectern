package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/autonomy"
)

// This is an explicitly incomplete discovery index, never admission or a
// replacement for a proposal. Original reports and selected plans stay intact.
type autoBacklogEntry struct {
	Key             string `json:"key"`
	ProjectID       int64  `json:"project_id"`
	Title           string `json:"title"`
	Score           int    `json:"score"`
	ContinueTaskID  int64  `json:"continue_task_id,omitempty"`
	RepairTaskID    int64  `json:"repair_task_id,omitempty"`
	SourceRevision  string `json:"source_revision,omitempty"`
	WhyPreview      string `json:"why_preview"`
	AmbitionPreview string `json:"ambition_preview,omitempty"`
	DetailRequired  bool   `json:"detail_required"`
	DetailsURI      string `json:"details_uri"`
}

func autoBacklogKey(p autonomy.Proposal) string {
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func autoPreview(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "… [preview; read full entry]"
}
func autoBacklogIndex(items []autonomy.Proposal) []autoBacklogEntry {
	rows := make([]autoBacklogEntry, 0, len(items))
	for _, p := range items {
		key := autoBacklogKey(p)
		rows = append(rows, autoBacklogEntry{Key: key, ProjectID: p.ProjectID, Title: p.Title, Score: p.Score, ContinueTaskID: p.ContinueTaskID, RepairTaskID: p.RepairTaskID, SourceRevision: p.SourceRevision, WhyPreview: autoPreview(p.Why, 240), AmbitionPreview: autoPreview(p.Ambition, 160), DetailRequired: true, DetailsURI: "/backlog?key=" + key})
	}
	return rows
}
func autoBacklogDetail(a *autoRecord, key string) (autonomy.Proposal, bool) {
	if len(key) != 64 || strings.ToLower(key) != key {
		return autonomy.Proposal{}, false
	}
	if _, err := hex.DecodeString(key); err != nil {
		return autonomy.Proposal{}, false
	}
	states := append([]*autonomy.State{a.State}, a.DeferredRuns...)
	states = append(states, a.Runs...)
	for _, state := range states {
		if state != nil {
			for _, p := range state.Backlog {
				if autoBacklogKey(p) == key {
					return p, true
				}
			}
		}
	}
	return autonomy.Proposal{}, false
}
