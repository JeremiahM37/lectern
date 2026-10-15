// Package decisionlog records every decision a person, a policy or a witness
// check made about an agent's work, and how long the thing waited for it
// (docs/decision-log.md). Lectern already receives the hooks for all of these
// (agent events, approvals, Review actions); this package only writes them
// down, so the history exists to be studied. It adds no service and no UI:
// rows stay in Lectern's own database and are read through /api/decisions.
//
// What is recorded is structure, never content: a decision's kind, its value,
// how it arrived, timings, and small counts. No prompt, command or message
// text is stored, only lengths, tool names and numbers.
package decisionlog

import (
	"encoding/json"
	"os"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The values of decisions.kind.
const (
	KindApproval           = "approval"
	KindTurnResponse       = "turn_response"
	KindInterrupt          = "interrupt"
	KindPermissionPrompt   = "permission_prompt"
	KindReviewCommit       = "review_commit"
	KindReviewPush         = "review_push"
	KindReviewCommentsSent = "review_comments_sent"
	KindReviewDiscard      = "review_discard"
	KindTaskReview         = "task_review"
)

// The values of decisions.via.
const (
	ViaWeb      = "web"
	ViaPhone    = "phone"
	ViaTerminal = "terminal"
	ViaPolicy   = "policy"
	ViaWitness  = "witness"
	ViaInferred = "inferred"
	ViaAPI      = "api"
	ViaSystem   = "system"
)

// EnvVar is the off switch. Anything but 0, false or off leaves recording on.
const EnvVar = "LECTERN_DECISION_LOG"

// Enabled reports whether decisions are recorded. It is read on every call, so
// a test can flip the switch with t.Setenv.
func Enabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvVar))) {
	case "0", "false", "off":
		return false
	}
	return true
}

// ViaFor maps an approval's decided_by to a decision's via. The broker does
// not know whether a web click or a phone tap sent a "user" decision, so both
// land as web; the phone case is documented in docs/decision-log.md.
func ViaFor(decidedBy string) string {
	switch decidedBy {
	case "user":
		return ViaWeb
	case "policy":
		return ViaPolicy
	case "witness":
		return ViaWitness
	case "system":
		return ViaSystem
	case "":
		return ViaAPI
	}
	return ViaAPI
}

// Record writes one decision. It fills in the live session and pending counts
// at this moment, because those are what the decision was made under. A
// disabled log writes nothing and returns nil.
func Record(db *store.DB, d store.Decision) error {
	if !Enabled() || db == nil {
		return nil
	}
	d.ActiveSessions, d.Pending = counts(db)
	_, err := db.InsertDecision(&d)
	return err
}

// counts is how many sessions are live and approvals pending right now. A
// read error reads as zero rather than failing the decision it describes.
func counts(db *store.DB) (active, pending int) {
	if live, err := db.LiveSessions(); err == nil {
		active = len(live)
	}
	if pend, err := db.ApprovalsByStatus("pending"); err == nil {
		pending = len(pend)
	}
	return active, pending
}

// RecordApproval records a broker decision that has been applied: the row is
// the approval as it now stands, with its final status. It is called from
// the broker's OnDecided callback, so the broker never imports this package.
func RecordApproval(db *store.DB, row *store.Approval) error {
	if row == nil || row.DecidedAt == nil {
		return nil
	}
	created := row.CreatedAt
	decided := *row.DecidedAt
	latency := decided - created
	d := store.Decision{
		At:          decided,
		Kind:        KindApproval,
		Decision:    row.Status,
		Via:         ViaFor(row.DecidedBy),
		WaitedSince: &created,
		LatencyS:    &latency,
		Detail:      detailJSON(map[string]any{"tool": row.ToolName}),
	}
	if row.AttemptID != 0 {
		aid := row.AttemptID
		d.AttemptID = &aid
		if proj, err := db.ProjectForAttempt(row.AttemptID); err == nil && proj != nil {
			pid := proj.ID
			d.ProjectID = &pid
		}
	}
	if row.SessionID != 0 {
		sid := row.SessionID
		d.SessionID = &sid
		if sess, err := db.Session(row.SessionID); err == nil && sess != nil {
			d.Agent = sess.Agent
		}
	}
	return Record(db, d)
}

// detailJSON renders a detail map. It only ever receives lengths, names and
// counts; the package never puts text in here.
func detailJSON(m map[string]any) json.RawMessage {
	b, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
