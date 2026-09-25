// Package autonomy contains the deterministic daily experiment policy. It does
// not launch agents, change servers, or grant permissions. Callers persist State
// before dispatching and enforce QuotaGate before and throughout every run.
package autonomy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
)

type Config struct {
	Enabled           bool    `json:"enabled"`
	Continuous        bool    `json:"continuous"`
	Timezone          string  `json:"timezone"`
	MorningHour       int     `json:"morning_hour"`
	ReservePercent    float64 `json:"reserve_percent"`
	MarginPercent     float64 `json:"margin_percent"`
	MaxRevisionRounds int     `json:"max_revision_rounds"`
	MaxItemsPerDay    int     `json:"max_items_per_day"`
}

func DefaultConfig() Config {
	return Config{Continuous: true, Timezone: "America/Denver", MorningHour: 8, ReservePercent: 10, MarginPercent: 5, MaxRevisionRounds: 2, MaxItemsPerDay: 3}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Timezone) == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if c.MorningHour < 0 || c.MorningHour > 23 {
		return errors.New("morning_hour must be 0–23")
	}
	if !finite(c.ReservePercent) || !finite(c.MarginPercent) || c.ReservePercent < 10 || c.MarginPercent < 5 || c.ReservePercent+c.MarginPercent >= 100 {
		return errors.New("reserve must be at least 10%, margin at least 5%, sum below 100%")
	}
	if c.MaxRevisionRounds < 0 || c.MaxRevisionRounds > 2 || c.MaxItemsPerDay < 1 || c.MaxItemsPerDay > 3 {
		return errors.New("at most two revision rounds and one to three items per cycle are allowed")
	}
	return nil
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// Usage matches homelab-api /api/agent-usage's normalized response. Pointers
// distinguish absent measurements from legitimate zeroes; all advertised
// windows, including model-specific ones, constrain dispatch.
type Usage struct {
	Providers      []ProviderUsage `json:"providers"`
	RefreshSeconds int             `json:"refresh_seconds"`
}
type ProviderUsage struct {
	ID        string        `json:"id"`
	Status    string        `json:"status"`
	UpdatedAt *float64      `json:"updated_at"`
	Buckets   []UsageBucket `json:"buckets"`
}
type UsageBucket struct {
	ID      string        `json:"id"`
	Windows []UsageWindow `json:"windows"`
}
type UsageWindow struct {
	Label            string   `json:"label"`
	UsedPercent      *float64 `json:"used_percent"`
	RemainingPercent *float64 `json:"remaining_percent"`
	ResetsAt         *float64 `json:"resets_at"`
	ResetPassed      bool     `json:"reset_passed"`
}

// QuotaGate fails closed for missing, duplicate, invalid, stale, or reset-past
// data. The five-minute source cache gets only a one-minute delivery allowance.
// This threshold is headroom, not a guarantee against provider reporting lag.
func QuotaGate(c Config, providers []ProviderUsage, required []string, now time.Time) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Enabled {
		return errors.New("autonomous mode is off")
	}
	if len(required) == 0 {
		return errors.New("no required quota providers")
	}
	seen := map[string]bool{}
	for _, id := range required {
		if id == "" || seen[id] {
			return errors.New("required provider IDs must be unique and nonempty")
		}
		seen[id] = true
		var found *ProviderUsage
		for i := range providers {
			if providers[i].ID == id {
				if found != nil {
					return fmt.Errorf("duplicate usage for %s", id)
				}
				found = &providers[i]
			}
		}
		if found == nil || found.Status != "ok" || found.UpdatedAt == nil || !finite(*found.UpdatedAt) {
			return fmt.Errorf("%s usage unavailable", id)
		}
		age := float64(now.UnixNano())/1e9 - *found.UpdatedAt
		if age < 0 || age > 360 {
			return fmt.Errorf("%s usage stale or timestamp invalid", id)
		}
		if len(found.Buckets) == 0 {
			return fmt.Errorf("%s has no quota buckets", id)
		}
		var weekly, session bool
		for _, b := range found.Buckets {
			if len(b.Windows) == 0 {
				return fmt.Errorf("%s has no quota windows", id)
			}
			for _, w := range b.Windows {
				label := strings.ToLower(strings.TrimSpace(w.Label))
				weekly = weekly || strings.Contains(label, "weekly")
				session = session || strings.Contains(label, "session") || strings.Contains(label, "5-hour")
				if w.UsedPercent == nil || w.RemainingPercent == nil || w.ResetsAt == nil || !finite(*w.UsedPercent) || !finite(*w.RemainingPercent) || !finite(*w.ResetsAt) || *w.UsedPercent < 0 || *w.UsedPercent > 100 || *w.RemainingPercent < 0 || *w.RemainingPercent > 100 || math.Abs(*w.UsedPercent+*w.RemainingPercent-100) > 0.2 {
					return fmt.Errorf("%s %s quota invalid", id, w.Label)
				}
				if w.ResetPassed || *w.ResetsAt <= float64(now.UnixNano())/1e9 {
					return fmt.Errorf("%s %s reset requires fresh usage", id, w.Label)
				}
				if *w.RemainingPercent <= c.ReservePercent+c.MarginPercent {
					return fmt.Errorf("%s %s reserve reached", id, w.Label)
				}
			}
		}
		// Codex legitimately reports weekly-only subscriptions (primary=weekly,
		// secondary=null). Claude always supplies the five-hour window as well.
		if !weekly || (id == "claude" && !session) {
			return fmt.Errorf("%s lacks required quota window coverage", id)
		}
	}
	return nil
}

type Phase string

const (
	Plan          Phase = "plan"
	Audit         Phase = "audit"
	Revise        Phase = "revise"
	Build         Phase = "build"
	DecisionAudit Phase = "decision_audit"
	Review        Phase = "review"
	Complete      Phase = "complete"
	Paused        Phase = "paused"
)

type Proposal struct {
	ProjectID      int64    `json:"project_id"`
	RepairTaskID   int64    `json:"repair_task_id,omitempty"`
	ContinueTaskID int64    `json:"continue_task_id,omitempty"`
	Title          string   `json:"title"`
	Why            string   `json:"why"`
	Acceptance     []string `json:"acceptance"`
	Ambition       string   `json:"ambition,omitempty"`
	Novelty        string   `json:"novelty,omitempty"`
	Score          int      `json:"score,omitempty"`
	Expert         bool     `json:"expert,omitempty"`
}
type PlanReport struct {
	Items   []Proposal `json:"items"`
	Backlog []Proposal `json:"backlog,omitempty"`
}
type Verdict struct {
	Approve *bool  `json:"approve"`
	Reason  string `json:"reason"`
}
type BuildReport struct {
	Summary  string            `json:"summary"`
	Evidence []string          `json:"evidence"`
	Decision *DecisionProposal `json:"decision,omitempty"`
}
type DecisionProposal struct {
	Title        string   `json:"title"`
	Rationale    string   `json:"rationale"`
	Alternatives []string `json:"alternatives"`
	Risks        []string `json:"risks"`
}

func validateDecision(d DecisionProposal) error {
	if strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Rationale) == "" || len(d.Alternatives) == 0 || len(d.Risks) == 0 {
		return errors.New("decision needs title, rationale, alternatives, and risks")
	}
	for _, option := range d.Alternatives {
		if strings.TrimSpace(option) == "" {
			return errors.New("decision has an empty alternative")
		}
	}
	for _, risk := range d.Risks {
		if strings.TrimSpace(risk) == "" {
			return errors.New("decision has an empty risk")
		}
	}
	return nil
}

type Assignment struct {
	TaskID    int64  `json:"task_id"`
	Role      string `json:"role"`
	Round     int    `json:"round"`
	Item      int    `json:"item"`
	Step      int    `json:"step"`
	Completed bool   `json:"completed"`
}
type State struct {
	Date           string                    `json:"date"`
	Phase          Phase                     `json:"phase"`
	ResumePhase    Phase                     `json:"resume_phase,omitempty"`
	Reason         string                    `json:"reason,omitempty"`
	Revision       int                       `json:"revision"`
	Cycle          int                       `json:"cycle"`
	Item           int                       `json:"item"`
	Step           int                       `json:"step"`
	Items          []Proposal                `json:"items"`
	Backlog        []Proposal                `json:"backlog,omitempty"`
	Decision       *DecisionProposal         `json:"decision,omitempty"`
	DecisionAudits map[string]Verdict        `json:"decision_audits,omitempty"`
	Assignments    []Assignment              `json:"assignments"`
	Audits         map[string]Verdict        `json:"audits"`
	Reports        map[int64]json.RawMessage `json:"reports"`
}

func NewState(date string) (*State, error) {
	if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, err
	}
	return &State{Date: date, Phase: Plan, Audits: map[string]Verdict{}, Reports: map[int64]json.RawMessage{}}, nil
}

// NeededRoles is a stable dispatch plan. RegisterTask must be persisted before
// dispatch; a restart then sees the same outstanding owned task IDs.
func (s *State) NeededRoles() []string {
	var roles []string
	switch s.Phase {
	case Plan, Revise:
		roles = []string{"planner"}
	case Audit:
		roles = []string{"auditor_a", "auditor_b"}
	case Build:
		roles = []string{"builder"}
	case DecisionAudit:
		roles = []string{"decision_a", "decision_b"}
	case Review:
		roles = []string{"reviewer"}
	}
	out := []string{}
	for _, role := range roles {
		exists := false
		for _, a := range s.Assignments {
			if a.Role == role && a.Round == s.Revision && a.Item == s.Item && a.Step == s.Step {
				exists = true
			}
		}
		if !exists {
			out = append(out, role)
		}
	}
	return out
}

func (s *State) RegisterTask(role string, id int64) error {
	if id <= 0 {
		return errors.New("task ID must be positive")
	}
	for _, a := range s.Assignments {
		if a.TaskID == id {
			return errors.New("task ID already owned")
		}
	}
	for _, r := range s.NeededRoles() {
		if r == role {
			s.Assignments = append(s.Assignments, Assignment{TaskID: id, Role: role, Round: s.Revision, Item: s.Item, Step: s.Step})
			return nil
		}
	}
	return errors.New("role not needed in current phase")
}

func decodeStrict(raw []byte, v any) error {
	// encoding/json otherwise silently accepts repeated keys using the last
	// value, which makes an audit verdict ambiguous across different readers.
	if err := uniqueJSONKeys(json.NewDecoder(bytes.NewReader(raw))); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("report must be one JSON object")
	}
	return nil
}

func uniqueJSONKeys(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, container := t.(json.Delim)
	if !container {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[name] = true
			if err := uniqueJSONKeys(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSONKeys(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}

// ApplyReport accepts only the expected, owned task and validates the entire
// report before changing state. Repeated delivery is rejected without mutation.
func (s *State) ApplyReport(c Config, id int64, raw []byte) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !c.Enabled {
		return errors.New("autonomous mode is off")
	}
	if s.Phase == Paused || s.Phase == Complete {
		return errors.New("run is not accepting reports")
	}
	idx := -1
	for i, a := range s.Assignments {
		if a.TaskID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New("unowned task")
	}
	a := s.Assignments[idx]
	if a.Completed || a.Round != s.Revision || a.Item != s.Item || a.Step != s.Step {
		return errors.New("duplicate or obsolete report")
	}
	switch a.Role {
	case "planner":
		if s.Phase != Plan && s.Phase != Revise {
			return errors.New("unexpected planner report")
		}
		var r PlanReport
		if err := decodeStrict(raw, &r); err != nil {
			return err
		}
		if r.Items == nil || len(r.Items) > c.MaxItemsPerDay || len(r.Backlog) > 12 {
			return errors.New("plan needs items array within cycle cap and at most 12 backlog proposals")
		}
		seen := map[string]bool{}
		for index, p := range append(append([]Proposal(nil), r.Items...), r.Backlog...) {
			// A selected milestone may also appear in the persistent backlog.
			if index == len(r.Items) {
				seen = map[string]bool{}
			}
			key := fmt.Sprintf("%d:%s", p.ProjectID, strings.ToLower(strings.TrimSpace(p.Title)))
			if p.ProjectID <= 0 || p.ContinueTaskID < 0 || p.RepairTaskID < 0 || (p.ContinueTaskID > 0 && p.RepairTaskID > 0) || p.Score < 0 || p.Score > 100 || strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Why) == "" || seen[key] {
				return fmt.Errorf("proposal %d needs positive project_id, title, why, score 0..100, nonnegative mutually exclusive continue_task_id/repair_task_id and a unique title within its list", index)
			}
			if index < len(r.Items) && len(p.Acceptance) == 0 {
				return fmt.Errorf("items[%d].acceptance needs at least one concrete criterion", index)
			}
			for _, v := range p.Acceptance {
				if strings.TrimSpace(v) == "" {
					return errors.New("empty acceptance criterion")
				}
			}
			seen[key] = true
		}
		s.Items = r.Items
		s.Backlog = r.Backlog
		s.Audits = map[string]Verdict{}
		s.Phase = Audit
		if len(r.Items) == 0 {
			s.Phase = Complete
			s.Reason = "No worthwhile work proposed"
		}
	case "auditor_a", "auditor_b":
		if s.Phase != Audit {
			return errors.New("unexpected audit report")
		}
		var v Verdict
		if err := decodeStrict(raw, &v); err != nil {
			return err
		}
		if v.Approve == nil || strings.TrimSpace(v.Reason) == "" {
			return errors.New("audit needs explicit approval and reason")
		}
		if s.Audits == nil {
			s.Audits = map[string]Verdict{}
		}
		s.Audits[a.Role] = v
		if len(s.Audits) == 2 {
			if *s.Audits["auditor_a"].Approve && *s.Audits["auditor_b"].Approve {
				s.Phase = Build
			} else if s.Revision < c.MaxRevisionRounds {
				s.Revision++
				s.Phase = Revise
			} else {
				s.Phase = Complete
				s.Reason = "Audit agreement not reached within revision limit"
			}
		}
	case "builder":
		if s.Phase != Build {
			return errors.New("unexpected build report")
		}
		var r BuildReport
		if err := decodeStrict(raw, &r); err != nil {
			return err
		}
		if strings.TrimSpace(r.Summary) == "" || len(r.Evidence) == 0 {
			return errors.New("build report requires summary and evidence")
		}
		for _, v := range r.Evidence {
			if strings.TrimSpace(v) == "" {
				return errors.New("empty evidence")
			}
		}
		if r.Decision != nil {
			if err := validateDecision(*r.Decision); err != nil {
				return err
			}
			if s.Step >= 4 {
				s.Phase = Complete
				s.Reason = "Decision audit bounded after four rounds"
			} else {
				s.Decision = r.Decision
				s.DecisionAudits = map[string]Verdict{}
				s.Phase = DecisionAudit
			}
		} else {
			s.Phase = Review
		}
	case "decision_a", "decision_b":
		if s.Phase != DecisionAudit || s.Decision == nil {
			return errors.New("unexpected decision audit report")
		}
		var v Verdict
		if err := decodeStrict(raw, &v); err != nil {
			return err
		}
		if v.Approve == nil || strings.TrimSpace(v.Reason) == "" {
			return errors.New("decision audit needs explicit approval and reason")
		}
		if s.DecisionAudits == nil {
			s.DecisionAudits = map[string]Verdict{}
		}
		s.DecisionAudits[a.Role] = v
		if len(s.DecisionAudits) == 2 {
			s.Step++
			if !*s.DecisionAudits["decision_a"].Approve || !*s.DecisionAudits["decision_b"].Approve {
				if s.Step >= 4 {
					s.Phase = Complete
					s.Reason = "Decision audit agreement not reached within four rounds"
				} else {
					s.Phase = Build
				}
			} else {
				s.Phase = Build
			}
		}
	case "reviewer":
		if s.Phase != Review {
			return errors.New("unexpected review report")
		}
		var v Verdict
		if err := decodeStrict(raw, &v); err != nil {
			return err
		}
		if v.Approve == nil || strings.TrimSpace(v.Reason) == "" {
			return errors.New("review needs approval and reason")
		}
		if !*v.Approve {
			s.Phase = Complete
			s.Reason = "Build review rejected: " + v.Reason
		} else {
			s.Item++
			s.Step = 0
			s.Decision = nil
			s.DecisionAudits = nil
			if s.Item >= len(s.Items) {
				s.Phase = Complete
			} else {
				s.Phase = Build
			}
		}
	default:
		return errors.New("unknown task role")
	}
	s.Assignments[idx].Completed = true
	if s.Reports == nil {
		s.Reports = map[int64]json.RawMessage{}
	}
	s.Reports[id] = append(json.RawMessage(nil), raw...)
	return nil
}

func (s *State) Pause(reason string) {
	if s.Phase != Paused && s.Phase != Complete {
		s.ResumePhase = s.Phase
		s.Phase = Paused
		s.Reason = reason
	}
}
func (s *State) Resume(c Config, providers []ProviderUsage, required []string, now time.Time) error {
	if s.Phase != Paused {
		return errors.New("run is not paused")
	}
	if err := QuotaGate(c, providers, required, now); err != nil {
		return err
	}
	switch s.ResumePhase {
	case Plan, Audit, Revise, Build, DecisionAudit, Review:
		s.Phase = s.ResumePhase
		s.ResumePhase = ""
		s.Reason = ""
		return nil
	}
	return errors.New("invalid resume phase")
}

func (s *State) ActiveTaskIDs() []int64 {
	ids := []int64{}
	for _, a := range s.Assignments {
		if !a.Completed {
			ids = append(ids, a.TaskID)
		}
	}
	return ids
}
