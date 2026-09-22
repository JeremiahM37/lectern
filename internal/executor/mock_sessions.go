package executor

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// The mock's half of the interactive-session protocol.
//
// These two literals must match `sessions.PollDelimiter` and
// `sessions.DiscoverDelimiter`. They are duplicated rather than imported because
// the sessions package depends on this one; TestMockDelimitersMatchSessions
// asserts they have not drifted.
const (
	MockPollEnd           = "ADK-POLL-END-v2"
	MockPaneDelimiter     = "\x1e---LECTERN-PANE---\x1e"
	MockDiscoverDelimiter = "\x1e---LECTERN-PS---\x1e"
)

// mockPane is a scripted interactive agent: a pane of text that responds to what
// is typed into it, so the whole session loop (launch → status → send → handoff)
// is exercised without a real CLI.
type mockPane struct {
	trackingIdentity string
	lines            []string
	workdir          string
	// psArgs is how this agent appears in the process table, which is what
	// discovery joins on.
	psArgs string
	busy   bool
}

var (
	capturePaneRe = regexp.MustCompile(`capture-pane -p -t '?([^' ]+)'? -S`)
	loadBufferRe  = regexp.MustCompile(`tmux load-buffer -b lectern '?([^' ]+)'?`)
	pasteTargetRe = regexp.MustCompile(`paste-buffer -b lectern -t '?([^' ]+)'?`)
	sendKeysRe    = regexp.MustCompile(`^tmux send-keys -t '?([^' ]+)'? (\S+)$`)
	handoffPathRe = regexp.MustCompile(`(/tmp/lectern-handoff-\d+(?:-[a-f0-9]+)?\.md)`)
)

// interactiveSession decides whether a `tmux new-session` is starting an
// interactive session rather than a dispatched task. A task always redirects its
// output into the runtime events file; an interactive one never does.
func interactiveSession(cmd string) bool {
	return !strings.Contains(cmd, "events.jsonl")
}

func (m *Mock) startPane(sess, wt string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.panes == nil {
		m.panes = map[string]*mockPane{}
	}
	m.panes[sess] = &mockPane{workdir: wt, psArgs: "claude", lines: []string{
		"Welcome to the mock agent.",
		"cwd: " + wt,
		"",
		"❯ ",
	}}
}

// capture renders a pane the way `tmux capture-pane` would.
func (m *Mock) capture(sess string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.panes[sess]
	if !ok {
		return "", false
	}
	return strings.Join(p.lines, "\n"), true
}

// handlePoll answers a batched capture command for any number of sessions.
func (m *Mock) handlePoll(cmd string) Result {
	var b strings.Builder
	for _, match := range capturePaneRe.FindAllStringSubmatch(cmd, -1) {
		name := strings.TrimSuffix(strings.TrimPrefix(match[1], "="), ":")
		pane, ok := m.capture(name)
		if strings.Contains(cmd, MockPollEnd) {
			state := "ok"
			if !ok {
				state = "missing"
			}
			fmt.Fprintf(&b, "%s\t%s\t%s\n", base64.StdEncoding.EncodeToString([]byte(name)), state, base64.StdEncoding.EncodeToString([]byte(pane)))
		} else {
			b.WriteString(MockPaneDelimiter + name + "\n")
			if ok {
				b.WriteString(pane)
			}
		}
	}
	if strings.Contains(cmd, MockPollEnd) {
		b.WriteString(MockPollEnd + "\n")
	}
	return Result{0, b.String(), ""}
}

// handleSendText replays a paste into the pane and answers it, so a session that
// is sent a message visibly does something — including writing a handoff file
// when the message asks for one.
func (m *Mock) handleSendText(cmd string) Result {
	src := loadBufferRe.FindStringSubmatch(cmd)
	target := pasteTargetRe.FindStringSubmatch(cmd)
	if src == nil || target == nil {
		return Result{0, "", ""}
	}
	m.mu.Lock()
	text := string(m.fs[src[1]])
	pane, ok := m.panes[strings.TrimSuffix(strings.TrimPrefix(target[1], "="), ":")]
	m.mu.Unlock()
	if !ok {
		return Result{1, "", "no such session"}
	}

	m.mu.Lock()
	pane.lines = append(pane.lines, "❯ "+firstLine(text), "", "✻ Working… (esc to interrupt)")
	pane.busy = true
	m.mu.Unlock()

	// answer asynchronously, the way a real agent does — the operator's request
	// returns immediately and the status flips to running until it lands
	go func() {
		time.Sleep(m.Delay)
		if path := handoffPathRe.FindStringSubmatch(text); path != nil {
			m.mu.Lock()
			m.fs[path[1]] = []byte(mockHandoff + "\n<!-- lectern:complete " + path[1] + " -->\n")
			m.mu.Unlock()
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if pane.busy {
			pane.lines = append(pane.lines[:len(pane.lines)-1],
				"Understood.", "", "❯ ")
			pane.busy = false
		}
	}()
	return Result{0, "", ""}
}

// mockHandoff is what a scripted agent writes when asked to wrap up.
const mockHandoff = `## WHERE WE ARE
The mock agent finished the scripted change and verified it.

## NEXT
- Pick up the follow-up card on the board.

## DECISIONS
- Kept the existing interface; the change is additive.

## GOTCHAS
- None worth carrying forward.

## STATE
- Branch clean, nothing uncommitted.`

func (m *Mock) handleSendKey(cmd string) Result {
	match := sendKeysRe.FindStringSubmatch(cmd)
	if match == nil {
		return Result{0, "", ""}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	pane, ok := m.panes[strings.TrimSuffix(strings.TrimPrefix(match[1], "="), ":")]
	if !ok {
		return Result{1, "", "no such session"}
	}
	if match[2] == "Escape" || match[2] == "C-c" {
		pane.busy = false
		pane.lines = append(pane.lines, "[interrupted]", "❯ ")
	}
	return Result{0, "", ""}
}

// handleDiscover reports the scripted agents "already running" on this target,
// including any the mock itself started, so adoption is testable.
func (m *Mock) handleDiscover() Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	var panes, ps strings.Builder
	i := 100
	for name, p := range m.panes {
		fmt.Fprintf(&panes, "%s\t/dev/pts/%d\t%s\n", name, i, p.workdir)
		fmt.Fprintf(&ps, "pts/%d   %s\n", i, p.psArgs)
		i++
	}
	// a pane with no agent on it: discovery must ignore it rather than offering
	// the operator a plain shell to adopt
	fmt.Fprintf(&panes, "just-a-shell\t/dev/pts/%d\t/home/mock\n", i)
	fmt.Fprintf(&ps, "pts/%d   bash\n", i)
	return Result{0, panes.String() + MockDiscoverDelimiter + ps.String(), ""}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}

var mockIdentitySeed = regexp.MustCompile(`@lectern-tracking-identity '?([a-f0-9]{32})'?`)
var mockIdentityCondition = regexp.MustCompile(`@lectern-tracking-identity\},([a-f0-9]{32})\}`)

// handleTracking simulates the session-local option and atomic conditional stop.
// Real tmux regression tests own refused stops and changed-identity coverage.
func (m *Mock) handleTracking(cmd string) Result {
	name := strings.TrimSuffix(strings.TrimPrefix(strings.Trim(firstGroup(targetRe, cmd), "'"), "="), ":")
	m.mu.Lock()
	defer m.mu.Unlock()
	pane, ok := m.panes[name]
	if !ok {
		return Result{1, "", "can't find session"}
	}
	// The seed command reads the legacy option first, so set-option is no
	// longer at the front of the line.
	if strings.Contains(cmd, "tmux set-option") {
		match := mockIdentitySeed.FindStringSubmatch(cmd)
		if len(match) != 2 || pane.trackingIdentity != "" {
			return Result{1, "", "option already set or missing value"}
		}
		pane.trackingIdentity = match[1]
	}
	if strings.HasPrefix(cmd, "tmux if-shell") {
		match := mockIdentityCondition.FindStringSubmatch(cmd)
		if len(match) != 2 {
			return Result{1, "", "unsupported condition"}
		}
		if match[1] == pane.trackingIdentity {
			delete(m.panes, name)
		}
		return Result{0, "", ""}
	}
	return Result{0, pane.trackingIdentity + "\n", ""}
}
