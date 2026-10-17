package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

// newAPIExportApp is a saved session with its directory made, so a test can
// write a request dump into it the way the agent hook would.
func newAPIExportApp(t *testing.T) (*scribeBackend, *App) {
	t.Helper()
	isolatedHome(t)
	root := t.TempDir()
	a := &App{Workdir: root, Store: session.NewStore(root), Meta: session.Meta{ID: "s1", Title: "Fix the export"}}
	if err := os.MkdirAll(a.Store.Dir("s1"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &scribeBackend{app: a}, a
}

func TestExportKindKnowsAPI(t *testing.T) {
	cases := map[string]string{
		"api":        "api",
		" API ":      "api",
		"json":       "json",
		"all":        "diagnostic",
		"diagnostic": "diagnostic",
		"":           "markdown",
		"md":         "markdown",
	}
	for in, want := range cases {
		if got := exportKind(in); got != want {
			t.Errorf("exportKind(%q) = %q, want %q", in, got, want)
		}
	}
}

// With no dump on disk the export must not write a file or claim success: it
// says there is nothing yet and how to turn dumping on.
func TestAPIExportWithoutDumpSaysHowToTurnItOn(t *testing.T) {
	b, a := newAPIExportApp(t)
	t.Setenv(requestDumpEnv, "")

	path, err := b.Export("api")
	if err == nil {
		t.Fatalf("export with no dump returned %q, want an error", path)
	}
	for _, want := range []string{"/session export api on", "SCRIBE_REQUEST_DUMP=1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if _, statErr := os.Stat(filepath.Join(a.Workdir, ".scribe", "api-export")); !os.IsNotExist(statErr) {
		t.Errorf("a failed export must not create the export directory: %v", statErr)
	}
}

// A dump file that exists but holds no complete request is the same case as
// no dump at all: there is nothing to export.
func TestAPIExportOfAnEmptyDumpSaysSo(t *testing.T) {
	b, a := newAPIExportApp(t)
	if err := os.WriteFile(a.dumpPath(), []byte("\n  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Export("api"); err == nil || !strings.Contains(err.Error(), "no API request dump yet") {
		t.Fatalf("empty dump error = %v, want the no-dump message", err)
	}
}

// The export is built from the dump: one step per request in order, a
// truncated line counted and dropped, and a secret that never went through
// the request hook removed by the export's own redaction.
func TestAPIExportIsBuiltFromTheDumpAndRedacted(t *testing.T) {
	b, a := newAPIExportApp(t)
	key := "sk-ant-api03-ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdef"
	dump := strings.Join([]string{
		`{"step":1,"model":"fake/a","request":{"messages":[{"role":"user","content":"hi"}]}}`,
		`{"step":2,"model":"fake/a","request":{"messages":[{"role":"user","content":"leaked ` + key + `"}]}}`,
		`{"step":3,"model":"fake/a","request":{"messa`, // cut off mid-write
	}, "\n") + "\n"
	if err := os.WriteFile(a.dumpPath(), []byte(dump), 0o600); err != nil {
		t.Fatal(err)
	}

	path, err := b.Export("api")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	wantDir := filepath.Join(a.Workdir, ".scribe", "api-export")
	if filepath.Dir(path) != wantDir || !strings.HasSuffix(path, ".json") {
		t.Fatalf("export path = %q, want a .json file in %s", path, wantDir)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), key) {
		t.Fatal("a secret reached the API export unredacted")
	}
	var doc struct {
		SessionID    string `json:"session_id"`
		SkippedLines int    `json:"skipped_lines"`
		Steps        []struct {
			Step  int    `json:"step"`
			Model string `json:"model"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("export is not JSON: %v\n%s", err, raw)
	}
	if doc.SessionID != "s1" || doc.SkippedLines != 1 || len(doc.Steps) != 2 {
		t.Fatalf("doc = %+v, want session s1, 1 skipped line, 2 steps", doc)
	}
	if doc.Steps[0].Step != 1 || doc.Steps[1].Step != 2 || doc.Steps[0].Model != "fake/a" {
		t.Fatalf("steps out of order or wrong: %+v", doc.Steps)
	}
}

// An ephemeral session has no dump and no directory, so the export refuses
// the same way every other export does.
func TestAPIExportRefusesAnEphemeralSession(t *testing.T) {
	b, a := newAPIExportApp(t)
	a.opts.Ephemeral = true
	if _, err := b.Export("api"); err == nil || !strings.Contains(err.Error(), "not saved") {
		t.Fatalf("ephemeral export error = %v, want the not-saved message", err)
	}
}
