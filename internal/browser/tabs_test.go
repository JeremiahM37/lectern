package browser

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const tabsPage = `<!doctype html><title>Tabs home</title>
<p>Needle one, then needle two, and a third NEEDLE.</p>
<a id="pop" href="/other" target="_blank">Open other</a>
<a id="dl" href="/file.txt">Download</a>
<button id="closeme" onclick="window.close()">Close</button>`

func tabsServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/other":
			io.WriteString(w, `<title>Other tab</title><p>other</p><button id="closeme" onclick="window.close()">Close</button>`)
		case "/file.txt":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
			io.WriteString(w, "downloaded body")
		case "/set":
			http.SetCookie(w, &http.Cookie{Name: "kept", Value: "yes", Path: "/", Expires: time.Now().Add(time.Hour)})
			io.WriteString(w, `<title>Set</title>`)
		case "/show":
			io.WriteString(w, `<title>Show</title>`)
		default:
			io.WriteString(w, tabsPage)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func startTabs(t *testing.T, opts LaunchOptions, downloads string) (*Tabs, *Process) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	proc, err := Launch(ctx, localRun, testOwner(), opts)
	if err == ErrNoBrowser {
		t.Skip("no Chromium on this machine")
	}
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = Stop(c, localRun, proc.Dir)
	})
	tabs, err := OpenTabs(ctx, localDial, proc, Viewport{}, downloads)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(tabs.Close)
	return tabs, proc
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTabsPopupsFindAndDownloads(t *testing.T) {
	srv := tabsServer(t)
	dl := t.TempDir()
	tabs, _ := startTabs(t, LaunchOptions{}, dl)
	var renamed []string
	tabs.OnDownload = func(d Download) string {
		final := filepath.Join(dl, d.Name)
		if err := os.Rename(d.Path, final); err != nil {
			return ""
		}
		renamed = append(renamed, final)
		return final
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	home, _, err := tabs.Get(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := home.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}

	// Find counts every match, case-insensitively, and steps through them.
	r, err := home.Find(ctx, "needle", false)
	if err != nil || r.Matches != 3 || r.Index != 1 || !r.Found {
		t.Fatalf("find: %+v %v", r, err)
	}
	if r, _ = home.Find(ctx, "needle", false); r.Index != 2 {
		t.Fatalf("find next: %+v", r)
	}
	if r, _ = home.Find(ctx, "needle", true); r.Index != 1 {
		t.Fatalf("find previous: %+v", r)
	}
	if r, _ = home.Find(ctx, "absent", false); r.Matches != 0 || r.Found {
		t.Fatalf("find nothing: %+v", r)
	}

	// A target=_blank link opens a tab of its own, which becomes active.
	if _, err := home.Click(ctx, Target{Selector: "#pop"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the popup tab", func() bool {
		list := tabs.List()
		return len(list) == 2 && list[1].Active && list[1].Title == "Other tab"
	})
	// A page that closes itself leaves the tab strip.
	other, otherID, _ := tabs.Get(0)
	if _, err := other.Click(ctx, Target{Selector: "#closeme"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the closed tab to go", func() bool { return len(tabs.List()) == 1 })
	if tabs.Active() == otherID {
		t.Fatal("a closed tab stayed active")
	}

	// New, select and close by id.
	id, _, err := tabs.New(ctx, srv.URL+"/other")
	if err != nil || tabs.Active() != id {
		t.Fatalf("new tab %d: %v", id, err)
	}
	first := tabs.List()[0].ID
	if err := tabs.Select(first); err != nil || tabs.Active() != first {
		t.Fatalf("select: %v", err)
	}
	if err := tabs.CloseTab(ctx, id); err != nil || len(tabs.List()) != 1 {
		t.Fatalf("close: %v %v", err, tabs.List())
	}
	if err := tabs.CloseTab(ctx, first); err != nil || len(tabs.List()) != 1 || tabs.List()[0].ID == first {
		t.Fatalf("closing the last tab must leave a fresh one: %v %v", err, tabs.List())
	}

	// A download lands in the directory, under its own name.
	b, _, _ := tabs.Get(0)
	if _, err := b.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Click(ctx, Target{Selector: "#dl"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the download", func() bool {
		d := tabs.Downloads()
		return len(d) == 1 && d[0].State == "completed" && strings.HasSuffix(d[0].Path, "report.txt")
	})
	if got, _ := os.ReadFile(filepath.Join(dl, "report.txt")); string(got) != "downloaded body" {
		t.Fatalf("downloaded file: %q", got)
	}
}

func cookieOf(t *testing.T, ctx context.Context, b *Browser, url string) string {
	t.Helper()
	if _, err := b.Navigate(ctx, url); err != nil {
		t.Fatal(err)
	}
	v, err := b.Evaluate(ctx, "document.cookie")
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(v)
}

func TestProfilesPersistAndCookiesImportOnTheMachine(t *testing.T) {
	srv := tabsServer(t)
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// A named profile keeps its cookies across browsers.
	tabs, proc := startTabs(t, LaunchOptions{Profile: "p1-work"}, "")
	b, _, _ := tabs.Get(0)
	if _, err := b.Navigate(ctx, srv.URL+"/set"); err != nil {
		t.Fatal(err)
	}
	// As the server does: ask the browser to quit, which writes the profile.
	tabs.Quit(ctx)
	if err := Stop(ctx, localRun, proc.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".lectern/browser-profiles/p1-work")); err != nil {
		t.Fatalf("the profile did not outlive its browser: %v", err)
	}
	again, _ := startTabs(t, LaunchOptions{Profile: "p1-work"}, "")
	b, _, _ = again.Get(0)
	if got := cookieOf(t, ctx, b, srv.URL+"/show"); got != "kept=yes" {
		t.Fatalf("cookie after restart: %q", got)
	}
	// Another profile, and a throwaway one, do not see it.
	other, _ := startTabs(t, LaunchOptions{Profile: "p1-other"}, "")
	ob, _, _ := other.Get(0)
	if got := cookieOf(t, ctx, ob, srv.URL+"/show"); got != "" {
		t.Fatalf("profiles leak into each other: %q", got)
	}
	if _, err := Launch(ctx, localRun, testOwner(), LaunchOptions{Profile: "p1-work"}); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("a profile in use was opened twice: %v", err)
	}
	if _, err := Launch(ctx, localRun, testOwner(), LaunchOptions{Profile: "../escape"}); err == nil {
		t.Fatal("a profile name escaped the profiles directory")
	}

	// Import from a cookies file, staged in the browser's own directory and
	// deleted after reading.
	fresh, fproc := startTabs(t, LaunchOptions{}, "")
	host := strings.TrimPrefix(srv.URL, "http://")
	host = host[:strings.Index(host, ":")]
	file := filepath.Join(fproc.Dir, "import.cookies")
	txt := fmt.Sprintf("# Netscape HTTP Cookie File\n%s\tFALSE\t/\tFALSE\t%d\tfromfile\tone\n#HttpOnly_other.example\tTRUE\t/\tFALSE\t0\tfiltered\tout\n",
		host, time.Now().Add(time.Hour).Unix())
	if err := os.WriteFile(file, []byte(txt), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ImportCookies(ctx, localRun, fproc, "file", file, []string{host})
	if err != nil || res.Imported != 1 {
		t.Fatalf("file import: %+v %v", res, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("the cookies file was left behind")
	}
	fb, _, _ := fresh.Get(0)
	if got := cookieOf(t, ctx, fb, srv.URL+"/show"); got != "fromfile=one" {
		t.Fatalf("after file import: %q", got)
	}
	if _, err := ImportCookies(ctx, localRun, fproc, "file", "/etc/passwd", nil); err == nil {
		t.Fatal("a file outside the browser's directory was accepted")
	}

	// Import from a real Chromium profile on this machine: made by running
	// Chromium on it, so its cookies are encrypted the way Chromium does it.
	// It is made the way the product runs a browser, and asked to quit, which
	// is when Chromium writes cookies to disk: no timing, no --dump-dom, and
	// nothing that waits on a desktop session the machine may not have.
	srcTabs, srcProc := startTabs(t, LaunchOptions{Profile: "source-chrome"}, "")
	sb, _, _ := srcTabs.Get(0)
	if _, err := sb.Navigate(ctx, srv.URL+"/set"); err != nil {
		t.Fatal(err)
	}
	srcTabs.Quit(ctx)
	if err := Stop(ctx, localRun, srcProc.Dir); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(os.Getenv("HOME"), ".lectern/browser-profiles/source-chrome")
	res, err = ImportCookies(ctx, localRun, fproc, "chrome", filepath.Join(source, "Default"), nil)
	if err != nil || res.Imported < 1 {
		t.Fatalf("chrome import: %+v %v", res, err)
	}
	if got := cookieOf(t, ctx, fb, srv.URL+"/show"); !strings.Contains(got, "kept=yes") {
		t.Fatalf("after chrome import: %q", got)
	}
}
