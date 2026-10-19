package codex

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// nextEvent reads one login event or fails the test if none comes in time.
func nextLoginEvent(t *testing.T, ch <-chan LoginEvent) (LoginEvent, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(10 * time.Second):
		t.Fatal("no login event within 10s")
		return LoginEvent{}, false
	}
}

// drain collects the rest of the events, to check the channel closes.
func drainLoginEvents(t *testing.T, ch <-chan LoginEvent) []LoginEvent {
	t.Helper()
	var evs []LoginEvent
	for {
		ev, ok := nextLoginEvent(t, ch)
		if !ok {
			return evs
		}
		evs = append(evs, ev)
	}
}

func TestResolve(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	got, err := Resolve(f.bin)
	if err != nil || got != f.bin {
		t.Fatalf("Resolve(path) = %q, %v", got, err)
	}

	t.Setenv("PATH", t.TempDir())
	_, err = Resolve("")
	if err == nil || !strings.Contains(err.Error(), "npm i -g @openai/codex") {
		t.Fatalf("Resolve on an empty PATH: err = %v, want the install hint", err)
	}
}

func TestWindowName(t *testing.T) {
	cases := map[int64]string{
		300:   "5h",
		10080: "weekly",
		90:    "90m",
		120:   "2h",
		1440:  "1d",
		2880:  "2d",
		0:     "window",
	}
	for mins, want := range cases {
		if got := windowName(mins); got != want {
			t.Errorf("windowName(%d) = %q, want %q", mins, got, want)
		}
	}
}

func TestReadStatusSignedIn(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	st := ReadStatus(context.Background(), f.bin)
	if st.Err != nil {
		t.Fatalf("Err = %v", st.Err)
	}
	if !st.Installed || st.Binary != f.bin || st.Version != "codex-cli 0.163.0" {
		t.Fatalf("install fields = %+v", st)
	}
	if !st.LoggedIn || st.Email != "tester@example.com" || st.Plan != "plus" || st.AuthMode != "chatgpt" {
		t.Fatalf("account fields = %+v", st)
	}
	if len(st.Limits) != 2 {
		t.Fatalf("limits = %+v", st.Limits)
	}
	if st.Limits[0].Name != "5h" || st.Limits[0].UsedPercent != 96 || st.Limits[0].Window != 5*time.Hour {
		t.Errorf("primary limit = %+v", st.Limits[0])
	}
	if st.Limits[0].ResetsAt.IsZero() || st.Limits[0].ResetsAt.Before(time.Now()) {
		t.Errorf("primary reset = %v, want a time in the future", st.Limits[0].ResetsAt)
	}
	if st.Limits[1].Name != "weekly" || st.Limits[1].UsedPercent != 46 {
		t.Errorf("secondary limit = %+v", st.Limits[1])
	}
	if st.Credits == nil || !st.Credits.HasCredits || st.Credits.Unlimited || st.Credits.Balance != "58727.00" {
		t.Errorf("credits = %+v", st.Credits)
	}
}

func TestReadStatusNotSignedIn(t *testing.T) {
	f := newFakeCodex(t, "ok", "", false)
	st := ReadStatus(context.Background(), f.bin)
	if st.Err != nil {
		t.Fatalf("Err = %v; a signed-out account is not an error", st.Err)
	}
	if st.LoggedIn || st.Email != "" {
		t.Fatalf("status = %+v", st)
	}
	if !st.Installed {
		t.Fatal("Installed = false for a working binary")
	}
}

func TestReadStatusFallsBackToLoginStatus(t *testing.T) {
	f := newFakeCodex(t, "rpc-error", "", true)
	st := ReadStatus(context.Background(), f.bin)
	if !st.LoggedIn {
		t.Fatalf("LoggedIn = false; the CLI's login status says signed in: %+v", st)
	}
	if st.Err == nil || !strings.Contains(st.Err.Error(), "boom") {
		t.Fatalf("Err = %v, want the app-server failure kept", st.Err)
	}
	if st.Version == "" {
		t.Fatal("Version was not filled in on the fallback path")
	}
	if !f.hasRequest(t, "login status") {
		t.Fatalf("login status was not run; requests = %v", f.requests(t))
	}
}

func TestReadStatusMissingBinary(t *testing.T) {
	st := ReadStatus(context.Background(), filepath.Join(t.TempDir(), "codex"))
	if st.Installed || st.Err == nil {
		t.Fatalf("status = %+v, want not installed with an error", st)
	}
}

func TestReadStatusBinaryThatDoesNotRun(t *testing.T) {
	if _, err := Resolve("false"); err != nil {
		t.Skip("no false binary on PATH")
	}
	st := ReadStatus(context.Background(), "false")
	if st.Installed || st.Err == nil {
		t.Fatalf("status = %+v, want a failed version run reported", st)
	}
}

func TestLoginDeviceCompletes(t *testing.T) {
	f := newFakeCodex(t, "device-late", "success", false)
	ch, err := Login(context.Background(), f.bin, LoginDevice)
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := nextLoginEvent(t, ch)
	if ev.Kind != LoginStarted || ev.Mode != LoginDevice || ev.URL != "https://auth.example/device" || ev.Code != "ABCD-1234" {
		t.Fatalf("first event = %+v", ev)
	}
	if ev.Opened || len(f.opened) != 0 {
		t.Fatal("a device login must not open a browser")
	}
	ev, _ = nextLoginEvent(t, ch)
	if ev.Kind != LoginDone || ev.Status == nil || !ev.Status.LoggedIn || ev.Status.Email != "tester@example.com" {
		t.Fatalf("done event = %+v", ev)
	}
	if !strings.Contains(ev.Message, "tester@example.com") {
		t.Fatalf("message = %q", ev.Message)
	}
	if rest := drainLoginEvents(t, ch); len(rest) != 0 {
		t.Fatalf("events after Done: %+v", rest)
	}
}

func TestLoginCompletionThatArrivesEarlyIsNotLost(t *testing.T) {
	f := newFakeCodex(t, "device-early", "success", false)
	ch, err := Login(context.Background(), f.bin, LoginDevice)
	if err != nil {
		t.Fatal(err)
	}
	evs := drainLoginEvents(t, ch)
	if len(evs) < 2 || evs[len(evs)-1].Kind != LoginDone {
		t.Fatalf("events = %+v, want Started then Done", evs)
	}
}

func TestLoginDeviceFails(t *testing.T) {
	f := newFakeCodex(t, "device-late", "fail", false)
	ch, err := Login(context.Background(), f.bin, LoginDevice)
	if err != nil {
		t.Fatal(err)
	}
	evs := drainLoginEvents(t, ch)
	last := evs[len(evs)-1]
	if last.Kind != LoginFailed || last.Err == nil || !strings.Contains(last.Err.Error(), "access denied") {
		t.Fatalf("last event = %+v, want Failed with the server's error", last)
	}
}

func TestLoginCancelSendsCancelAndStopsChild(t *testing.T) {
	f := newFakeCodex(t, "device-hang", "", false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Login(ctx, f.bin, LoginDevice)
	if err != nil {
		t.Fatal(err)
	}
	if ev, _ := nextLoginEvent(t, ch); ev.Kind != LoginStarted {
		t.Fatalf("first event = %+v", ev)
	}
	pid := f.pid(t)
	cancel()
	evs := drainLoginEvents(t, ch)
	last := evs[len(evs)-1]
	if last.Kind != LoginFailed || last.Err == nil || !strings.Contains(last.Err.Error(), "cancelled") {
		t.Fatalf("last event = %+v, want Failed saying cancelled", last)
	}
	if !f.hasRequest(t, "account/login/cancel") {
		t.Fatalf("account/login/cancel was not sent; requests = %v", f.requests(t))
	}
	waitGone(t, pid, 5*time.Second)
}

func TestLoginAlreadySignedInSkipsTheFlow(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	ch, err := Login(context.Background(), f.bin, LoginDevice)
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := nextLoginEvent(t, ch)
	if ev.Kind != LoginDone || ev.Status == nil || !ev.Status.LoggedIn {
		t.Fatalf("event = %+v, want Done with the account", ev)
	}
	if !ev.AlreadySignedIn || !strings.Contains(ev.Message, "Already signed in") {
		t.Fatalf("message = %q", ev.Message)
	}
	if f.hasRequest(t, "account/login/start") {
		t.Fatal("a new sign-in was started for an account that is already signed in")
	}
	if rest := drainLoginEvents(t, ch); len(rest) != 0 {
		t.Fatalf("events after Done: %+v", rest)
	}
}

func TestLoginBrowserOpensURL(t *testing.T) {
	f := newFakeCodex(t, "browser-late", "success", false)
	ch, err := Login(context.Background(), f.bin, LoginBrowser)
	if err != nil {
		t.Fatal(err)
	}
	ev, _ := nextLoginEvent(t, ch)
	if ev.Kind != LoginStarted || ev.Mode != LoginBrowser || ev.URL != "https://auth.example/login" || ev.Code != "" {
		t.Fatalf("started = %+v", ev)
	}
	if !ev.Opened || len(f.opened) != 1 || f.opened[0] != ev.URL {
		t.Fatalf("opened = %v, want the sign-in URL opened once", f.opened)
	}
	if last := drainLoginEvents(t, ch); last[len(last)-1].Kind != LoginDone {
		t.Fatalf("events = %+v, want Done last", last)
	}
}

func TestLoginAutoChoosesFlowFromEnvironment(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS always counts as having a display")
	}
	cases := []struct {
		name    string
		display string
		wayland string
		ssh     string
		sshTTY  string
		want    LoginMode
	}{
		{"local display", ":0", "", "", "", LoginBrowser},
		{"wayland", "", "wayland-0", "", "", LoginBrowser},
		{"no display", "", "", "", "", LoginDevice},
		{"over ssh connection", ":0", "", "1.2.3.4 22 5.6.7.8 22", "", LoginDevice},
		{"over ssh tty", ":0", "", "", "/dev/pts/3", LoginDevice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DISPLAY", tc.display)
			t.Setenv("WAYLAND_DISPLAY", tc.wayland)
			t.Setenv("SSH_CONNECTION", tc.ssh)
			t.Setenv("SSH_TTY", tc.sshTTY)
			if got := resolveMode(LoginAuto); got != tc.want {
				t.Fatalf("resolveMode(auto) = %v, want %v", got, tc.want)
			}
		})
	}
	if got := resolveMode(LoginDevice); got != LoginDevice {
		t.Fatalf("an explicit mode must be kept, got %v", got)
	}
}

func TestLogoutSignsOut(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	if err := Logout(context.Background(), f.bin); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if signedIn(f.home) {
		t.Fatal("the sign-in is still there after Logout")
	}
	if !f.hasRequest(t, "account/logout") {
		t.Fatalf("account/logout was not sent; requests = %v", f.requests(t))
	}
}

func TestLogoutFallsBackToCLI(t *testing.T) {
	f := newFakeCodex(t, "logout-rpc-error", "", true)
	if err := Logout(context.Background(), f.bin); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if signedIn(f.home) {
		t.Fatal("the CLI fallback did not sign out")
	}
	if !f.hasRequest(t, "logout") {
		t.Fatalf("the CLI logout was not run; requests = %v", f.requests(t))
	}
}

func TestListModelsFollowsCursor(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	models, err := ListModels(context.Background(), f.bin)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want both pages", models)
	}
	if models[0].ID != "gpt-6.1-sol" || models[0].Name != "GPT-6.1-Sol" || !models[0].Tools {
		t.Errorf("first = %+v", models[0])
	}
	if models[1].ID != "gpt-6.1-terra" || models[1].Name != "gpt-6.1-terra" {
		t.Errorf("second = %+v; an empty displayName falls back to the id", models[1])
	}
}

func TestAvailableIsCachedForThirtySeconds(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	base := time.Now()
	prevClock := clock
	clock = func() time.Time { return base }
	t.Cleanup(func() { clock = prevClock })

	if !Available(context.Background(), f.bin) {
		t.Fatal("Available = false for a signed-in binary")
	}
	// Sign out behind the cache's back. The answer stays until the TTL ends.
	_ = os.Remove(filepath.Join(f.home, "auth.json"))
	if !Available(context.Background(), f.bin) {
		t.Fatal("Available was not served from the cache")
	}
	clock = func() time.Time { return base.Add(availableTTL + time.Second) }
	if Available(context.Background(), f.bin) {
		t.Fatal("Available was still cached after the TTL")
	}
}

func TestAvailableFalseWhenNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if Available(context.Background(), "") {
		t.Fatal("Available = true with no codex on PATH")
	}
}

func TestSignInForgetsCachedAvailability(t *testing.T) {
	f := newFakeCodex(t, "device-late", "success", false)
	if Available(context.Background(), f.bin) {
		t.Fatal("Available = true before signing in")
	}
	ch, err := Login(context.Background(), f.bin, LoginDevice)
	if err != nil {
		t.Fatal(err)
	}
	drainLoginEvents(t, ch)
	if !Available(context.Background(), f.bin) {
		t.Fatal("Available stayed false after a successful sign-in; the cache was not cleared")
	}
}

func TestLogoutForgetsCachedAvailability(t *testing.T) {
	f := newFakeCodex(t, "ok", "", true)
	if !Available(context.Background(), f.bin) {
		t.Fatal("Available = false for a signed-in binary")
	}
	if err := Logout(context.Background(), f.bin); err != nil {
		t.Fatal(err)
	}
	if Available(context.Background(), f.bin) {
		t.Fatal("Available stayed true after Logout")
	}
}
