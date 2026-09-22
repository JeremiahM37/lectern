package sessions

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/internal/bus"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/store"
	"github.com/JeremiahM37/lectern/internal/testutil"
)

func TestRecoverAfterTargetRebootResumesCapturedCIDOnce(t *testing.T) {
	testutil.RequireIsolated(t)
	root := t.TempDir()
	workdir := filepath.Join(root, "workspace")
	home := filepath.Join(root, "codex-home")
	binDir := filepath.Join(root, "bin")
	for _, dir := range []string{workdir, filepath.Join(home, "sessions"), binDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	src := filepath.Join(root, "fake.go")
	if err := os.WriteFile(src, []byte(`package main
import("encoding/json";"os";"path/filepath";"strings";"time")
func main(){
 cid:="11111111-1111-4111-8111-111111111111"
 home:=os.Getenv("CODEX_HOME"); p:=filepath.Join(home,"sessions",cid+".jsonl")
 f,_:=os.OpenFile(p,os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600); defer f.Close()
 b,_:=json.Marshal(map[string]any{"type":"session_meta","payload":map[string]any{"id":cid,"cwd":mustCwd(),"source":"vscode"}}); f.Write(append(b,'\n'))
 args,_:=os.OpenFile(filepath.Join(mustCwd(),"launches.log"),os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600); args.WriteString(strings.Join(os.Args[1:]," ")+"\n"); args.Close()
 for { time.Sleep(time.Second) }
}
func mustCwd() string { p,_:=os.Getwd(); return p }
`), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "codex")
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build fake codex: %v: %s", err, out)
	}

	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&store.Target{Name: "local", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	ex := executor.NewLocal()
	m := New(db, executor.NewRegistry(false, 0), bus.New(), Launcher{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.Specs = func() []Spec {
		return []Spec{{Name: "codex", Command: bin, Env: map[string]string{"CODEX_HOME": home}, ResumeIDArgs: []string{"resume", "{id}"}}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	row, err := m.Launch(ctx, LaunchOpts{TargetID: target.ID, Agent: "codex", Name: "reboot", Workdir: workdir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exec.Command("tmux", "kill-session", "-t", "="+row.TmuxSession).Run(); m.Close() }()

	var captured *store.Session
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		captured, err = db.Session(row.ID)
		if err == nil && captured.NativeRecoveryCID != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if captured == nil || captured.NativeRecoveryCID == "" {
		t.Fatalf("native CID was not checkpointed: %+v", captured)
	}
	cid := captured.NativeRecoveryCID
	if err := exec.Command("tmux", "kill-session", "-t", "="+row.TmuxSession).Run(); err != nil {
		t.Fatal(err)
	}
	newBoot := "22222222-2222-4222-8222-222222222222"
	m.recoverAfterBoot(ctx, target, ex, []*store.Session{captured}, newBoot)
	after, err := db.Session(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.EndedAt != nil || after.BootID != newBoot {
		t.Fatalf("recovery did not retain live row: %+v", after)
	}
	var args []byte
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		args, err = os.ReadFile(filepath.Join(workdir, "launches.log"))
		if err == nil && strings.Count(strings.TrimSuffix(string(args), "\n"), "\n")+1 >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(args), "\n"), "\n")
	if len(lines) != 2 || lines[1] != "resume "+cid {
		t.Fatalf("recovery launch arguments: %q", string(args))
	}

	m.recoverAfterBoot(ctx, target, ex, []*store.Session{after}, newBoot)
	args, err = os.ReadFile(filepath.Join(workdir, "launches.log"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSuffix(string(args), "\n"), "\n") + 1; got != 2 {
		t.Fatalf("second recovery launched again: %q", string(args))
	}
}
