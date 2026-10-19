package codex

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The tests never run the real codex. A fake is this test binary, re-run
// through a small shell script named "codex" in a temp dir. The script execs
// the test binary with -test.run pointing at TestCodexFakeServerProcess, which
// then speaks the app-server protocol (or answers the CLI subcommands) and
// exits. Behaviour is chosen by environment variables, which the child
// inherits.
//
// Environment:
//   GO_WANT_CODEX_FAKE   "1" turns the helper on
//   FAKE_CODEX_MODE      scenario, see fakeCodexServe
//   FAKE_CODEX_HOME      state dir: auth.json present means signed in
//   FAKE_CODEX_PIDFILE   the child writes its pid here at start
//   FAKE_CODEX_OUTCOME   "success" or "fail" for a sign-in that completes

const fakeLoginID = "login-1"

func TestCodexFakeServerProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_FAKE") != "1" {
		t.Skip("helper process for the fake codex binary")
	}
	os.Exit(fakeCodexMain(flag.Args()))
}

func fakeCodexMain(args []string) int {
	home := os.Getenv("FAKE_CODEX_HOME")
	if len(args) == 0 {
		return 2
	}
	switch {
	case args[0] == "--version":
		fmt.Println("codex-cli 0.163.0")
		return 0
	case args[0] == "app-server":
		return fakeCodexServe(home)
	case args[0] == "login" && len(args) > 1 && args[1] == "status":
		logRequest(home, "login status")
		if signedIn(home) {
			fmt.Println("Logged in using ChatGPT")
			return 0
		}
		fmt.Println("Not logged in")
		return 1
	case args[0] == "logout":
		logRequest(home, "logout")
		_ = os.Remove(filepath.Join(home, "auth.json"))
		fmt.Println("Successfully logged out")
		return 0
	}
	return 2
}

// fakeCodexServe speaks JSON-RPC on stdin and stdout. Each scenario is one
// behaviour the client has to handle.
func fakeCodexServe(home string) int {
	mode := os.Getenv("FAKE_CODEX_MODE")
	if pf := os.Getenv("FAKE_CODEX_PIDFILE"); pf != "" {
		_ = os.WriteFile(pf, []byte(fmt.Sprint(os.Getpid())), 0o600)
	}
	if mode == "ignore-term" {
		signal.Ignore(syscall.SIGTERM)
	}
	if mode == "hang-init" {
		time.Sleep(time.Hour)
		return 0
	}

	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		b, _ := json.Marshal(v)
		out.Write(append(b, '\n'))
		out.Flush()
	}
	interleave := func() {
		if mode != "interleave" {
			return
		}
		send(map[string]any{"jsonrpc": "2.0", "method": "account/updated", "params": map[string]any{"authMode": "chatgpt"}})
		send(map[string]any{"jsonrpc": "2.0", "method": "remoteControl/status/changed", "params": map[string]any{"status": "disabled"}})
	}
	reply := func(id json.RawMessage, result any) {
		interleave()
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	fail := func(id json.RawMessage, msg string) {
		interleave()
		send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": msg}})
	}
	completion := func(success bool) map[string]any {
		params := map[string]any{"loginId": fakeLoginID, "success": success, "error": nil}
		if !success {
			params["error"] = "access denied"
		}
		return map[string]any{"jsonrpc": "2.0", "method": "account/login/completed", "params": params}
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		if msg.Method == "" || len(msg.ID) == 0 {
			continue // notifications such as "initialized" need no answer
		}
		logRequest(home, msg.Method)
		switch msg.Method {
		case "initialize":
			reply(msg.ID, map[string]any{"userAgent": "fake", "codexHome": home})
		case "account/read":
			if mode == "rpc-error" {
				fail(msg.ID, "boom")
				continue
			}
			reply(msg.ID, map[string]any{"account": accountFor(home), "requiresOpenaiAuth": true})
		case "account/rateLimits/read":
			reply(msg.ID, rateLimitsFor(home))
		case "model/list":
			reply(msg.ID, modelPage(msg.Params))
		case "account/login/start":
			// device-early finishes the sign-in before the reply is sent, so
			// the client must keep the notification until it asks for it.
			// device-late finishes it after. device-hang never finishes.
			switch mode {
			case "device-early":
				send(completion(outcomeSucceeds()))
				markSignedIn(home, outcomeSucceeds())
				reply(msg.ID, loginStartReply(msg.Params))
			case "device-late", "browser-late":
				reply(msg.ID, loginStartReply(msg.Params))
				send(completion(outcomeSucceeds()))
				markSignedIn(home, outcomeSucceeds())
			default:
				reply(msg.ID, loginStartReply(msg.Params))
			}
		case "account/login/cancel":
			reply(msg.ID, map[string]any{})
		case "account/logout":
			if mode == "logout-rpc-error" {
				fail(msg.ID, "logout is not available")
				continue
			}
			_ = os.Remove(filepath.Join(home, "auth.json"))
			reply(msg.ID, map[string]any{})
		default:
			fail(msg.ID, "method not found: "+msg.Method)
		}
	}
	if mode == "ignore-term" {
		// Keep running after stdin closes, so only a signal can stop it. The
		// client then has to escalate from SIGTERM to SIGKILL.
		time.Sleep(time.Hour)
	}
	return 0
}

func outcomeSucceeds() bool {
	return os.Getenv("FAKE_CODEX_OUTCOME") != "fail"
}

func logRequest(home, what string) {
	if home == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(home, "requests.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, what)
}

func signedIn(home string) bool {
	_, err := os.Stat(filepath.Join(home, "auth.json"))
	return err == nil
}

// markSignedIn writes the same file a real sign-in leaves behind.
func markSignedIn(home string, ok bool) {
	if ok {
		_ = os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"email":"tester@example.com","plan":"plus"}`), 0o600)
	}
}

func accountFor(home string) any {
	if !signedIn(home) {
		return nil
	}
	return map[string]any{"type": "chatgpt", "email": "tester@example.com", "planType": "plus"}
}

func rateLimitsFor(home string) any {
	if !signedIn(home) {
		return map[string]any{"rateLimits": map[string]any{"primary": nil, "secondary": nil}}
	}
	return map[string]any{"rateLimits": map[string]any{
		"primary":   map[string]any{"usedPercent": 96, "windowDurationMins": 300, "resetsAt": time.Now().Add(3 * time.Hour).Unix()},
		"secondary": map[string]any{"usedPercent": 46, "windowDurationMins": 10080, "resetsAt": time.Now().Add(72 * time.Hour).Unix()},
		"credits":   map[string]any{"hasCredits": true, "unlimited": false, "balance": "58726.9964"},
	}}
}

// modelPage returns two pages. The first carries a cursor, the second ends
// the list, so the client has to follow the cursor.
func modelPage(params json.RawMessage) any {
	var p struct {
		Cursor string `json:"cursor"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Cursor == "" {
		return map[string]any{
			"data": []any{
				map[string]any{"id": "gpt-6.1-sol", "model": "gpt-6.1-sol", "displayName": "GPT-6.1-Sol", "hidden": false},
			},
			"nextCursor": "page-2",
		}
	}
	return map[string]any{
		"data": []any{
			map[string]any{"id": "gpt-6.1-terra", "model": "gpt-6.1-terra", "displayName": "", "hidden": false},
		},
		"nextCursor": nil,
	}
}

func loginStartReply(params json.RawMessage) any {
	var p struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(params, &p)
	if p.Type == "chatgptDeviceCode" {
		return map[string]any{"type": p.Type, "loginId": fakeLoginID,
			"verificationUrl": "https://auth.example/device", "userCode": "ABCD-1234"}
	}
	return map[string]any{"type": p.Type, "loginId": fakeLoginID, "authUrl": "https://auth.example/login"}
}

// fakeCodex is one configured fake binary and the state it writes.
type fakeCodex struct {
	bin     string
	home    string
	pidFile string
	opened  []string // URLs the browser opener was asked to open
}

// newFakeCodex writes the fake into a temp dir, points the environment at
// it for the rest of the test, and stubs out the browser opener. signedIn
// sets the starting account state.
func newFakeCodex(t *testing.T, mode, outcome string, isSignedIn bool) *fakeCodex {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake codex uses unix signals and process groups")
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "codex")
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' -test.run='^TestCodexFakeServerProcess$' -- \"$@\"\n", self)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fakeCodex{bin: bin, home: home, pidFile: filepath.Join(dir, "pid")}
	if isSignedIn {
		markSignedIn(home, true)
	}
	t.Setenv("GO_WANT_CODEX_FAKE", "1")
	t.Setenv("FAKE_CODEX_MODE", mode)
	t.Setenv("FAKE_CODEX_OUTCOME", outcome)
	t.Setenv("FAKE_CODEX_HOME", home)
	t.Setenv("FAKE_CODEX_PIDFILE", f.pidFile)

	prevOpen := openBrowserFn
	openBrowserFn = func(u string) bool {
		f.opened = append(f.opened, u)
		return true
	}
	t.Cleanup(func() { openBrowserFn = prevOpen })

	forgetAvailable(bin)
	t.Cleanup(func() { forgetAvailable(bin) })
	return f
}

// pid returns the fake's process id, read from the pid file it wrote.
func (f *fakeCodex) pid(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(f.pidFile)
	if err != nil {
		t.Fatalf("fake codex never wrote its pid: %v", err)
	}
	var pid int
	if _, err := fmt.Sscan(string(b), &pid); err != nil {
		t.Fatalf("bad pid file %q: %v", b, err)
	}
	return pid
}

// requests returns the methods and CLI subcommands the fake received, in order.
func (f *fakeCodex) requests(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.home, "requests.log"))
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func (f *fakeCodex) hasRequest(t *testing.T, method string) bool {
	t.Helper()
	for _, r := range f.requests(t) {
		if r == method {
			return true
		}
	}
	return false
}

// waitGone polls until pid is gone, for the cases where the stop happens in
// the background.
func waitGone(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !processGone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still running %v after it should have stopped", pid, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
