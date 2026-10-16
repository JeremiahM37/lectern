// Package chat renders Scribe's chat transcript: user and assistant messages,
// system rows, streamed text, reasoning, tool calls and sub-agents. It
// consumes the agent Event stream and exposes a scrollable View that is always
// exactly w x h cells.
package chat

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Options configures a View.
type Options struct {
	Theme         theme.Theme
	Verbose       bool   // original order, every tool row, no final-response rail
	ShowReasoning bool   // reasoning segments are kept and rendered
	ChatStyle     string // "accent" (default) or "bubble"
	DiffStyle     string // DiffDefault, DiffSideBySide or DiffCompact
	CollapseDiffs bool   // finished edits show one line until expanded
	Workdir       string // paths under it are shown relative
	Now           func() time.Time
}

// View is the chat transcript. It is not safe for concurrent use; the
// bubbletea model owns it.
type View struct {
	opt Options
	th  theme.Theme
	w   int
	h   int

	msgs       []*Record
	live       *Record // assistant record receiving the current turn
	busy       bool
	compacting *Record
	errShown   bool
	calls      map[string]*Call
	agents     map[string]*agent.AgentInfo
	// localPrompts are prompts AddUser added whose turn has not started yet.
	localPrompts []string

	now        time.Time // last Tick time; zero means use the clock
	wheelAt    time.Time // last wheel tick, for acceleration (scroll.go)
	wheelScore float64   // burst score of recent wheel ticks
	expanded   bool      // Ctrl+O: tool details and reasoning
	gen        int       // bumped whenever the global expansion changes

	// Scrolling. top is the first visible line; sticky follows the bottom.
	top       int
	sticky    bool
	detachAt  int // line count when the user last scrolled away from the bottom
	lastTotal int
	recStart  []int
	lines     []string
	hits      map[int]hitTarget

	// Checkpoint rail (rail.go). railAt is the absolute line of each marker's
	// user turn, -1 when the turn is not in the transcript.
	cps        []Checkpoint
	railAt     []int
	railUser   int // user turns in the transcript
	railViewed int // checkpoint being browsed, -1 = live
	railSel    int // rail cursor
	railFocus  bool
}

type hitTarget struct {
	rec     *Record
	call    *Call
	seg     *Segment
	summary bool
	rail    bool
}

// New returns an empty transcript.
func New(o Options) *View {
	if o.Theme.Name == "" {
		o.Theme = theme.Default()
	}
	if o.ChatStyle != "bubble" {
		o.ChatStyle = "accent"
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &View{
		opt:        o,
		th:         o.Theme,
		sticky:     true,
		calls:      map[string]*Call{},
		agents:     map[string]*agent.AgentInfo{},
		hits:       map[int]hitTarget{},
		railViewed: -1,
	}
}

// SetSize sets the cell size of the view.
func (v *View) SetSize(w, h int) {
	if w < 1 {
		w = 1
	}
	if h < 0 {
		h = 0
	}
	v.w, v.h = w, h
}

func (v *View) clock() time.Time {
	if !v.now.IsZero() {
		return v.now
	}
	return v.opt.Now()
}

func (v *View) when(t time.Time) time.Time {
	if t.IsZero() {
		return v.clock()
	}
	return t
}

// Tick advances spinners and the text drip to t. The app calls it at about
// 30 fps while Busy.
func (v *View) Tick(t time.Time) {
	v.now = t
	v.advanceAll(t)
}

// Busy reports whether a turn (or a context compaction) is in progress.
func (v *View) Busy() bool { return v.busy || v.compacting != nil }

// ToggleExpand is Ctrl+O: it flips tool details and reasoning together. If
// anything is expanded, everything collapses; otherwise everything expands.
func (v *View) ToggleExpand() {
	v.expanded = !v.expanded
	v.gen++
	for _, r := range v.msgs {
		r.ReasonOpen = false
		for _, s := range r.Segments {
			s.Toggled = false
			for _, c := range s.Calls {
				c.Toggled = false
			}
		}
		r.cache = nil
	}
}

// AddUser appends a user message. A steering message closes the assistant
// message being streamed; later events start a new one below it.
//
// The text a remote surface sends carries its marker (see originMarkers);
// the marker is removed and kept as Origin.
func (v *View) AddUser(text string, images int, at time.Time, steering bool) {
	v.closeLive()
	body, origin := splitOrigin(text)
	v.addRecord(&Record{Role: roleUser, At: at, Text: stripImageTags(body), Origin: origin, Images: images, Steering: steering})
	if !steering {
		// The turn that this prompt starts will echo it back as EvTurnStart;
		// remember it so that echo is not shown a second time.
		v.localPrompts = append(v.localPrompts, promptKey(text))
		v.Bottom()
	}
}

// originMarkers are the prefixes a message gets when it does not come from
// this terminal. The Telegram surface prefixes the prompts it forwards.
var originMarkers = []struct{ prefix, origin string }{
	{"[via telegram] ", "phone"},
}

// splitOrigin removes a remote marker from text and names where it came from.
// Local text is returned unchanged with origin "".
func splitOrigin(text string) (body, origin string) {
	for _, m := range originMarkers {
		if strings.HasPrefix(text, m.prefix) {
			return strings.TrimPrefix(text, m.prefix), m.origin
		}
	}
	return text, ""
}

// promptKey is the form a prompt is compared in, with and without its origin.
func promptKey(text string) string {
	body, _ := splitOrigin(text)
	return stripImageTags(body)
}

// takeLocal reports whether prompt is one this view added itself, and forgets
// that entry. A prompt is matched in the order it was added.
func (v *View) takeLocal(prompt string) bool {
	key := promptKey(prompt)
	for i, p := range v.localPrompts {
		if p == key {
			v.localPrompts = append(v.localPrompts[:i], v.localPrompts[i+1:]...)
			return true
		}
	}
	return false
}

var imageTag = regexp.MustCompile(`\[image-\d+\]`)

func stripImageTags(s string) string { return strings.TrimSpace(imageTag.ReplaceAllString(s, "")) }

// AddSystem appends a system row. kind is info, error, retry or interrupted.
func (v *View) AddSystem(kind, text string) {
	switch kind {
	case "error":
		v.addRecord(&Record{Role: roleSystem, Kind: "error", Category: "Error", Text: text})
	case "retry":
		v.addRecord(&Record{Role: roleSystem, Kind: "retry", Text: text})
	case "interrupted":
		v.addRecord(&Record{Role: roleSystem, Kind: "interrupted"})
	default:
		v.addRecord(&Record{Role: roleSystem, Kind: "info", Text: text})
	}
}

// Apply consumes one agent event. Events from subagents (AgentID set) update
// their dispatch row; their own text is not shown.
func (v *View) Apply(e agent.Event) {
	if e.AgentID != "" {
		v.applySub(e)
		return
	}
	switch e.Kind {
	case agent.EvTurnStart:
		v.closeLive()
		if e.Prompt != "" && !v.takeLocal(e.Prompt) {
			// A turn this view did not start: a phone message, or any other
			// prompt the backend ran. It has no row yet, so it gets one here.
			v.AddUser(e.Prompt, 0, v.when(e.At), false)
		}
		v.ensureLive(e.At).Prompt = e.Prompt
		v.errShown = false
	case agent.EvStepStart:
		rec := v.ensureLive(e.At)
		if e.Step > rec.Steps {
			rec.Steps = e.Step
		} else if e.Step == 0 {
			rec.Steps++
		}
	case agent.EvText:
		v.openSeg("text", e.At).Text += e.Text
	case agent.EvReasoning:
		rec := v.ensureLive(e.At)
		rec.Reasoned = true
		if v.opt.ShowReasoning {
			at := v.when(e.At)
			s := v.openSeg("reasoning", at)
			if s.Step == 0 {
				s.Step = maxInt(rec.Steps, 1)
			}
			if !s.seen.IsZero() {
				s.Spent += at.Sub(s.seen)
			}
			s.seen = at
			s.Text += e.Text
		}
	case agent.EvToolStart:
		v.callFor(e.CallID, e.Tool, e.At)
	case agent.EvToolRun:
		c := v.callFor(e.CallID, e.Tool, e.At)
		if e.Tool != "" {
			c.Tool = e.Tool
		}
		c.Args = e.Args
		c.Status = "running"
		c.Start = v.when(e.At)
		if c.Tool == "dispatch" {
			seedAgents(c)
		}
	case agent.EvToolProgress:
		c := v.callFor(e.CallID, "", e.At)
		c.Progress = appendLines(c.Progress, e.Text)
	case agent.EvToolResult:
		c := v.callFor(e.CallID, e.Tool, e.At)
		c.Result = e.Result
		c.Duration = e.Duration
		c.Status = resultStatus(e.Result)
		if c.Tool == "dispatch" {
			dropSeeds(c)
		}
	case agent.EvSubagent:
		if e.Agent != nil {
			v.upsertAgent(e)
		}
	case agent.EvPlan:
		if e.Plan != nil {
			rec := v.ensureLive(e.At)
			s := lastSeg(rec, "plan")
			if s == nil {
				closeSegs(rec)
				s = &Segment{Kind: "plan"}
				rec.Segments = append(rec.Segments, s)
			}
			s.Plan = e.Plan
		}
	case agent.EvTasks:
		rec := v.ensureLive(e.At)
		s := lastSeg(rec, "tasks")
		if s == nil {
			closeSegs(rec)
			s = &Segment{Kind: "tasks"}
			rec.Segments = append(rec.Segments, s)
		}
		s.Tasks = append([]agent.Task(nil), e.Tasks...)
		if tasksDone(s.Tasks) {
			if s.DoneAt.IsZero() {
				s.DoneAt = v.when(e.At)
			}
		} else {
			s.DoneAt = time.Time{}
		}
	case agent.EvRetry:
		v.addRecord(&Record{Role: roleSystem, Kind: "retry", Category: string(e.Category), Attempt: e.Attempt,
			Max: e.MaxAttempts, Delay: e.Delay, Text: e.Text, At: e.At})
	case agent.EvFallback:
		v.addRecord(&Record{Role: roleSystem, Kind: "switch", Model: e.Model, Text: e.Text, At: e.At})
	case agent.EvWarning:
		v.addRecord(&Record{Role: roleSystem, Kind: "warning", Text: e.Text, At: e.At})
	case agent.EvError:
		cat := string(e.Category)
		if cat == "" {
			cat = "Error"
		}
		v.addRecord(&Record{Role: roleSystem, Kind: "error", Category: cat, Text: e.Text, At: e.At})
		v.errShown = true
	case agent.EvCompactStart:
		r := &Record{Role: roleSystem, Kind: "compact", Text: e.Text, At: e.At}
		v.addRecord(r)
		v.compacting = r
	case agent.EvCompactDone:
		r := v.compacting
		if r == nil {
			r = &Record{Role: roleSystem, Kind: "compact", At: e.At}
			v.addRecord(r)
		}
		v.compacting = nil
		r.Done = true
		r.Before = metaInt(e.Meta, "messages_before")
		r.After = metaInt(e.Meta, "messages_after")
	case agent.EvTurnDone:
		v.finishTurn(e)
	}
}

func (v *View) applySub(e agent.Event) {
	a := v.agents[e.AgentID]
	if a == nil {
		return
	}
	switch e.Kind {
	case agent.EvToolRun:
		a.Last = strings.TrimSpace(e.Tool + " " + genericArg(argsOf(e.Args)))
	case agent.EvStepStart:
		a.Step = e.Step
	case agent.EvStepFinish:
		a.Tokens.Add(e.Usage)
	}
}

func (v *View) upsertAgent(e agent.Event) {
	a := e.Agent
	c := v.calls[e.CallID]
	if c == nil {
		c = v.callFor(e.CallID, "dispatch", e.At)
	}
	if c.Tool == "" {
		c.Tool = "dispatch"
	}
	if p := v.agents[a.ID]; p != nil {
		last := p.Last
		*p = *a
		if a.Last == "" {
			p.Last = last
		}
		return
	}
	p := new(agent.AgentInfo)
	*p = *a
	// A real agent takes the place of the pending row seeded for its task.
	if i := matchSeed(c.Agents, a.Task); i >= 0 {
		c.Agents[i] = p
	} else {
		c.Agents = append(c.Agents, p)
	}
	v.agents[a.ID] = p
}

func (v *View) ensureLive(at time.Time) *Record {
	if v.live == nil {
		v.live = &Record{Role: roleAssistant, At: v.when(at)}
		v.msgs = append(v.msgs, v.live)
	}
	v.busy = true
	return v.live
}

func (v *View) addRecord(r *Record) {
	r.At = v.when(r.At)
	v.msgs = append(v.msgs, r)
}

func (v *View) closeLive() {
	if v.live != nil {
		closeSegs(v.live)
		v.live = nil
	}
}

// openSeg returns the open segment of kind, starting one if needed.
func (v *View) openSeg(kind string, at time.Time) *Segment {
	rec := v.ensureLive(at)
	if n := len(rec.Segments); n > 0 {
		if last := rec.Segments[n-1]; last.Kind == kind && last.Open {
			return last
		}
	}
	closeSegs(rec)
	s := &Segment{Kind: kind, Open: true}
	rec.Segments = append(rec.Segments, s)
	return s
}

// callFor returns the call with id, creating it in the current tools segment.
func (v *View) callFor(id, tool string, at time.Time) *Call {
	if c := v.calls[id]; c != nil {
		if c.Tool == "" {
			c.Tool = tool
		}
		return c
	}
	rec := v.ensureLive(at)
	var seg *Segment
	if n := len(rec.Segments); n > 0 && rec.Segments[n-1].Kind == "tools" {
		seg = rec.Segments[n-1]
	} else {
		closeSegs(rec)
		seg = &Segment{Kind: "tools"}
		rec.Segments = append(rec.Segments, seg)
	}
	c := &Call{ID: id, Tool: tool, Status: "pending", Start: v.when(at)}
	seg.Calls = append(seg.Calls, c)
	v.calls[id] = c
	return c
}

func (v *View) finishTurn(e agent.Event) {
	rec := v.ensureLive(e.At)
	st := e.Stats
	aborted := st != nil && st.Aborted
	if st != nil && st.Error != "" && !v.errShown && !aborted {
		v.addRecord(&Record{Role: roleSystem, Kind: "error", Category: "Error", Text: st.Error, At: e.At})
	}
	if aborted {
		v.addRecord(&Record{Role: roleSystem, Kind: "interrupted", At: e.At})
	}
	closeSegs(rec)
	for _, s := range rec.Segments {
		for _, c := range s.Calls {
			if c.running() {
				if aborted {
					c.Status = "denied"
					c.Result = &tools.Result{Output: "cancelled", IsError: true}
				} else {
					c.Status = "done"
				}
			}
		}
	}
	if e.Text != "" && !hasText(rec) {
		rec.Segments = append(rec.Segments, &Segment{Kind: "text", Text: e.Text})
	}
	rec.Done = true
	rec.Interrupted = aborted
	if st != nil && st.Duration > 0 {
		rec.Duration = st.Duration
	} else {
		rec.Duration = v.when(e.At).Sub(rec.At)
	}
	if !rec.hasContent() && (v.errShown || aborted || (st != nil && st.Error != "")) {
		v.removeRecord(rec)
	}
	v.live = nil
	v.busy = false
	v.errShown = false
}

func (v *View) removeRecord(r *Record) {
	for i, x := range v.msgs {
		if x == r {
			v.msgs = append(v.msgs[:i], v.msgs[i+1:]...)
			return
		}
	}
}

// Clear empties the transcript.
func (v *View) Clear() {
	v.msgs = nil
	v.live = nil
	v.busy = false
	v.compacting = nil
	v.errShown = false
	v.calls = map[string]*Call{}
	v.agents = map[string]*agent.AgentInfo{}
	v.top = 0
	v.sticky = true
	v.detachAt = 0
	v.railFocus = false
	v.gen++
}

// Export returns the transcript as plain, JSON-serialisable records.
func (v *View) Export() []Record {
	b, _ := json.Marshal(v.msgs)
	out := []Record{}
	_ = json.Unmarshal(b, &out)
	return out
}

// Restore replaces the transcript with recs (from Export). Turns are treated
// as finished; calls that were still running become errors.
func (v *View) Restore(recs []Record) {
	v.Clear()
	b, _ := json.Marshal(recs)
	var cp []*Record
	_ = json.Unmarshal(b, &cp)
	for _, r := range cp {
		if r.Role == roleAssistant {
			r.Done = true
		}
		for _, s := range r.Segments {
			s.Open = false
			for _, c := range s.Calls {
				v.calls[c.ID] = c
				if c.running() {
					c.Status = "error"
					c.Result = &tools.Result{Output: "interrupted", IsError: true}
				}
				for _, a := range c.Agents {
					v.agents[a.ID] = a
				}
			}
		}
	}
	v.msgs = cp
	v.gen++
}

// ChangedFiles lists the files the turns changed: created (new files) and
// modified, each in first-seen order and each path once.
func (v *View) ChangedFiles() (created, modified []string) {
	seen := map[string]bool{}
	for _, r := range v.msgs {
		for _, s := range r.Segments {
			for _, c := range s.Calls {
				if c.Status != "done" || c.Result == nil || c.Result.IsError {
					continue
				}
				isNew := strings.Contains(c.Result.Diff, "--- /dev/null")
				if b, ok := c.Result.Meta["created"].(bool); ok && b {
					isNew = true
				}
				for _, f := range c.Result.Files {
					if seen[f] {
						continue
					}
					seen[f] = true
					if isNew {
						created = append(created, f)
					} else {
						modified = append(modified, f)
					}
				}
			}
		}
	}
	return created, modified
}

// JumpToTurn scrolls so that user message n (1-based, steering excluded)
// sits at the top with one row of offset.
func (v *View) JumpToTurn(turn int) {
	n := 0
	for i, r := range v.msgs {
		if r.Role != roleUser || r.Steering {
			continue
		}
		n++
		if n != turn {
			continue
		}
		v.build()
		v.sticky = false
		if v.detachAt == 0 {
			v.detachAt = v.lastTotal
		}
		// The record's first row is its top padding, so "You" sits one row down.
		v.top = v.recStart[i]
		return
	}
}

// --- helpers on segments and records ---

func closeSegs(r *Record) {
	for _, s := range r.Segments {
		s.Open = false
	}
}

func lastSeg(r *Record, kind string) *Segment {
	for i := len(r.Segments) - 1; i >= 0; i-- {
		if r.Segments[i].Kind == kind {
			return r.Segments[i]
		}
	}
	return nil
}

func hasText(r *Record) bool {
	for _, s := range r.Segments {
		if s.Kind == "text" && trimmed(s.Text) != "" {
			return true
		}
	}
	return false
}

func resultStatus(r *tools.Result) string {
	if r == nil || !r.IsError {
		return "done"
	}
	low := strings.ToLower(r.Output)
	if strings.Contains(low, "denied") || strings.Contains(low, "rejected") || strings.Contains(low, "cancelled") {
		return "denied"
	}
	return "error"
}

func appendLines(prog []string, text string) []string {
	if text == "" {
		return prog
	}
	prog = append(prog, strings.Split(strings.TrimRight(text, "\n"), "\n")...)
	if len(prog) > 200 {
		prog = prog[len(prog)-200:]
	}
	return prog
}

func metaInt(m map[string]any, key string) int {
	switch n := m[key].(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}
