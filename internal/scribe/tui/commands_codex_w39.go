package tui

// Wave 39: /codex, the Codex (ChatGPT subscription) status and sign-in popups
// (spec 02 /codex rows, spec 03 section 4.4). The CLI work lives in llm/codex;
// this file decides what the popups show and how they update. The backend
// methods are in app/backend_codex_w39.go.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/codex"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// CodexAccount is the optional backend interface /codex uses. CodexStatus
// never fails: problems come back in Status.Err. CodexLogin returns a channel
// of LoginEvents that is closed after a done or failed event; cancelling ctx
// cancels the sign-in.
type CodexAccount interface {
	CodexStatus(ctx context.Context) codex.Status
	CodexLogin(ctx context.Context, mode codex.LoginMode) (<-chan codex.LoginEvent, error)
	CodexLogout(ctx context.Context) error
}

// codexStatusWait bounds the status read. The CLI starts an app-server and
// asks it for the account and the limits, which can take a few seconds.
const codexStatusWait = 20 * time.Second

// codexLogoutWait bounds a sign-out.
const codexLogoutWait = 30 * time.Second

// codexUsageText is the usage line for a /codex subcommand that is not known
// or is given the wrong arguments.
const codexUsageText = "Usage: /codex status, /codex login [device|browser], /codex logout, or /codex switch"

func init() {
	registerCommands(
		&command{Name: "codex", Args: "[status|login [device|browser]|logout|switch]",
			Desc: "Codex (ChatGPT subscription): show sign-in status, sign in, sign out or switch account", Group: "Models",
			Run: func(m *Model, args string) tea.Cmd { return codexCommand(m, strings.TrimSpace(args)) }},
	)
}

// codexCommand dispatches /codex. With no argument it shows the status.
func codexCommand(m *Model, args string) tea.Cmd {
	ca, ok := m.cur().be.(CodexAccount)
	if !ok {
		return cmdErrText("this session has no Codex support")
	}
	f := strings.Fields(args)
	if len(f) == 0 {
		return codexStatusCommand(m, ca)
	}
	sub, rest := strings.ToLower(f[0]), f[1:]
	switch sub {
	case "status":
		if len(rest) == 0 {
			return codexStatusCommand(m, ca)
		}
	case "login":
		if mode, ok := codexLoginMode(rest); ok {
			return codexLoginCommand(m, ca, mode, false)
		}
	case "logout":
		if len(rest) == 0 {
			return codexLogoutCommand(m, ca)
		}
	case "switch":
		if len(rest) == 0 {
			return codexLoginCommand(m, ca, codex.LoginAuto, true)
		}
	}
	return cmdErrText(codexUsageText)
}

// codexLoginMode reads the optional flow after /codex login. No argument lets
// the CLI pick; "device" and "browser" force one.
func codexLoginMode(rest []string) (codex.LoginMode, bool) {
	if len(rest) == 0 {
		return codex.LoginAuto, true
	}
	if len(rest) > 1 {
		return codex.LoginAuto, false
	}
	switch strings.ToLower(rest[0]) {
	case "device":
		return codex.LoginDevice, true
	case "browser":
		return codex.LoginBrowser, true
	}
	return codex.LoginAuto, false
}

// ---------------------------------------------------------------- status

// codexStatusCommand opens the status popup right away and fills it when the
// read finishes, so a slow CLI start never leaves the screen blank.
func codexStatusCommand(m *Model, ca CodexAccount) tea.Cmd {
	iv := NewInfoView(m.th, "Codex Status", "gauge", []string{"Checking the Codex CLI..."})
	m.pushModal(iv)
	return asyncModal(iv, func() func() (tea.Cmd, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), codexStatusWait)
		defer cancel()
		st := ca.CodexStatus(ctx)
		return func() (tea.Cmd, bool) {
			iv.lines = codexStatusLines(st, time.Now())
			return nil, false
		}
	})
}

// codexStatusLines is the text of the status popup. Each line is short enough
// to read at 80 columns; a limit is one line with its bar.
func codexStatusLines(st codex.Status, now time.Time) []string {
	var lines []string
	if st.Installed {
		cli := "installed"
		if st.Version != "" {
			cli = st.Version
		}
		if st.Binary != "" {
			cli += " (" + st.Binary + ")"
		}
		lines = append(lines, "Codex CLI: "+cli)
	} else {
		lines = append(lines, "Codex CLI: not installed. Install it with: npm i -g @openai/codex")
	}
	lines = append(lines, codexAccountLine(st))
	for _, rl := range st.Limits {
		lines = append(lines, codexLimitLine(rl, now))
	}
	if st.Credits != nil {
		lines = append(lines, codexCreditsLine(*st.Credits))
	}
	if st.Err != nil {
		lines = append(lines, "Note: "+codexOneLine(st.Err.Error()))
	}
	return lines
}

func codexAccountLine(st codex.Status) string {
	switch {
	case !st.Installed:
		return "Account: not checked, because Codex is not installed."
	case !st.LoggedIn:
		return "Account: not signed in. Run /codex login."
	}
	who := st.Email
	if who == "" {
		who = "signed in"
	}
	if st.Plan != "" {
		who += " (" + st.Plan + " plan)"
	}
	return "Account: " + who
}

// codexLimitLine draws one usage window, e.g.
// "5h limit     [###################-] 96% used, resets in 3h 12m".
func codexLimitLine(rl codex.RateLimit, now time.Time) string {
	name := padRight(rl.Name+" limit", 12)
	frac := clampF(rl.UsedPercent/100, 0, 1)
	return fmt.Sprintf("%s [%s] %3.0f%% used, %s", name, bar(frac, 20), rl.UsedPercent, codexResetText(rl.ResetsAt, now))
}

func codexResetText(at, now time.Time) string {
	if at.IsZero() {
		return "reset time unknown"
	}
	left := at.Sub(now)
	if left <= 0 {
		return "resets now"
	}
	return "resets in " + codexDuration(left)
}

// codexDuration writes a time left in two units at most: "4d 2h", "3h 12m",
// "45m". Under a minute it says so rather than "0m".
func codexDuration(d time.Duration) string {
	if d < time.Minute {
		return "less than a minute"
	}
	days := int(d / (24 * time.Hour))
	hours := int(d/time.Hour) % 24
	mins := int(d/time.Minute) % 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	}
	return fmt.Sprintf("%dm", mins)
}

func codexCreditsLine(c codex.Credits) string {
	switch {
	case c.Unlimited:
		return "Credits: unlimited"
	case c.HasCredits && c.Balance != "":
		return "Credits balance: " + c.Balance
	case c.HasCredits:
		return "Credits: available"
	}
	return "Credits: none"
}

// codexOneLine joins a message onto one line, so a multi-line CLI error
// stays one row in the popup.
func codexOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// codexErrText is an error as one line, with a fallback for a nil error.
func codexErrText(err error) string {
	if err == nil {
		return "unknown error"
	}
	return codexOneLine(err.Error())
}

// ---------------------------------------------------------------- login

// codexLine is one row of the login popup. Colour and bold are optional.
type codexLine struct {
	text  string
	color lipgloss.TerminalColor
	bold  bool
}

// codexLoginView is the "Codex Login" popup. Each LoginEvent from the CLI
// updates it in place (see receive). Esc or closing the popup cancels the
// sign-in, so a popup left on screen never leaves a login running.
type codexLoginView struct {
	th        theme.Theme
	ctx       context.Context
	cancel    context.CancelFunc
	ch        <-chan codex.LoginEvent
	mode      codex.LoginMode // the flow the CLI chose, once it has said
	switching bool            // /codex switch: signed out before this sign-in
	url, code string
	opened    bool   // a browser was opened on this machine (browser flow)
	info      string // latest progress line from the CLI
	note      string // what the last c press copied
}

// Title implements Modal.
func (v *codexLoginView) Title() string { return "Codex Login" }

// Icon implements Iconed.
func (v *codexLoginView) Icon() string { return "key" }

// Footer implements Footered. It names the copy key only when there is
// something to copy.
func (v *codexLoginView) Footer() string {
	if text, what := v.copyTarget(); text != "" {
		return "c copy " + what + " · Esc cancel"
	}
	return "Esc cancel"
}

// Size implements Sized. The frame fits the text, as the info popups do.
func (v *codexLoginView) Size(cols, rows int) (int, int) {
	w := min(72, max(int(float64(cols)*0.8), cols-2*overlayMinMargin))
	n := 0
	for _, l := range v.lines() {
		n += strings.Count(render.WrapPath(l.text, max(w-4, 10)), "\n") + 1
	}
	return w, clamp(n+2, 6, min(rows-4, 24))
}

// Update implements Modal. Keys only: CLI events arrive through modalDone.
func (v *codexLoginView) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return v, nil, false
	}
	switch km.String() {
	case "esc", "q":
		text := "Codex sign-in cancelled."
		if v.switching {
			text += " You are signed out of Codex; run /codex login to sign in."
		}
		return v, flashCmd("info", text), true
	case "c":
		text, what := v.copyTarget()
		if text == "" {
			return v, nil, false
		}
		copyToTerminal(text)
		v.note = "Copied the " + what + " to the clipboard."
	}
	return v, nil, false
}

// View implements Modal.
func (v *codexLoginView) View(w, h int) string {
	var out []string
	for _, l := range v.lines() {
		for _, piece := range strings.Split(render.WrapPath(l.text, w), "\n") {
			out = append(out, styleCodexLine(l, piece))
		}
	}
	return strings.Join(fitLines(out, w, h), "\n")
}

// OnClose implements Closer: leaving the popup cancels the sign-in.
func (v *codexLoginView) OnClose() {
	if v.cancel != nil {
		v.cancel()
	}
}

// copyTarget is what the c key copies: the code for the device flow, the
// address otherwise. An empty text means there is nothing to copy yet.
func (v *codexLoginView) copyTarget() (string, string) {
	if v.mode == codex.LoginDevice && v.code != "" {
		return v.code, "code"
	}
	if v.url != "" {
		return v.url, "link"
	}
	return "", ""
}

// lines is the popup text for the state so far.
func (v *codexLoginView) lines() []codexLine {
	if v.url == "" && v.code == "" {
		text := "Starting sign-in..."
		if v.switching {
			text = "Signing out of Codex, then starting sign-in..."
		}
		return []codexLine{{text: text, color: v.th.TextFaint}}
	}
	out := []codexLine{{text: "Sign in to Codex with your ChatGPT account."}, {text: ""}}
	waiting := "Waiting for the sign-in to finish."
	if v.mode == codex.LoginDevice {
		waiting = "Waiting for you to finish on the other device."
		out = append(out,
			codexLine{text: "1. On any device, such as your phone, open:"},
			codexLine{text: "   " + v.url, color: v.th.Brand},
			codexLine{text: "2. Enter this one-time code:"},
			codexLine{text: "   " + v.code, color: v.th.Brand, bold: true},
		)
	} else {
		first, color := "A browser window opened here. Finish signing in there.", v.th.Success
		if !v.opened {
			first, color = "Could not open a browser here. Open this address to sign in:", v.th.Warning
		}
		out = append(out,
			codexLine{text: first, color: color},
			codexLine{text: "   " + v.url, color: v.th.Brand},
		)
	}
	out = append(out, codexLine{text: ""})
	if v.info != "" {
		out = append(out, codexLine{text: v.info})
	}
	out = append(out, codexLine{text: waiting, color: v.th.TextFaint})
	if v.note != "" {
		out = append(out, codexLine{text: v.note, color: v.th.Success})
	}
	return out
}

// styleCodexLine colours one wrapped piece of a line.
func styleCodexLine(l codexLine, s string) string {
	st := lipgloss.NewStyle().Bold(l.bold)
	if l.color != nil {
		st = st.Foreground(l.color)
	}
	return st.Render(s)
}

// codexLoginCommand opens the login popup and starts the sign-in behind it.
// With switching set, the current account is signed out first. The CLI work
// runs off the event loop; the popup gets the channel back through modalDone.
func codexLoginCommand(m *Model, ca CodexAccount, mode codex.LoginMode, switching bool) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	v := &codexLoginView{th: m.th, ctx: ctx, cancel: cancel, mode: mode, switching: switching}
	m.pushModal(v)
	return asyncModal(v, func() func() (tea.Cmd, bool) {
		if switching {
			if err := ca.CodexLogout(ctx); err != nil {
				return func() (tea.Cmd, bool) {
					return flashCmd("err", "Could not sign out of Codex: "+codexErrText(err)), true
				}
			}
		}
		ch, err := ca.CodexLogin(ctx, mode)
		if err == nil && ch == nil {
			// A nil channel would block the first read forever.
			err = fmt.Errorf("the CLI sent no sign-in events")
		}
		if err != nil {
			return func() (tea.Cmd, bool) {
				return flashCmd("err", "Codex sign-in failed: "+codexErrText(err)), true
			}
		}
		return func() (tea.Cmd, bool) {
			v.ch = ch
			return codexNextEvent(v), false
		}
	})
}

// codexNextEvent reads one event and re-arms itself. It stops when the sign-in
// ends, when the popup closes (the ctx is cancelled), or when the channel
// closes.
func codexNextEvent(v *codexLoginView) tea.Cmd {
	ch, ctx := v.ch, v.ctx
	return asyncModal(v, func() func() (tea.Cmd, bool) {
		select {
		case ev, ok := <-ch:
			return func() (tea.Cmd, bool) { return v.receive(ev, ok) }
		case <-ctx.Done():
			return func() (tea.Cmd, bool) { return nil, false }
		}
	})
}

// receive applies one LoginEvent to the popup. It returns the next read, or
// the flash that ends the popup.
func (v *codexLoginView) receive(ev codex.LoginEvent, ok bool) (tea.Cmd, bool) {
	if !ok {
		return flashCmd("err", "Codex sign-in ended without an answer. Run /codex login to try again."), true
	}
	switch ev.Kind {
	case codex.LoginStarted:
		v.mode, v.url, v.code, v.opened = ev.Mode, ev.URL, ev.Code, ev.Opened
	case codex.LoginInfo:
		if ev.Message != "" {
			v.info = ev.Message
		}
	case codex.LoginDone:
		if ev.AlreadySignedIn {
			return flashCmd("info", codexAlreadyText(ev.Status)), true
		}
		return flashCmd("ok", codexSignedInText(ev.Status)), true
	case codex.LoginFailed:
		return flashCmd("err", "Codex sign-in failed: "+codexErrText(ev.Err)), true
	}
	return codexNextEvent(v), false
}

// codexSignedInText is the flash after a sign-in, with the account when the
// CLI reported one.
func codexSignedInText(st *codex.Status) string {
	if st == nil || st.Email == "" {
		return "Signed in to Codex"
	}
	text := "Signed in to Codex as " + st.Email
	if st.Plan != "" {
		text += " (" + st.Plan + ")"
	}
	return text
}

// codexAlreadyText is the flash when /codex login found the CLI already
// signed in, so nothing changed; it names the way to change account.
func codexAlreadyText(st *codex.Status) string {
	text := "Already signed in to Codex"
	if st != nil && st.Email != "" {
		text += " as " + st.Email
		if st.Plan != "" {
			text += " (" + st.Plan + ")"
		}
	}
	return text + ". /codex switch signs in with another account."
}

// ---------------------------------------------------------------- logout

// codexLogoutCommand signs out at once, with no confirmation (spec 02). It does
// not change the active model; it only warns when the active model is a Codex
// one, since that model will now fail until another sign-in.
func codexLogoutCommand(m *Model, ca CodexAccount) tea.Cmd {
	ref := m.cur().be.Model()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), codexLogoutWait)
		defer cancel()
		if err := ca.CodexLogout(ctx); err != nil {
			return flashMsg{kind: "err", text: "Could not sign out of Codex: " + codexErrText(err)}
		}
		return flashMsg{kind: "ok", text: codexLogoutText(ref)}
	}
}

// codexLogoutText is the flash after a sign-out.
func codexLogoutText(modelRef string) string {
	text := "Signed out of Codex."
	if strings.HasPrefix(modelRef, codex.ProviderID+"/") {
		text += " The current model is a Codex model; pick another with /models."
	}
	return text
}
