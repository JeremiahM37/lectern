package desktop

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func sh(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-c", script).Output()
	return string(out), err
}

func needs(t *testing.T) {
	t.Helper()
	for _, b := range []string{"Xvfb", "x11vnc", "websockify", "python3"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("needs %s to start a real display", b)
		}
	}
	if _, err := os.Stat("/usr/share/novnc/vnc.html"); err != nil {
		t.Skip("needs novnc")
	}
}

func TestADesktopStartsServesItsWebClientAndStopsCleanly(t *testing.T) {
	needs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	d, err := Start(ctx, sh, "testowner0001", 1280, 800)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = Stop(context.Background(), sh, d.Dir)
		}
	}()
	// Start must return while the display is still running. If a background
	// process kept the script's stdout, this would not return until it died.
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("start took %s", took)
	}
	if d.Display < 90 || d.Port == 0 {
		t.Fatalf("desktop: %+v", d)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/vnc.html", d.Port))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "noVNC") {
		t.Fatalf("web client: %d", resp.StatusCode)
	}
	// Something can really draw on it.
	if out, err := sh(ctx, fmt.Sprintf("DISPLAY=:%d xdpyinfo 2>/dev/null | grep -c dimensions || true", d.Display)); err == nil && strings.TrimSpace(out) == "0" {
		if _, lookErr := exec.LookPath("xdpyinfo"); lookErr == nil {
			t.Errorf("the display does not answer")
		}
	}
	if _, err := os.Stat(fmt.Sprintf("/tmp/.X11-unix/X%d", d.Display)); err != nil {
		t.Errorf("no X socket: %v", err)
	}
	pids, _ := sh(ctx, "cat "+shellQuote(d.Dir)+"/*.pid")
	if err := Stop(ctx, sh, d.Dir); err != nil {
		t.Fatal(err)
	}
	stopped = true
	if _, err := os.Stat(d.Dir); !os.IsNotExist(err) {
		t.Errorf("state directory survived: %v", err)
	}
	for _, pid := range strings.Fields(pids) {
		if err := exec.Command("kill", "-0", pid).Run(); err == nil {
			t.Errorf("process %s outlived its desktop", pid)
		}
	}
	if _, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/vnc.html", d.Port)); err == nil {
		t.Error("the web client still answers after stop")
	}
}

func TestANewServerReapsDesktopsAnOldOneLeftBehind(t *testing.T) {
	needs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	abandoned, err := Start(ctx, sh, "oldserver0001", 1024, 768)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), sh, abandoned.Dir)
	mine, err := Start(ctx, sh, "newserver0002", 1024, 768)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), sh, mine.Dir)
	if _, err := os.Stat(abandoned.Dir); !os.IsNotExist(err) {
		t.Errorf("the abandoned desktop was not reaped: %v", err)
	}
	// A second desktop from the same server is a sibling, not an orphan.
	sibling, err := Start(ctx, sh, "newserver0002", 1024, 768)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), sh, sibling.Dir)
	if _, err := os.Stat(mine.Dir); err != nil {
		t.Errorf("a live sibling was reaped: %v", err)
	}
	if sibling.Display == mine.Display || sibling.Port == mine.Port {
		t.Errorf("two desktops share a display or port: %+v %+v", mine, sibling)
	}
}

func TestStopAndBrowserRefuseAnythingThatIsNotADesktop(t *testing.T) {
	never := func(context.Context, string) (string, error) {
		t.Fatal("a script ran for an invalid request")
		return "", nil
	}
	for _, dir := range []string{"", "/", "/tmp", "/tmp/lectern-live-abc", "/tmp/lectern-live-abcdef/../..", "/home/admin", "/tmp/lectern-live-abcde$"} {
		if err := Stop(context.Background(), never, dir); err == nil {
			t.Errorf("Stop(%q) must be refused", dir)
		}
	}
	d := &Desktop{Dir: "/tmp/lectern-live-abcdef", Display: 99}
	for _, address := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "localhost:3000", "http://"} {
		if _, err := OpenBrowser(context.Background(), never, d, address); err == nil {
			t.Errorf("OpenBrowser(%q) must be refused", address)
		}
	}
	if _, err := Start(context.Background(), never, "bad owner;rm", 0, 0); err == nil {
		t.Error("an unsafe owner id must be refused")
	}
}

func TestAMachineWithoutTheToolsSaysWhatToInstall(t *testing.T) {
	bare := func(ctx context.Context, script string) (string, error) {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
		cmd.Env = []string{"PATH=/nonexistent"}
		out, _ := cmd.Output()
		return string(out), nil
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	_, err := Start(context.Background(), bare, "testowner0001", 1280, 800)
	missing, ok := err.(*MissingError)
	if !ok || len(missing.Tools) == 0 || !strings.Contains(err.Error(), "Xvfb") {
		t.Fatalf("want a MissingError naming the tools, got %v", err)
	}
}
