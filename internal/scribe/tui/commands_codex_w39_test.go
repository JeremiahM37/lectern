package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/codex"
)

// codexTestBackend is the fake backend with a Codex CLI behind it. It records
// what /codex asked for. No test runs the real codex binary.
type codexTestBackend struct {
	*FakeBackend
	status      codex.Status
	statusCalls int
	loginMode   codex.LoginMode
	loginCtx    context.Context
	loginEvents chan codex.LoginEvent
	loginErr    error
	logoutErr   error
	calls       []string
}

func (c *codexTestBackend) CodexStatus(ctx context.Context) codex.Status {
	c.statusCalls++
	return c.status
}

func (c *codexTestBackend) CodexLogin(ctx context.Context, mode codex.LoginMode) (<-chan codex.LoginEvent, error) {
	c.calls = append(c.calls, "login")
	c.loginMode = mode
	c.loginCtx = ctx
	if c.loginErr != nil {
		return nil, c.loginErr
	}
	return c.loginEvents, nil
}

func (c *codexTestBackend) CodexLogout(ctx context.Context) error {
	c.calls = append(c.calls, "logout")
	return c.logoutErr
}

// newCodexTestModel is newTestModel with a backend that has a Codex CLI. The
// event channel is buffered, so tests queue events before reading them.
func newCodexTestModel(t *testing.T) (*Model, *codexTestBackend) {
	t.Helper()
	fb := NewFakeBackend()
	fb.Delay = 0
	cb := &codexTestBackend{FakeBackend: fb, loginEvents: make(chan codex.LoginEvent, 8)}
	m := NewModel(cb, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: 100, Height: 30, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m, cb
}

// stepCodex runs one command and feeds its message back to the model. It
// returns the command the model answers with, which may be nil.
func stepCodex(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command to run, got nil")
	}
	_, next := m.Update(cmd())
	return next
}

// drainCodex runs a command chain to its end and returns every flash it
// produced, in order.
func drainCodex(t *testing.T, m *Model, cmd tea.Cmd) []flashMsg {
	t.Helper()
	var flashes []flashMsg
	for i := 0; cmd != nil; i++ {
		if i > 50 {
			t.Fatal("command chain did not end")
		}
		msg := cmd()
		if f, ok := msg.(flashMsg); ok {
			flashes = append(flashes, f)
		}
		_, cmd = m.Update(msg)
	}
	return flashes
}

// codexPopup returns the open Codex Login popup.
func codexPopup(t *testing.T, m *Model) *codexLoginView {
	t.Helper()
	v, ok := m.topModal().(*codexLoginView)
	if !ok {
		t.Fatalf("top modal is %T, want the Codex Login popup", m.topModal())
	}
	return v
}

// popupText is what the login popup draws, as plain text.
func popupText(md Modal) string {
	return md.View(100, 40)
}

func TestCodexCommandIsRegistered(t *testing.T) {
	for _, c := range commandTable {
		if c.Name == "codex" {
			return
		}
	}
	t.Fatal("/codex is not in the command table")
}

func TestCodexNeedsACodexBackend(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fm := flashOf(t, codexCommand(m, "status"))
	if fm.kind != "err" || !strings.Contains(fm.text, "no Codex support") {
		t.Fatalf("flash = %+v, want an error about Codex support", fm)
	}
}

func TestCodexStatusPopupFillsWhenStatusArrives(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.status = codex.Status{Installed: true, Version: "codex-cli 0.163.0", Binary: "/usr/bin/codex",
		LoggedIn: true, Email: "me@example.com", Plan: "plus"}
	cmd := codexCommand(m, "")
	iv, ok := m.topModal().(*InfoView)
	if !ok {
		t.Fatalf("top modal is %T, want the status popup", m.topModal())
	}
	if iv.title != "Codex Status" || len(iv.lines) != 1 || !strings.Contains(iv.lines[0], "Checking") {
		t.Fatalf("popup before the read = %q %q, want a checking line", iv.title, iv.lines)
	}
	stepCodex(t, m, cmd)
	joined := strings.Join(iv.lines, "\n")
	if !strings.Contains(joined, "codex-cli 0.163.0") || !strings.Contains(joined, "me@example.com (plus plan)") {
		t.Errorf("popup did not fill in:\n%s", joined)
	}
	if cb.statusCalls != 1 {
		t.Errorf("status read %d times, want once", cb.statusCalls)
	}
}

func TestCodexStatusLinesCoverEachState(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	missing := strings.Join(codexStatusLines(codex.Status{}, now), "\n")
	if !strings.Contains(missing, "Codex CLI: not installed. Install it with: npm i -g @openai/codex") {
		t.Errorf("not-installed status lacks the install line:\n%s", missing)
	}

	out := strings.Join(codexStatusLines(codex.Status{Installed: true, Version: "codex-cli 0.163.0"}, now), "\n")
	if !strings.Contains(out, "Account: not signed in. Run /codex login.") {
		t.Errorf("signed-out status lacks the sign-in hint:\n%s", out)
	}

	st := codex.Status{
		Installed: true, Version: "codex-cli 0.163.0", Binary: "/usr/bin/codex",
		LoggedIn: true, Email: "me@example.com", Plan: "plus",
		Limits: []codex.RateLimit{
			{Name: "5h", UsedPercent: 96, ResetsAt: now.Add(3*time.Hour + 12*time.Minute)},
			{Name: "weekly", UsedPercent: 18, ResetsAt: now.Add(4*24*time.Hour + 2*time.Hour)},
		},
		Credits: &codex.Credits{HasCredits: true, Balance: "58726.99"},
		Err:     errors.New("app-server\nfailed to start"),
	}
	lines := codexStatusLines(st, now)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"Codex CLI: codex-cli 0.163.0 (/usr/bin/codex)",
		"Account: me@example.com (plus plan)",
		"5h limit",
		"96% used, resets in 3h 12m",
		"weekly limit",
		"resets in 4d 2h",
		"Credits balance: 58726.99",
		"Note: app-server failed to start",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("status lacks %q:\n%s", want, joined)
		}
	}
	for _, l := range lines {
		if strings.Contains(l, "\n") {
			t.Errorf("a status line spans two rows: %q", l)
		}
	}
	if !strings.Contains(joined, "█") || !strings.Contains(joined, "░") {
		t.Errorf("a limit has no bar:\n%s", joined)
	}
}

func TestCodexDurationAndResetText(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	cases := map[time.Duration]string{
		30 * time.Second:             "less than a minute",
		45 * time.Minute:             "45m",
		3*time.Hour + 12*time.Minute: "3h 12m",
		4*24*time.Hour + 2*time.Hour: "4d 2h",
	}
	for d, want := range cases {
		if got := codexDuration(d); got != want {
			t.Errorf("codexDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := codexResetText(time.Time{}, now); got != "reset time unknown" {
		t.Errorf("zero reset = %q", got)
	}
	if got := codexResetText(now.Add(-time.Minute), now); got != "resets now" {
		t.Errorf("past reset = %q", got)
	}
}

func TestCodexSignedInText(t *testing.T) {
	if got := codexSignedInText(nil); got != "Signed in to Codex" {
		t.Errorf("nil status = %q", got)
	}
	if got := codexSignedInText(&codex.Status{Email: "a@b.c", Plan: "plus"}); got != "Signed in to Codex as a@b.c (plus)" {
		t.Errorf("status with account = %q", got)
	}
}

func TestCodexDeviceLoginUpdatesLiveAndFlashesSignedIn(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginStarted, Mode: codex.LoginDevice,
		URL: "https://auth.example/device", Code: "ABCD-1234"}
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginInfo, Message: "Checking the code..."}
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginDone, Status: &codex.Status{Email: "me@example.com", Plan: "plus"}}

	cmd := codexCommand(m, "login device")
	v := codexPopup(t, m)
	if v.Title() != "Codex Login" || !strings.Contains(popupText(v), "Starting sign-in") {
		t.Fatalf("popup before the CLI answers should say it is starting")
	}
	next := stepCodex(t, m, cmd)
	if cb.loginMode != codex.LoginDevice {
		t.Errorf("login mode = %v, want device", cb.loginMode)
	}
	next = stepCodex(t, m, next)
	text := popupText(codexPopup(t, m))
	for _, want := range []string{"https://auth.example/device", "ABCD-1234", "any device"} {
		if !strings.Contains(text, want) {
			t.Errorf("device popup lacks %q:\n%s", want, text)
		}
	}
	flashes := drainCodex(t, m, next)
	if len(flashes) == 0 || flashes[len(flashes)-1].kind != "ok" ||
		flashes[len(flashes)-1].text != "Signed in to Codex as me@example.com (plus)" {
		t.Fatalf("flashes = %+v, want the signed-in flash", flashes)
	}
	if m.topModal() != nil {
		t.Errorf("the login popup should close once the sign-in is done")
	}
}

func TestCodexLoginWhenAlreadySignedInSaysSo(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginDone, AlreadySignedIn: true,
		Status: &codex.Status{Email: "me@example.com", Plan: "plus"}}
	flashes := drainCodex(t, m, codexCommand(m, "login"))
	want := "Already signed in to Codex as me@example.com (plus). /codex switch signs in with another account."
	if len(flashes) == 0 || flashes[len(flashes)-1].text != want {
		t.Fatalf("flashes = %+v, want %q", flashes, want)
	}
}

func TestCodexBrowserLoginSaysWhetherABrowserOpened(t *testing.T) {
	for _, opened := range []bool{true, false} {
		m, cb := newCodexTestModel(t)
		cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginStarted, Mode: codex.LoginBrowser,
			URL: "https://auth.example/start", Opened: opened}
		next := stepCodex(t, m, codexCommand(m, "login browser"))
		stepCodex(t, m, next)
		text := popupText(codexPopup(t, m))
		if !strings.Contains(text, "https://auth.example/start") {
			t.Errorf("opened=%v: popup lacks the URL:\n%s", opened, text)
		}
		if opened && !strings.Contains(text, "A browser window opened here") {
			t.Errorf("opened=true: popup does not say a browser opened:\n%s", text)
		}
		if !opened && !strings.Contains(text, "Could not open a browser here") {
			t.Errorf("opened=false: popup does not say no browser opened:\n%s", text)
		}
	}
}

func TestCodexLoginFailureFlashesTheError(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginStarted, Mode: codex.LoginDevice, URL: "u", Code: "c"}
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginFailed, Err: errors.New("boom")}
	flashes := drainCodex(t, m, codexCommand(m, "login device"))
	if len(flashes) == 0 || flashes[len(flashes)-1].kind != "err" ||
		flashes[len(flashes)-1].text != "Codex sign-in failed: boom" {
		t.Fatalf("flashes = %+v, want the failure", flashes)
	}
	if m.topModal() != nil {
		t.Errorf("the popup should close on failure")
	}
}

func TestCodexLoginStartErrorFlashes(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.loginErr = errors.New("no codex here")
	flashes := drainCodex(t, m, codexCommand(m, "login"))
	if len(flashes) == 0 || flashes[len(flashes)-1].text != "Codex sign-in failed: no codex here" {
		t.Fatalf("flashes = %+v, want the start error", flashes)
	}
}

func TestCodexLoginEscCancelsTheSignIn(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginStarted, Mode: codex.LoginBrowser, URL: "u"}
	// The first command starts the sign-in; the read that follows is never run,
	// so nothing waits on the empty channel.
	stepCodex(t, m, codexCommand(m, "login browser"))
	if cb.loginCtx.Err() != nil {
		t.Fatal("the sign-in was cancelled before the popup closed")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cb.loginCtx.Err() == nil {
		t.Fatal("Esc did not cancel the sign-in context")
	}
	if m.topModal() != nil {
		t.Fatal("Esc did not close the popup")
	}
	fm := flashOf(t, cmd)
	if fm.kind != "info" || !strings.Contains(fm.text, "cancelled") {
		t.Errorf("cancel flash = %+v", fm)
	}
}

func TestCodexLoginCopyKeyCopiesTheCode(t *testing.T) {
	buf := &bytes.Buffer{}
	old := clipboardOut
	clipboardOut = buf
	t.Cleanup(func() { clipboardOut = old })

	m, cb := newCodexTestModel(t)
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginStarted, Mode: codex.LoginDevice, URL: "u", Code: "ABCD-1234"}
	next := stepCodex(t, m, codexCommand(m, "login device"))
	stepCodex(t, m, next)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	want := base64.StdEncoding.EncodeToString([]byte("ABCD-1234"))
	if !strings.Contains(buf.String(), want) {
		t.Errorf("c did not copy the code; clipboard output %q", buf.String())
	}
	if !strings.Contains(popupText(codexPopup(t, m)), "Copied the code") {
		t.Errorf("popup does not confirm the copy")
	}
}

func TestCodexLoginModesFromArguments(t *testing.T) {
	cases := map[string]codex.LoginMode{
		"login":         codex.LoginAuto,
		"login browser": codex.LoginBrowser,
		"login device":  codex.LoginDevice,
	}
	for args, want := range cases {
		m, cb := newCodexTestModel(t)
		stepCodex(t, m, codexCommand(m, args))
		if cb.loginMode != want {
			t.Errorf("%q: mode = %v, want %v", args, cb.loginMode, want)
		}
		m.clearModals()
	}
}

func TestCodexLogoutFlashesAndNamesACodexModel(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.FakeBackend.SetModel("codex/gpt-6.1-sol")
	fm := flashOf(t, codexCommand(m, "logout"))
	if fm.kind != "ok" || !strings.Contains(fm.text, "/models") {
		t.Errorf("codex model flash = %+v, want the pick-another-model line", fm)
	}
	if len(cb.calls) != 1 || cb.calls[0] != "logout" {
		t.Errorf("calls = %v, want [logout]", cb.calls)
	}

	cb.FakeBackend.SetModel("openai/gpt-x")
	fm = flashOf(t, codexCommand(m, "logout"))
	if fm.kind != "ok" || strings.Contains(fm.text, "/models") {
		t.Errorf("non-codex model flash = %+v, want no model line", fm)
	}
}

func TestCodexLogoutErrorFlashes(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.logoutErr = errors.New("denied")
	fm := flashOf(t, codexCommand(m, "logout"))
	if fm.kind != "err" || fm.text != "Could not sign out of Codex: denied" {
		t.Errorf("flash = %+v", fm)
	}
}

func TestCodexSwitchSignsOutThenLogsIn(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.loginEvents <- codex.LoginEvent{Kind: codex.LoginDone, Status: &codex.Status{Email: "new@example.com", Plan: "pro"}}
	flashes := drainCodex(t, m, codexCommand(m, "switch"))
	if len(cb.calls) != 2 || cb.calls[0] != "logout" || cb.calls[1] != "login" {
		t.Fatalf("calls = %v, want logout then login", cb.calls)
	}
	if len(flashes) == 0 || flashes[len(flashes)-1].text != "Signed in to Codex as new@example.com (pro)" {
		t.Errorf("flashes = %+v", flashes)
	}
}

func TestCodexSwitchStopsWhenSignOutFails(t *testing.T) {
	m, cb := newCodexTestModel(t)
	cb.logoutErr = errors.New("denied")
	flashes := drainCodex(t, m, codexCommand(m, "switch"))
	if len(cb.calls) != 1 || cb.calls[0] != "logout" {
		t.Fatalf("calls = %v, want only logout", cb.calls)
	}
	if len(flashes) == 0 || !strings.Contains(flashes[len(flashes)-1].text, "Could not sign out") {
		t.Errorf("flashes = %+v", flashes)
	}
}

func TestCodexUnknownSubcommandShowsUsage(t *testing.T) {
	m, _ := newCodexTestModel(t)
	for _, args := range []string{"frobnicate", "status now", "login sideways", "login device extra", "logout now", "switch now"} {
		fm := flashOf(t, codexCommand(m, args))
		if fm.kind != "err" || !strings.Contains(fm.text, "Usage: /codex") {
			t.Errorf("%q: flash = %+v, want usage", args, fm)
		}
	}
	if m.topModal() != nil {
		t.Errorf("a usage error should not open a popup")
	}
}
