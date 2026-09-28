package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func f64(v float64) *float64 { return &v }

func TestMatchLaunchedConversationNeverGuesses(t *testing.T) {
	launched := 1000.0
	convs := []CatalogConversation{
		{ID: "old", Created: f64(900)},            // before this launch
		{ID: "mine", Created: f64(1005)},          // created by it
		{ID: "undated"},                           // cannot be placed in time
		{ID: "slack", Created: f64(launched - 5)}, // within the slack window
		{ID: "someone-elses", Created: f64(1010)}, // bound to another row
	}
	taken := map[string]bool{"someone-elses": true}
	if id, amb := MatchLaunchedConversation(convs[:3], launched, taken, nil); id != "mine" || amb {
		t.Fatalf("got %q ambiguous=%v", id, amb)
	}
	if id, amb := MatchLaunchedConversation(convs, launched, taken, nil); id != "" || !amb {
		t.Fatalf("two new conversations must be ambiguous, got %q %v", id, amb)
	}
	if id, _ := MatchLaunchedConversation(convs[:1], launched, nil, nil); id != "" {
		t.Fatalf("an older conversation was bound: %q", id)
	}
	if id, _ := MatchLaunchedConversation(convs[4:], launched, taken, nil); id != "" {
		t.Fatalf("a conversation another session holds was bound: %q", id)
	}
	// A fork whose CLI copies the parent's creation time (Qwen Code) is new
	// by the pre-launch snapshot, and only once it has been written since.
	fork := []CatalogConversation{
		{ID: "parent", Created: f64(900), Modified: f64(1003)},
		{ID: "fork", Created: f64(900), Modified: f64(1004)},
		{ID: "idle", Created: f64(800), Modified: f64(850)},
	}
	existing := map[string]bool{"parent": true}
	if id, amb := MatchLaunchedConversation(fork, launched, nil, existing); id != "fork" || amb {
		t.Fatalf("fork: got %q %v", id, amb)
	}
	if id, _ := MatchLaunchedConversation(fork, launched, nil, nil); id != "" {
		t.Fatalf("without a snapshot an old creation time must not be bound: %q", id)
	}
}

func TestAssignedSessionIDsOnlyNameNewConversations(t *testing.T) {
	qwen, _ := FindCatalogPreset("qwen")
	openclaude, _ := FindCatalogPreset("openclaude")
	for _, tc := range []struct {
		spec             Spec
		resume           bool
		resumeID, forkID string
		want             bool
	}{
		{qwen.Spec, false, "", "", true},
		{qwen.Spec, true, "", "", false},   // resume-last keeps its own id
		{qwen.Spec, false, "x", "", false}, // exact resume is already bound
		{qwen.Spec, false, "", "x", false}, // qwen rejects --session-id on a fork
		{openclaude.Spec, false, "", "x", true},
		{Spec{Name: "plain"}, false, "", "", false},
	} {
		if got := tc.spec.AssignsSessionID(tc.resume, tc.resumeID, tc.forkID); got != tc.want {
			t.Errorf("%s resume=%v id=%q fork=%q: got %v", tc.spec.Name, tc.resume, tc.resumeID, tc.forkID, got)
		}
	}
	got := qwen.invocation(Start{Workdir: "/w", SessionID: "11111111-2222-4333-8444-555555555555", Prompt: "fix it"})
	if got != "qwen --session-id 11111111-2222-4333-8444-555555555555 -i 'fix it'" {
		t.Errorf("qwen: %s", got)
	}
	got = openclaude.invocation(Start{Workdir: "/w", ForkID: "src", SessionID: "new"})
	if got != "openclaude --resume src --fork-session --session-id new" {
		t.Errorf("openclaude fork: %s", got)
	}
	grok, _ := FindCatalogPreset("grok")
	got = grok.invocation(Start{Workdir: "/w", SessionID: "u", Prompt: "fix it"})
	if got != "grok --session-id u -- 'fix it'" {
		t.Errorf("grok: %s", got)
	}
	if id := newConversationID(); len(id) != 36 || id[14] != '4' || strings.Count(id, "-") != 4 {
		t.Errorf("not a v4 UUID: %q", id)
	}
}

func TestSessionsSpecValidation(t *testing.T) {
	for raw, wantErr := range map[string]bool{
		`[{"name":"a","command":"a","sessions":{"command":"a list","id":"id","created":"c"}}]`:                          false,
		`[{"name":"a","command":"a","sessions":{"files":["~/x/*.json"],"id":"@stem","created":"c"}}]`:                   false,
		`[{"name":"a","command":"a","sessions":{"sqlite":["~/s.db"],"query":"SELECT 1","id":"id","created":"c"}}]`:      false,
		`[{"name":"a","command":"a","sessions":{"sqlite":["~/s.db"],"query":"DELETE FROM s","id":"id","created":"c"}}]`: true,
		`[{"name":"a","command":"a","sessions":{"id":"id","created":"c"}}]`:                                             true,
		`[{"name":"a","command":"a","sessions":{"command":"x","files":["y"],"id":"id","created":"c"}}]`:                 true,
		`[{"name":"a","command":"a","sessions":{"command":"x","created":"c"}}]`:                                         true,
		`[{"name":"a","command":"a","session_id_args":["--session-id","{id}"]}]`:                                        false,
		`[{"name":"a","command":"a","session_id_args":["--resume={id}"]}]`:                                              false,
		`[{"name":"a","command":"a","session_id_args":["--session-id"]}]`:                                               true,
		`[{"name":"a","command":"a","fork_session_id":true}]`:                                                           true,
	} {
		if err := ValidateSpecs(raw); (err != nil) != wantErr {
			t.Errorf("%s: err=%v want error %v", raw, err, wantErr)
		}
	}
}

// runReader executes the real target-side reader locally.
func runReader(t *testing.T, spec Spec, workdir, cid string) map[string]json.RawMessage {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	cmd, err := CatalogConversationsCommand(nil, spec, workdir, cid)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("bash", "-c", cmd).Output()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("reader output %q: %v", out, err)
	}
	return doc
}

func ids(t *testing.T, doc map[string]json.RawMessage) string {
	t.Helper()
	var convs []CatalogConversation
	if err := json.Unmarshal(doc["conversations"], &convs); err != nil {
		t.Fatalf("no conversations in %s", doc["error"])
	}
	var out []string
	for _, c := range convs {
		out = append(out, fmt.Sprintf("%s@%.0f", c.ID, *c.Created))
	}
	return strings.Join(out, ",")
}

// The three source kinds, with record shapes taken from real CLIs (pi's
// JSONL header, Vibe's meta.json, Hermes' state.db, OpenCode's listing).
func TestCatalogReaderSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "repo")
	other := filepath.Join(home, "elsewhere")
	for _, d := range []string{ws, other, filepath.Join(home, ".pi/agent/sessions/a"), filepath.Join(home, ".vibe/logs/session/session_1")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		if err := os.WriteFile(filepath.Join(home, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".pi/agent/sessions/a/1.jsonl", `{"type":"session","version":3,"id":"01a0e22a-20cf-7443-b683-ce30d22f08bf","timestamp":"2026-09-27T09:20:06.864Z","cwd":"`+ws+`"}`+"\n"+`{"type":"message","id":"x","timestamp":"2026-09-27T09:21:00Z"}`+"\n")
	write(".pi/agent/sessions/a/2.jsonl", `{"type":"session","id":"elsewhere-1","timestamp":"2026-09-27T09:20:06Z","cwd":"`+other+`"}`+"\n")
	pi, _ := FindCatalogPreset("pi")
	os.Unsetenv("PI_CODING_AGENT_SESSION_DIR")
	os.Unsetenv("PI_CODING_AGENT_DIR")
	if got := ids(t, runReader(t, pi.Spec, ws, "")); got != "01a0e22a-20cf-7443-b683-ce30d22f08bf@1790500807" {
		t.Errorf("pi: %s", got)
	}
	write(".vibe/logs/session/session_1/meta.json", `{"session_id":"087b2cf2-da56-dbff-c2c1-170c706b1ec9","start_time":"2026-09-27T09:22:11.155428+00:00","origin_directory":"`+ws+`","title":null}`)
	vibe, _ := FindCatalogPreset("vibe")
	os.Unsetenv("VIBE_HOME")
	if got := ids(t, runReader(t, vibe.Spec, ws, "")); got != "087b2cf2-da56-dbff-c2c1-170c706b1ec9@1790500931" {
		t.Errorf("vibe: %s", got)
	}
	// SQLite, through Python so the test needs no Go SQLite driver setup.
	os.MkdirAll(filepath.Join(home, ".hermes"), 0o755)
	py := `import sqlite3,sys
db=sqlite3.connect(sys.argv[1])
db.execute("create table sessions(id text, cwd text, started_at real, last_activity_at real, title text, archived int, hidden int)")
db.execute("insert into sessions values('20260927_032121_985cf4',?,1790500883.49,1790500883.5,'say hi',0,0)",(sys.argv[2],))
db.execute("insert into sessions values('hidden-one',?,1790500900,1790500900,'x',0,1)",(sys.argv[2],))
db.commit()`
	if out, err := exec.Command("python3", "-c", py, filepath.Join(home, ".hermes/state.db"), ws).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	hermes, _ := FindCatalogPreset("hermes")
	os.Unsetenv("HERMES_HOME")
	if got := ids(t, runReader(t, hermes.Spec, ws, "")); got != "20260927_032121_985cf4@1790500883" {
		t.Errorf("hermes: %s", got)
	}
	// A listing command, run in the working directory; epoch milliseconds.
	listing := fmt.Sprintf(`[{"id":"ses_1","title":"t","updated":1790500749323,"created":1790500748265,"directory":%q},{"id":"ses_2","created":1790500748265,"directory":%q}]`, ws, other)
	oc := Spec{Name: "opencode", Command: "opencode", Sessions: &SessionsSpec{Command: "cat " + filepath.Join(home, "listing.json"),
		ID: "id", Dir: "directory", Created: "created", Updated: "updated", Title: "title"}}
	write("listing.json", listing)
	if got := ids(t, runReader(t, oc, ws, "")); got != "ses_1@1790500748" {
		t.Errorf("command: %s", got)
	}
	// Validating one id: found in this workspace, refused from another.
	if doc := runReader(t, oc, ws, "ses_1"); doc["conversation"] == nil {
		t.Errorf("ses_1 not validated: %s", doc["error"])
	}
	if doc := runReader(t, oc, ws, "ses_2"); doc["error"] == nil {
		t.Error("a conversation from another folder was accepted")
	}
	if doc := runReader(t, oc, ws, "../../etc/passwd"); doc["error"] == nil {
		t.Error("a path-shaped id was accepted")
	}
}

// End to end through the manager: a fresh catalog session is bound to the
// one conversation its CLI created after launch, and left alone when another
// unbound session of the same agent shares the folder.
func TestCaptureBindsOnlyAnUnambiguousNewConversation(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	m := New(db, executor.NewRegistry(false, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ex, _ := m.Reg.For(target)
	now := store.Now()
	listing := filepath.Join(t.TempDir(), "list.json")
	os.WriteFile(listing, []byte(fmt.Sprintf(`[{"id":"old","created":%f,"directory":%q},{"id":"new","created":%f,"directory":%q}]`, now-3600, ws, now+2, ws)), 0o644)
	spec := Spec{Name: "opencode", Command: "opencode", ResumeIDArgs: []string{"--session", "{id}"},
		Sessions: &SessionsSpec{Command: "cat " + listing, ID: "id", Dir: "directory", Created: "created"}}
	row, err := db.InsertSession(&store.Session{TargetID: target.ID, Agent: "opencode", Workdir: ws, TmuxSession: "t1", Status: StatusIdle, Origin: "lectern"})
	if err != nil {
		t.Fatal(err)
	}
	m.launched.set(row.ID, now-6)
	peer, err := db.InsertSession(&store.Session{TargetID: target.ID, Agent: "opencode", Workdir: ws, TmuxSession: "t2", Status: StatusIdle, Origin: "lectern"})
	if err != nil {
		t.Fatal(err)
	}
	m.launched.set(row.ID, now)
	if got := m.captureCatalogConversation(context.Background(), ex, row, spec); got != "" {
		t.Fatalf("read the CLI's sessions before it settled: %q", got)
	}
	m.launched.set(row.ID, now-6)
	if got := m.captureCatalogConversation(context.Background(), ex, row, spec); got != "" {
		t.Fatalf("bound %q while another unbound session shares the folder", got)
	}
	db.Update("sessions", peer.ID, map[string]any{"ended_at": now - 60})
	if got := m.captureCatalogConversation(context.Background(), ex, row, spec); got != "new" {
		t.Fatalf("got %q", got)
	}
	fresh, _ := db.Session(row.ID)
	if fresh.NativeRecoveryCID != "new" {
		t.Fatalf("binding not recorded: %q", fresh.NativeRecoveryCID)
	}
	// A second session launched later cannot take the same conversation.
	later, _ := db.InsertSession(&store.Session{TargetID: target.ID, Agent: "opencode", Workdir: ws, TmuxSession: "t3", Status: StatusIdle, Origin: "lectern"})
	m.launched.set(later.ID, now-6)
	if got := m.captureCatalogConversation(context.Background(), ex, later, spec); got != "" {
		t.Fatalf("a bound conversation was taken again: %q", got)
	}
}
