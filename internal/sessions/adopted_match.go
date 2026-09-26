package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/nativeidentity"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// An adopted session was started outside Lectern, so no conversation was ever
// bound to it. When its terminal is lost (a reboot, usually), Lectern looks
// for the one saved conversation in the same folder, for the same agent,
// that was last written when the session was last seen active. Only an
// unambiguous match is kept, as resume_guess; Restore offers it as the
// likely conversation and the history picker stays available.

const (
	// matchBefore/matchAfter bound how far the transcript's last write may be
	// from the session's last observed activity.
	matchBefore = 300.0
	matchAfter  = 120.0
)

// NativeCandidate is one saved conversation in a folder.
type NativeCandidate struct {
	ID       string  `json:"id"`
	Modified float64 `json:"modified"`
}

// MatchAdoptedConversation picks the conversation whose last write is near
// the session's last activity, or "" when there is none or more than one.
// Conversations already bound to another session are never chosen.
func MatchAdoptedConversation(candidates []NativeCandidate, lastActive, ended float64, taken map[string]bool) string {
	from, to := lastActive-matchBefore, math.Max(lastActive, ended)+matchAfter
	match := ""
	for _, c := range candidates {
		if c.ID == "" || taken[c.ID] || c.Modified < from || c.Modified > to {
			continue
		}
		if match != "" {
			return ""
		}
		match = c.ID
	}
	return match
}

// MatchLostAdopted looks for a lost adopted session's conversation and stores
// it as resume_guess. It is safe to call more than once.
func (m *Manager) MatchLostAdopted(ctx context.Context, id int64) string {
	row, ex, err := m.resolve(id)
	if err != nil || row.Origin != "discovered" || row.EndedAt == nil || row.Status != StatusDead ||
		(row.Agent != "claude" && row.Agent != "codex") || row.ResumeID != "" || row.NativeRecoveryCID != "" || !path.IsAbs(row.Workdir) {
		return ""
	}
	target, err := m.DB.Target(row.TargetID)
	if err != nil || target.Kind == "sandbox" {
		return ""
	}
	candidates, err := m.nativeCandidates(ctx, ex, row)
	if err != nil {
		return ""
	}
	taken := map[string]bool{}
	if refs, err := m.DB.ConversationRefs(); err == nil {
		for _, ref := range refs {
			if ref.ID != row.ID && ref.TargetID == row.TargetID {
				for _, cid := range ref.CIDs {
					taken[cid] = true
				}
			}
		}
	}
	last := row.CreatedAt
	if row.LastActivityAt != nil {
		last = *row.LastActivityAt
	}
	cid := MatchAdoptedConversation(candidates, last, *row.EndedAt, taken)
	if cid != "" {
		_ = m.DB.Update("sessions", row.ID, map[string]any{"resume_guess": cid})
		m.Log.Info("matched a lost adopted session to its conversation", "session", row.ID)
	}
	return cid
}

// nativeCandidates lists the saved conversations in the row's folder with the
// same read-only reader the history picker uses.
func (m *Manager) nativeCandidates(ctx context.Context, ex executor.Executor, row *store.Session) ([]NativeCandidate, error) {
	config, err := m.SessionLaunchConfiguration(row)
	if err != nil {
		return nil, err
	}
	prefix, err := EnvPrefix(config.Spec.Env)
	if err != nil {
		return nil, err
	}
	script := nativeidentity.RecordsScript + "\n" + nativeidentity.IdentityScript + "\n" + nativeidentity.ConversationsScript
	args := []string{row.Agent, row.Workdir, "", "", "", ""}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellq.Quote(a)
	}
	r, err := ex.Run(ctx, prefix+"python3 -c "+shellq.Quote(script)+" "+strings.Join(quoted, " "), executor.RunOpts{Timeout: 30})
	if err != nil {
		return nil, err
	}
	var out struct {
		Conversations []NativeCandidate `json:"conversations"`
	}
	if !r.OK() || json.Unmarshal([]byte(r.Stdout), &out) != nil {
		return nil, fmt.Errorf("could not list saved conversations")
	}
	return out.Conversations, nil
}
