package sessions

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/accounts"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/limits"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// fakeClaude stands in for Claude Code with the parts an account swap relies
// on: its config directory comes from CLAUDE_CONFIG_DIR, --resume <id> finds
// the conversation anywhere under projects/ and fails with Claude's own words
// when it is not there, and an account with a "limited" marker answers with
// Claude's real session-limit message. Every launch and every prompt it
// receives is logged with the directory it ran under.
const fakeClaude = `package main
import("bufio";"fmt";"os";"path/filepath";"regexp";"strings")
func main(){
 dir:=os.Getenv("CLAUDE_CONFIG_DIR"); cwd,_:=os.Getwd()
 id:=""
 for i,a:=range os.Args{ if a=="--resume"&&i+1<len(os.Args){ id=os.Args[i+1] } }
 logf(cwd,"launches.log",dir+" "+strings.Join(os.Args[1:]," "))
 matches,_:=filepath.Glob(filepath.Join(dir,"projects","*",id+".jsonl"))
 if id==""||len(matches)==0 { fmt.Println("No conversation found with session ID: "+id); os.Exit(1) }
 transcript:=matches[0]
 limited:=func() bool { _,err:=os.Stat(filepath.Join(dir,"limited")); return err==nil }
 prompt:=func(){ fmt.Print("\n╭──────╮\n│ >    │\n╰──────╯\n") }
 if limited() { fmt.Println("  ⎿  You've hit your session limit · resets 3:40pm (UTC)") }
 prompt()
 in:=bufio.NewScanner(os.Stdin)
 for in.Scan(){
  line:=strings.TrimSpace(in.Text()); if line=="" { continue }
  if limited() { fmt.Println("  ⎿  You've hit your session limit · resets 3:40pm (UTC)"); prompt(); continue }
  f,_:=os.OpenFile(transcript,os.O_APPEND|os.O_WRONLY,0600); fmt.Fprintf(f,"{\"sessionId\":%q,\"user\":%q}\n",id,line); f.Close()
  logf(cwd,"turns.log",dir+" "+id+" "+line)
  fmt.Println("❯ "+line); fmt.Println("✻ Working on it… (esc to interrupt)")
 }
}
var _ = regexp.MustCompile
func logf(cwd,name,line string){ f,_:=os.OpenFile(filepath.Join(cwd,name),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600); f.WriteString(line+"\n"); f.Close() }
`

// swapRig is a local target with two claude logins — "Default" (the agent
// definition's own CLAUDE_CONFIG_DIR, A) and "second" (B) — the swap policy
// on, and one session resuming a conversation of A's that A is out of usage
// for.
type swapRig struct {
	t            *testing.T
	db           *store.DB
	m            *Manager
	row          *store.Session
	dflt, second *store.Account
	workdir, cid string
	acctA, acctB string
	ctx          context.Context
}

func newSwapRig(t *testing.T, signedIn bool) *swapRig {
	testutil.RequireIsolated(t)
	root := t.TempDir()
	r := &swapRig{t: t, workdir: filepath.Join(root, "work", "repo.x"), cid: "5b1c2d3e-4f50-4a61-8b72-9c83d4e5f601",
		acctA: filepath.Join(root, "acct-a"), acctB: filepath.Join(root, "acct-b")}
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{r.workdir, r.acctA, r.acctB, binDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(root, "fake.go")
	if err := os.WriteFile(src, []byte(fakeClaude), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "claude")
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake claude: %v: %s", err, out)
	}
	transcript := filepath.Join(r.acctA, "projects", accounts.ClaudeProjectDir(r.workdir), r.cid+".jsonl")
	os.MkdirAll(filepath.Dir(transcript), 0o700)
	os.WriteFile(transcript, []byte(`{"sessionId":"`+r.cid+`","user":"refactor the parser"}`+"\n"), 0o600)
	os.WriteFile(filepath.Join(r.acctA, "limited"), nil, 0o600)
	os.WriteFile(filepath.Join(r.acctA, ".credentials.json"), []byte("{}"), 0o600)
	if signedIn {
		os.WriteFile(filepath.Join(r.acctB, ".credentials.json"), []byte("{}"), 0o600)
	}

	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	r.db = db
	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	r.m = New(db, executor.NewRegistry(false, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.m.Specs = func() []Spec {
		return []Spec{{Name: "claude", Command: bin, Env: map[string]string{"CLAUDE_CONFIG_DIR": r.acctA},
			ResumeIDArgs: []string{"--resume", "{id}"}}}
	}
	r.dflt, _ = db.InsertAccount(&store.Account{TargetID: target.ID, Agent: "claude", Label: "Default"})
	r.second, _ = db.InsertAccount(&store.Account{TargetID: target.ID, Agent: "claude", Label: "second", Dir: r.acctB})
	raw, _ := json.Marshal(limits.Policy{Mode: limits.ModeSwap})
	db.SetLimitPolicyJSON(limits.ScopeGlobal, 0, string(raw))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	r.ctx = ctx
	r.row, err = r.m.Launch(ctx, LaunchOpts{TargetID: target.ID, Agent: "claude", Name: "swap", Workdir: r.workdir, ResumeID: r.cid})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", "="+r.row.TmuxSession).Run(); r.m.Close() })
	return r
}

// tracker is a fresh limits tracker on the rig, as a fresh process would have.
func (r *swapRig) tracker() *limits.Tracker {
	tr := limits.New(r.db, bus.New(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	tr.Sessions = r.m
	r.m.Limits = tr
	return tr
}

// run polls and ticks until done says so or two minutes pass.
func (r *swapRig) run(tr *limits.Tracker, done func(*store.LimitHold) bool) *store.LimitHold {
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		r.m.Poll(r.ctx)
		tr.Tick(r.ctx)
		tr.Wait()
		if h, err := r.db.LatestLimitHoldForSession(r.row.ID); err == nil && done(h) {
			return h
		}
		time.Sleep(300 * time.Millisecond)
	}
	h, _ := r.db.LatestLimitHoldForSession(r.row.ID)
	r.t.Fatalf("gave up: hold %+v\nlaunches:\n%s", h, r.read("launches.log"))
	return nil
}

func (r *swapRig) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(r.workdir, name))
	return string(b)
}

func TestLimitedSessionSwapsAccountAndResumesTheSameConversation(t *testing.T) {
	r := newSwapRig(t, true)
	hold := r.run(r.tracker(), func(h *store.LimitHold) bool { return h.State == limits.StateResumed })
	if hold.AccountFrom == nil || *hold.AccountFrom != r.dflt.ID || hold.AccountTo == nil || *hold.AccountTo != r.second.ID {
		t.Fatalf("hold ends: %+v", hold)
	}
	lines := strings.Split(strings.TrimSpace(r.read("launches.log")), "\n")
	if len(lines) != 2 || lines[0] != r.acctA+" --resume "+r.cid || lines[1] != r.acctB+" --resume "+r.cid {
		t.Fatalf("launches:\n%s", r.read("launches.log"))
	}
	// The same conversation, copied into B, got the nudge: the resumed agent
	// took its next turn in B's copy of the conversation.
	if want := r.acctB + " " + r.cid + " " + limits.NudgeText; strings.TrimSpace(r.read("turns.log")) != want {
		t.Fatalf("turns:\n%s\nwant %q", r.read("turns.log"), want)
	}
	moved, err := os.ReadFile(filepath.Join(r.acctB, "projects", accounts.ClaudeProjectDir(r.workdir), r.cid+".jsonl"))
	if err != nil || !strings.Contains(string(moved), "refactor the parser") || !strings.Contains(string(moved), limits.NudgeMarker) {
		t.Fatalf("B's transcript: %q %v", moved, err)
	}
	after, _ := r.db.Session(r.row.ID)
	if after.EndedAt != nil || after.AccountID == nil || *after.AccountID != r.second.ID || after.TmuxSession != r.row.TmuxSession {
		t.Fatalf("session after swap: %+v", after)
	}
	if a, _ := r.db.Account(r.dflt.ID); a.LimitedUntil == nil {
		t.Fatal("A was not recorded as limited")
	}

	// A restarted Lectern finds nothing left to do: no second swap, no
	// second nudge.
	restarted := r.tracker()
	for i := 0; i < 3; i++ {
		r.m.Poll(r.ctx)
		restarted.Tick(r.ctx)
		restarted.Wait()
	}
	if strings.Count(r.read("launches.log"), "\n") != 2 || strings.Count(r.read("turns.log"), "\n") != 1 {
		t.Fatalf("after restart:\nlaunches:\n%s\nturns:\n%s", r.read("launches.log"), r.read("turns.log"))
	}
}

// An account that was never signed in is not swapped to: the limited agent
// keeps running under its own login and the policy's fallback applies.
func TestSwapToASignedOutAccountLeavesTheAgentAlone(t *testing.T) {
	r := newSwapRig(t, false)
	hold := r.run(r.tracker(), func(h *store.LimitHold) bool {
		return h.Policy == limits.ModeNotify && strings.Contains(h.Note, "not signed in")
	})
	if hold.State != limits.StateWaiting {
		t.Fatalf("hold: %+v", hold)
	}
	if lines := strings.Split(strings.TrimSpace(r.read("launches.log")), "\n"); len(lines) != 1 {
		t.Fatalf("the agent was restarted:\n%s", r.read("launches.log"))
	}
	after, _ := r.db.Session(r.row.ID)
	if after.EndedAt != nil || after.AccountID != nil {
		t.Fatalf("session: %+v", after)
	}
}
