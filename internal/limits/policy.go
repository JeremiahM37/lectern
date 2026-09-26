package limits

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// What to do when a limit is hit.
const (
	// ModeNotify pushes a notification with one-tap choices and does nothing
	// else. It is the default: typing into someone's session unasked, or
	// starting a different agent, is an opt-in.
	ModeNotify = "notify"
	// ModeWait resumes the same agent once its limit resets (a task is
	// requeued for then).
	ModeWait = "wait"
	// ModeHandoff hands the work to the configured fallback agent now.
	ModeHandoff = "handoff"
)

// Policy is one scope's limit policy, stored as JSON in limit_policies.
type Policy struct {
	Mode              string `json:"mode"`
	FallbackAgent     string `json:"fallback_agent,omitempty"`
	FallbackModel     string `json:"fallback_model,omitempty"`
	FallbackProfileID int64  `json:"fallback_profile_id,omitempty"`
}

// HasFallback reports whether a handoff has somewhere to go.
func (p Policy) HasFallback() bool { return p.FallbackAgent != "" || p.FallbackProfileID != 0 }

// Fallback names the handoff destination for a card or a push.
func (p Policy) Fallback() string {
	if !p.HasFallback() {
		return ""
	}
	name := p.FallbackAgent
	if name == "" {
		name = fmt.Sprintf("profile %d", p.FallbackProfileID)
	}
	if p.FallbackModel != "" {
		name += " · " + p.FallbackModel
	}
	return name
}

// Validate normalises the mode and rejects a handoff with nowhere to go.
func (p *Policy) Validate() error {
	p.Mode = strings.TrimSpace(strings.ToLower(p.Mode))
	p.FallbackAgent = strings.TrimSpace(p.FallbackAgent)
	p.FallbackModel = strings.TrimSpace(p.FallbackModel)
	switch p.Mode {
	case "":
		p.Mode = ModeNotify
	case ModeNotify, ModeWait:
	case ModeHandoff:
		if !p.HasFallback() {
			return fmt.Errorf("handoff needs a fallback agent or launch profile")
		}
	default:
		return fmt.Errorf("mode must be notify, wait or handoff")
	}
	return nil
}

// Scopes, narrowest last. The narrowest one with a stored policy wins.
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
	ScopeSession = "session"
)

// ParsePolicy decodes a stored policy; an empty or unreadable value is the
// default notify policy.
func ParsePolicy(raw string) (Policy, bool) {
	var p Policy
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &p) != nil || p.Validate() != nil {
		return Policy{Mode: ModeNotify}, false
	}
	return p, true
}

// Effective resolves the policy for a session (sessionID 0 for a task) in a
// project (nil for none): session, then project, then global, then notify.
// The scope that supplied it is returned so the UI can say where to change it.
func Effective(db *store.DB, sessionID int64, projectID *int64) (Policy, string) {
	if sessionID != 0 {
		if p, ok := ParsePolicy(db.LimitPolicyJSON(ScopeSession, sessionID)); ok {
			return p, ScopeSession
		}
	}
	if projectID != nil {
		if p, ok := ParsePolicy(db.LimitPolicyJSON(ScopeProject, *projectID)); ok {
			return p, ScopeProject
		}
	}
	if p, ok := ParsePolicy(db.LimitPolicyJSON(ScopeGlobal, 0)); ok {
		return p, ScopeGlobal
	}
	return Policy{Mode: ModeNotify}, "default"
}

// Timing is every delay the resume loop uses. Tests shrink it; production
// uses DefaultTiming.
type Timing struct {
	// Grace is added to a stated reset time before anything is retried:
	// provider clocks and the CLI's rounded display are not exact.
	Grace time.Duration
	// Jitter is a random extra delay on top of Grace, so a fleet of sessions
	// limited by the same account does not all resume in the same second.
	Jitter time.Duration
	// Backoff is the first retry delay when no reset time is known or a
	// resume attempt hit the limit again. It doubles per try up to MaxBackoff.
	Backoff    time.Duration
	MaxBackoff time.Duration
	// Settle is how long a nudge has to show a result before Lectern decides
	// whether the agent resumed.
	Settle time.Duration
	// MaxTries bounds automatic resume attempts for one hold.
	MaxTries int
	// Stale is how long a handoff or requeue may sit mid-flight before it is
	// treated as interrupted (a restart in the middle of one).
	Stale time.Duration
}

// DefaultTiming is the production schedule.
var DefaultTiming = Timing{
	Grace: 45 * time.Second, Jitter: 90 * time.Second,
	Backoff: 10 * time.Minute, MaxBackoff: 2 * time.Hour,
	Settle: 90 * time.Second, MaxTries: 4, Stale: 10 * time.Minute,
}

// DueAt is when a waiting hold should next be retried: just after the reset
// when one is known and still ahead, otherwise an exponential backoff from now
// (tries is how many resumes have already failed).
func (t Timing) DueAt(reset time.Time, tries int, now time.Time, rnd func() float64) time.Time {
	if rnd == nil {
		rnd = rand.Float64
	}
	jitter := time.Duration(rnd() * float64(t.Jitter))
	if !reset.IsZero() && reset.After(now) {
		return reset.Add(t.Grace + jitter)
	}
	backoff := t.Backoff
	for i := 0; i < tries && backoff < t.MaxBackoff; i++ {
		backoff *= 2
	}
	if backoff > t.MaxBackoff {
		backoff = t.MaxBackoff
	}
	if reset.IsZero() {
		return now.Add(backoff + jitter)
	}
	// The stated reset has already passed: retry soon rather than a full
	// backoff, but never in the same instant as every other session.
	if tries == 0 {
		return now.Add(jitter)
	}
	return now.Add(backoff + jitter)
}
