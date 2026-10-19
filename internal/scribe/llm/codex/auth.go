package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// statusTimeout bounds one read of the CLI: the version run, the app-server
// session, or a logout. A sign-in is not bounded by it; the user ends that.
const statusTimeout = 20 * time.Second

// availableTTL is how long Available remembers an answer for one binary. The
// status line asks on every render, and each answer costs an app-server start.
const availableTTL = 30 * time.Second

// maxModelPages stops a model list that keeps returning cursors.
const maxModelPages = 20

// loginEventBuffer holds every event of one sign-in, so the sign-in never
// blocks on a caller that has stopped reading.
const loginEventBuffer = 8

var errLoginCancelled = errors.New("sign-in cancelled")

// Overridable in tests, so they never open a real browser or read the clock
// of a real login.
var (
	openBrowserFn = openBrowser
	clock         = time.Now

	availMu    sync.Mutex
	availCache = map[string]availEntry{}
)

type availEntry struct {
	ok bool
	at time.Time
}

// Resolve finds the codex binary: bin as given, or DefaultBinary on PATH when
// bin is empty.
func Resolve(bin string) (string, error) {
	if bin == "" {
		bin = DefaultBinary
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return "", fmt.Errorf("the Codex CLI is not installed (%s is not on PATH); install it with: npm i -g @openai/codex", bin)
	}
	return path, nil
}

// ReadStatus reports the installed version, the signed-in account, and the
// rate limits. It does not return an error: a read that fails is put in
// Status.Err, and the rest of the status is still filled in as far as it goes.
func ReadStatus(ctx context.Context, bin string) Status {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	path, err := Resolve(bin)
	if err != nil {
		return Status{Err: err}
	}
	out, err := runCLI(ctx, path, "--version")
	if err != nil {
		return Status{Err: fmt.Errorf("%s --version failed: %w", path, err)}
	}
	st := Status{Installed: true, Binary: path, Version: strings.TrimSpace(out)}

	client, err := StartAppServer(ctx, path)
	if err != nil {
		st.Err = err
		loginStatusFallback(ctx, path, &st)
		return st
	}
	defer client.Close()
	if err := readAccount(ctx, client, &st); err != nil {
		st.Err = err
		loginStatusFallback(ctx, path, &st)
		return st
	}
	if err := readLimits(ctx, client, &st); err != nil {
		st.Err = err
	}
	return st
}

// readAccount fills the sign-in fields from account/read. A null account is
// not an error: it means nobody is signed in.
func readAccount(ctx context.Context, client *Client, st *Status) error {
	var resp struct {
		Account *struct {
			Type     string `json:"type"`
			Email    string `json:"email"`
			PlanType string `json:"planType"`
		} `json:"account"`
	}
	if err := client.Request(ctx, "account/read", map[string]any{}, &resp); err != nil {
		return err
	}
	if resp.Account == nil {
		st.LoggedIn = false
		return nil
	}
	st.LoggedIn = true
	st.AuthMode = resp.Account.Type
	st.Email = resp.Account.Email
	st.Plan = resp.Account.PlanType
	return nil
}

// readLimits fills the rate limits and credits from account/rateLimits/read.
func readLimits(ctx context.Context, client *Client, st *Status) error {
	var resp struct {
		RateLimits struct {
			Primary   *rateWindow `json:"primary"`
			Secondary *rateWindow `json:"secondary"`
			Credits   *struct {
				HasCredits bool            `json:"hasCredits"`
				Unlimited  bool            `json:"unlimited"`
				Balance    json.RawMessage `json:"balance"`
			} `json:"credits"`
		} `json:"rateLimits"`
	}
	if err := client.Request(ctx, "account/rateLimits/read", nil, &resp); err != nil {
		return err
	}
	st.Limits = nil
	for _, w := range []*rateWindow{resp.RateLimits.Primary, resp.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		rl := RateLimit{
			Name:        windowName(w.WindowDurationMins),
			UsedPercent: w.UsedPercent,
			Window:      time.Duration(w.WindowDurationMins) * time.Minute,
		}
		if w.ResetsAt > 0 {
			rl.ResetsAt = time.Unix(w.ResetsAt, 0)
		}
		st.Limits = append(st.Limits, rl)
	}
	if c := resp.RateLimits.Credits; c != nil {
		st.Credits = &Credits{
			HasCredits: c.HasCredits,
			Unlimited:  c.Unlimited,
			Balance:    balanceText(c.Balance),
		}
	}
	return nil
}

type rateWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int64   `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

// windowName turns a window length in minutes into the name the status shows.
func windowName(mins int64) string {
	switch {
	case mins == 10080:
		return "weekly"
	case mins <= 0:
		return "window"
	case mins%1440 == 0:
		return fmt.Sprintf("%dd", mins/1440)
	case mins%60 == 0:
		return fmt.Sprintf("%dh", mins/60)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// balanceText reads the balance, which the server sends as a string. A bare
// number or null is accepted too.
func balanceText(raw json.RawMessage) string {
	s := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if s == "null" {
		return ""
	}
	// The CLI reports e.g. "58726.9964000000"; two decimals read better.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'f', 2, 64)
	}
	return s
}

// loginStatusFallback asks the CLI itself when the app-server cannot answer.
// The app-server failure stays in st.Err, so the user sees why the fast path
// was not used.
func loginStatusFallback(ctx context.Context, path string, st *Status) {
	out, err := runCLI(ctx, path, "login", "status")
	st.LoggedIn = err == nil && strings.Contains(out, "Logged in")
}

// Login starts a sign-in and reports its progress on the returned channel. The
// channel is closed after a LoginDone or LoginFailed event. Cancelling ctx
// sends account/login/cancel and reports a LoginFailed whose error says
// "cancelled". If the account is already signed in, the first event is a
// LoginDone that says so.
func Login(ctx context.Context, bin string, mode LoginMode) (<-chan LoginEvent, error) {
	path, err := Resolve(bin)
	if err != nil {
		return nil, err
	}
	mode = resolveMode(mode)

	// The child's lifetime is the sign-in, not ctx alone, so runLogin can send
	// the cancel before the child is closed.
	client, err := startAppServer(ctx, context.WithoutCancel(ctx), path)
	if err != nil {
		return nil, err
	}
	var st Status
	if err := readAccount(ctx, client, &st); err != nil {
		_ = client.Close()
		return nil, err
	}
	ch := make(chan LoginEvent, loginEventBuffer)
	if st.LoggedIn {
		_ = client.Close()
		full := ReadStatus(ctx, path)
		ch <- LoginEvent{Kind: LoginDone, Mode: mode, Status: &full, AlreadySignedIn: true,
			Message: alreadySignedInMessage(st.Email)}
		close(ch)
		return ch, nil
	}

	method := "chatgpt"
	if mode == LoginDevice {
		method = "chatgptDeviceCode"
	}
	var start struct {
		LoginID         string `json:"loginId"`
		AuthURL         string `json:"authUrl"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := client.Request(ctx, "account/login/start", map[string]string{"type": method}, &start); err != nil {
		_ = client.Close()
		return nil, err
	}
	ev := LoginEvent{Kind: LoginStarted, Mode: mode, URL: start.AuthURL}
	if mode == LoginDevice {
		ev.URL = start.VerificationURL
		ev.Code = start.UserCode
	}
	if ev.URL == "" {
		_ = client.Close()
		return nil, errors.New("codex did not return a sign-in link")
	}
	if mode == LoginBrowser {
		ev.Opened = openBrowserFn(ev.URL)
	}
	ch <- ev
	go runLogin(ctx, bin, client, start.LoginID, mode, ch)
	return ch, nil
}

// runLogin waits for the server to finish the sign-in, then reports the result.
// It owns client and closes it.
func runLogin(ctx context.Context, bin string, client *Client, loginID string, mode LoginMode, ch chan<- LoginEvent) {
	defer close(ch)
	raw, err := client.WaitNotification(ctx, "account/login/completed", func(p json.RawMessage) bool {
		return loginIDOf(p) == loginID
	})
	if err != nil {
		if ctx.Err() != nil {
			cancelLogin(client, loginID)
			_ = client.Close()
			ch <- LoginEvent{Kind: LoginFailed, Mode: mode, Err: errLoginCancelled, Message: "Codex sign-in cancelled."}
			return
		}
		_ = client.Close()
		ch <- LoginEvent{Kind: LoginFailed, Mode: mode, Err: err}
		return
	}
	_ = client.Close()

	var done struct {
		Success bool    `json:"success"`
		Error   *string `json:"error"`
	}
	if json.Unmarshal(raw, &done) != nil || !done.Success {
		msg := "sign-in failed"
		if done.Error != nil && *done.Error != "" {
			msg = *done.Error
		}
		ch <- LoginEvent{Kind: LoginFailed, Mode: mode, Err: errors.New(msg)}
		return
	}
	// Read the result with a context that survives a cancel that came just now.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), statusTimeout)
	defer cancel()
	st := ReadStatus(sctx, bin)
	forgetAvailable(bin)
	ch <- LoginEvent{Kind: LoginDone, Mode: mode, Status: &st, Message: signedInMessage(st)}
}

// cancelLogin tells the server to drop the sign-in. It uses its own short
// deadline, because the sign-in's context is already done.
func cancelLogin(client *Client, loginID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = client.Request(ctx, "account/login/cancel", map[string]string{"loginId": loginID}, nil)
}

func loginIDOf(params json.RawMessage) string {
	var p struct {
		LoginID string `json:"loginId"`
	}
	_ = json.Unmarshal(params, &p)
	return p.LoginID
}

func alreadySignedInMessage(email string) string {
	if email == "" {
		return "Already signed in to Codex."
	}
	return "Already signed in to Codex as " + email + "."
}

func signedInMessage(st Status) string {
	if st.Email == "" {
		return "Signed in to Codex."
	}
	return "Signed in to Codex as " + st.Email + "."
}

// resolveMode picks the flow for LoginAuto: the browser when this machine has
// a display and the session is local, otherwise the device code. macOS always
// has a display for its opener, so it counts as one.
func resolveMode(mode LoginMode) LoginMode {
	if mode != LoginAuto {
		return mode
	}
	display := os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != "" || runtime.GOOS == "darwin"
	remote := os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != ""
	if display && !remote {
		return LoginBrowser
	}
	return LoginDevice
}

// Logout signs the CLI out through the app-server, and falls back to
// `<bin> logout` when that fails.
func Logout(ctx context.Context, bin string) error {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	defer forgetAvailable(bin)
	path, err := Resolve(bin)
	if err != nil {
		return err
	}
	client, err := StartAppServer(ctx, path)
	if err == nil {
		err = client.Request(ctx, "account/logout", nil, nil)
		_ = client.Close()
		if err == nil {
			return nil
		}
	}
	if _, err := runCLI(ctx, path, "logout"); err != nil {
		return fmt.Errorf("could not sign out of Codex: %w", err)
	}
	return nil
}

// ListModels returns the models the signed-in account can use. Tools is set
// on each, since the CLI runs its own tool loop. Name is the display name,
// falling back to the id.
func ListModels(ctx context.Context, bin string) ([]llm.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	path, err := Resolve(bin)
	if err != nil {
		return nil, err
	}
	client, err := StartAppServer(ctx, path)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	var out []llm.ModelInfo
	cursor := ""
	for page := 0; page < maxModelPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var resp struct {
			Data []struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := client.Request(ctx, "model/list", params, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Data {
			id := m.ID
			if id == "" {
				id = m.Model
			}
			if id == "" {
				continue
			}
			name := m.DisplayName
			if name == "" {
				name = id
			}
			out = append(out, llm.ModelInfo{ID: id, Name: name, Tools: true})
		}
		if resp.NextCursor == nil || *resp.NextCursor == "" {
			break
		}
		cursor = *resp.NextCursor
	}
	return out, nil
}

// Available reports whether the CLI is installed and signed in. The answer is
// kept for availableTTL per binary. A cancelled ctx gives an answer that is not
// kept.
func Available(ctx context.Context, bin string) bool {
	availMu.Lock()
	e, ok := availCache[bin]
	availMu.Unlock()
	if ok && clock().Sub(e.at) < availableTTL {
		return e.ok
	}
	answer := checkAvailable(ctx, bin)
	if ctx.Err() == nil {
		availMu.Lock()
		availCache[bin] = availEntry{ok: answer, at: clock()}
		availMu.Unlock()
	}
	return answer
}

func checkAvailable(ctx context.Context, bin string) bool {
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	path, err := Resolve(bin)
	if err != nil {
		return false
	}
	client, err := StartAppServer(ctx, path)
	if err != nil {
		return loggedInByCLI(ctx, path)
	}
	defer client.Close()
	var st Status
	if err := readAccount(ctx, client, &st); err != nil {
		return loggedInByCLI(ctx, path)
	}
	return st.LoggedIn
}

func loggedInByCLI(ctx context.Context, path string) bool {
	var st Status
	loginStatusFallback(ctx, path, &st)
	return st.LoggedIn
}

// forgetAvailable drops a cached availability answer, so the next check reads
// the account again. Called after a sign-in or sign-out.
func forgetAvailable(bin string) {
	availMu.Lock()
	delete(availCache, bin)
	availMu.Unlock()
}

// runCLI runs `<bin> args...` and returns its combined output. The child runs
// in its own process group, and the group is killed if ctx ends first. A
// non-zero exit is an error that includes the output.
func runCLI(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	setProcGroup(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		text := strings.TrimSpace(out.String())
		if err != nil && text != "" {
			return out.String(), fmt.Errorf("%w: %s", err, text)
		}
		return out.String(), err
	case <-ctx.Done():
		_ = killGroup(cmd)
		<-done
		return out.String(), ctx.Err()
	}
}

// openBrowser hands a link to the desktop's opener. It does not wait for the
// browser. A false result means the opener could not start, and the caller
// shows the link instead.
func openBrowser(u string) bool {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, u)
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { _ = cmd.Wait() }()
	return true
}
