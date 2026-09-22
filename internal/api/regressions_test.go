package api_test

// Tripwires on the things that boot fine and break silently.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/version"
	"github.com/JeremiahM37/lectern/v2/web"
)

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(raw)
}

// The image must build and ship the binary; a Dockerfile that merely runs is not
// evidence it ships a working one.
func TestDockerfileBuildsAndShipsTheBinary(t *testing.T) {
	df := repoFile(t, "deploy/Dockerfile")
	for _, want := range []string{"go build", "./cmd/lectern", "COPY --from=build"} {
		if !strings.Contains(df, want) {
			t.Errorf("Dockerfile is missing %q", want)
		}
	}
	// CGO off keeps the pure-Go sqlite driver working on a slim base image
	if !strings.Contains(df, "CGO_ENABLED=0") {
		t.Error("the build must stay CGO-free or the slim image cannot run it")
	}
}

// The runtime image needs the tools a dispatch actually shells out to.
func TestDockerfileInstallsRuntimeTools(t *testing.T) {
	df := repoFile(t, "deploy/Dockerfile")
	for _, tool := range []string{"git", "tmux", "openssh-client", "ttyd"} {
		if !strings.Contains(df, tool) {
			t.Errorf("Dockerfile is missing runtime tool %q", tool)
		}
	}
}

func TestDockerfileHasHealthcheck(t *testing.T) {
	df := repoFile(t, "deploy/Dockerfile")
	if !strings.Contains(df, "HEALTHCHECK") || !strings.Contains(df, "/api/health") {
		t.Fatal("orchestrators need a liveness probe")
	}
}

// The systemd unit runs the compiled binary, not a source tree.
func TestSystemdUnitRunsTheBinary(t *testing.T) {
	unit := repoFile(t, "deploy/lectern.service")
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/lectern") {
		t.Error("the unit should exec the installed binary")
	}
	// agent binaries in ~/.local/bin are why a probe reports a missing codex
	if !strings.Contains(unit, "Environment=PATH=") {
		t.Error("the unit must set an explicit PATH")
	}
}

// One version string, reported by the API and shown in the UI.
func TestVersionIsDeclaredOnce(t *testing.T) {
	h := newHarness(t)
	if got := h.get("/api/health").str("version"); got != version.Version {
		t.Fatalf("/api/health reports %q, the package says %q", got, version.Version)
	}
	if !strings.Contains(repoFile(t, "README.md"), version.Version) {
		t.Errorf("README does not mention the current version %s", version.Version)
	}
}

// The PWA is embedded, so a missing asset is a build-time fact, not a 404 that
// only a phone discovers.
func TestWebAssetsAreEmbedded(t *testing.T) {
	if len(web.IndexHTML) == 0 {
		t.Fatal("index.html was not embedded")
	}
	for _, name := range []string{
		"static/sw.js", "static/icon.svg",
		"static/manifest.webmanifest", "static/fonts.css",
		"static/fonts/inter-latin.woff2",
	} {
		if _, err := web.Assets.ReadFile(name); err != nil {
			t.Errorf("%s is not embedded: %v", name, err)
		}
	}
}

// Verify the actual built shell references assets present in the embedded binary.
// Worker network/cache semantics run in e2e/test_react_worker.py in a browser.
func TestReactShellAssetsAreEmbedded(t *testing.T) {
	matches := regexp.MustCompile(`(?:src|href)="(/react/assets/[^"\s]+)"`).FindAllStringSubmatch(string(web.IndexHTML), -1)
	if len(matches) < 2 {
		t.Fatal("React shell must reference bundled JavaScript and CSS")
	}
	for _, match := range matches {
		if _, err := web.Assets.ReadFile("static" + match[1]); err != nil {
			t.Errorf("built shell asset %s missing: %v", match[1], err)
		}
	}
	sw, err := web.Assets.ReadFile("static/sw.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sw), "lectern-react-") {
		t.Fatal("embedded worker is not the content-versioned React build")
	}
}

// The agent-side kit is embedded too — there is no hooks/ directory to deploy.
func TestAgentHooksAreEmbedded(t *testing.T) {
	h := newHarness(t)
	h.run(h.seededProjectID(), "hooks", "x", nil)
	if !strings.Contains(string(h.staged("/.lectern/lec.py")), "add-task") {
		t.Error("the task-filing kit was not staged")
	}
	if !strings.Contains(string(h.staged("/.lectern/env")), "ADK_TOKEN=") {
		t.Error("the per-attempt token was not staged")
	}
}
