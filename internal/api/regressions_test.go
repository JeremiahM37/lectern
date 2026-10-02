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

// One version string, reported by the API and shown in the UI. The README
// need not state a version at all (a rewrite that dropped its badge is what
// turned CI red), but a version it does state must be the current one.
func TestVersionIsDeclaredOnce(t *testing.T) {
	h := newHarness(t)
	if got := h.get("/api/health").str("version"); got != version.Version {
		t.Fatalf("/api/health reports %q, the package says %q", got, version.Version)
	}
	readme := repoFile(t, "README.md")
	for _, claim := range readmeVersionClaims(readme) {
		if claim != version.Version {
			t.Errorf("README says version %s; the package says %s", claim, version.Version)
		}
	}
}

// readmeVersionClaims finds the version a README's shields.io badge states
// as Lectern's current one. Prose about past releases ("since 2.5.0") and
// install examples that pin one (--version v2.4.0) are not such claims.
func readmeVersionClaims(readme string) []string {
	var out []string
	re := regexp.MustCompile(`badge/version-v?(\d+\.\d+\.\d+(?:--[0-9A-Za-z.]+)*)-[0-9A-Fa-f]{3,8}\b`)
	for _, m := range re.FindAllStringSubmatch(readme, -1) {
		// shields.io writes a literal dash as "--".
		out = append(out, strings.ReplaceAll(m[1], "--", "-"))
	}
	return out
}

func TestReadmeVersionClaims(t *testing.T) {
	got := readmeVersionClaims("![version](https://img.shields.io/badge/version-2.6.2-8b5cf6)\n" +
		"![version](https://img.shields.io/badge/version-2.7.0--rc.1-8b5cf6)\n" +
		"Since Lectern 2.5.0 it pairs phones.\n`lectern update --version v2.4.0`\n")
	if strings.Join(got, ",") != "2.6.2,2.7.0-rc.1" {
		t.Fatalf("claims: %v", got)
	}
	if got := readmeVersionClaims("No version here.\n"); len(got) != 0 {
		t.Fatalf("claims: %v", got)
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
