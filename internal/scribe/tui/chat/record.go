package chat

import (
	"encoding/json"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const (
	roleUser      = "user"
	roleAssistant = "assistant"
	roleSystem    = "system"
)

// Record is one transcript entry. It is plain data: Export returns Records
// and Restore accepts them, both through JSON, so a session can be resumed
// and re-rendered identically.
type Record struct {
	Role     string    `json:"role"`
	At       time.Time `json:"at"`
	Text     string    `json:"text,omitempty"`
	Images   int       `json:"images,omitempty"`
	Steering bool      `json:"steering,omitempty"`
	Origin   string    `json:"origin,omitempty"` // where a user message came from when not this terminal
	Prompt   string    `json:"prompt,omitempty"`

	// System rows.
	Kind     string        `json:"kind,omitempty"` // info|error|retry|interrupted|warning|switch|compact
	Category string        `json:"category,omitempty"`
	Model    string        `json:"model,omitempty"`
	Attempt  int           `json:"attempt,omitempty"`
	Max      int           `json:"max,omitempty"`
	Delay    time.Duration `json:"delay,omitempty"`
	Before   int           `json:"before,omitempty"`
	After    int           `json:"after,omitempty"`
	// TokBefore and TokAfter are the estimated history tokens around a
	// compaction, for one that kept the message count but shortened them.
	TokBefore int  `json:"tok_before,omitempty"`
	TokAfter  int  `json:"tok_after,omitempty"`
	Status    int  `json:"status,omitempty"`  // HTTP status of a provider error or retry, when it had one
	Pending   bool `json:"pending,omitempty"` // a retry notice whose attempt has not yet made progress

	// Assistant turns.
	Done        bool          `json:"done,omitempty"`
	Interrupted bool          `json:"interrupted,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
	Reasoned    bool          `json:"reasoned,omitempty"`
	Steps       int           `json:"steps,omitempty"`       // highest step number seen this turn
	Retries     int           `json:"retries,omitempty"`     // retry notices this turn needed; they collapse into the finished row
	ReasonOpen  bool          `json:"reason_open,omitempty"` // thought summary expanded by a click
	RailOpen    bool          `json:"rail_open,omitempty"`   // final rail unfolded by a click
	Segments    []*Segment    `json:"segments,omitempty"`

	cache *cached
	imgs  [][]byte // pasted images, for display only (AttachImages)
}

// Segment is one block of an assistant turn, in arrival order.
type Segment struct {
	Kind    string       `json:"kind"` // text | reasoning | tools | plan | tasks
	Text    string       `json:"text,omitempty"`
	Open    bool         `json:"open,omitempty"` // still streaming
	Toggled bool         `json:"toggled,omitempty"`
	Calls   []*Call      `json:"calls,omitempty"`
	Plan    *agent.Plan  `json:"plan,omitempty"`
	Tasks   []agent.Task `json:"tasks,omitempty"`
	DoneAt  time.Time    `json:"done_at,omitempty"` // when every task became done (task list)

	// Reasoning: the step it belongs to and the time spent streaming it.
	Step  int           `json:"step,omitempty"`
	Spent time.Duration `json:"spent,omitempty"`

	// Typewriter state for open text segments.
	shown  int
	fed    int // rune length of the text the last time the drip ran
	vel    float64
	dripAt time.Time
	// seen is the time of the last reasoning delta, for Spent.
	seen time.Time

	// Fade timing for text segments (spec 01 §2.12). advanceAll stamps both on
	// the first frame that sees the change, so they can lag the real edge by
	// one tick. Neither is saved: a restored segment is settled.
	openedAt time.Time
	closedAt time.Time
}

// Call is one tool invocation.
type Call struct {
	ID       string             `json:"id"`
	Tool     string             `json:"tool"`
	Args     json.RawMessage    `json:"args,omitempty"`
	Status   string             `json:"status"` // pending | running | done | error | denied
	Start    time.Time          `json:"start"`
	Duration time.Duration      `json:"duration,omitempty"`
	Result   *tools.Result      `json:"result,omitempty"`
	Progress []string           `json:"progress,omitempty"`
	Agents   []*agent.AgentInfo `json:"agents,omitempty"`
	Toggled  bool               `json:"toggled,omitempty"`
}

// cached is a finished record's rendered lines, valid while the key matches.
type cached struct {
	gen       int
	width     int
	verbose   bool
	reasoning bool
	style     string
	lines     []string
	hits      []hitRec
}

func (c *Call) running() bool { return c.Status == "pending" || c.Status == "running" }

func (r *Record) hasOpen() bool {
	for _, s := range r.Segments {
		if s.Open {
			return true
		}
		for _, c := range s.Calls {
			if c.running() {
				return true
			}
		}
	}
	return false
}

// hasContent reports whether the turn produced anything visible.
func (r *Record) hasContent() bool {
	for _, s := range r.Segments {
		switch s.Kind {
		case "text", "reasoning":
			if trimmed(s.Text) != "" {
				return true
			}
		case "tools":
			if len(s.Calls) > 0 {
				return true
			}
		case "plan":
			if s.Plan != nil {
				return true
			}
		case "tasks":
			if len(s.Tasks) > 0 {
				return true
			}
		}
	}
	return false
}
