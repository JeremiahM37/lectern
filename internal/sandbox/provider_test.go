package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// dirHooks is a script provider whose "sandbox" is a directory: enough to
// run every hook for real on this machine.
const dirHooks = `create: |
  d=$(mktemp -d "${SANDBOX_ROOT}/sb-XXXXXX")
  printf '%s' "$GREETING" > "$d/greeting"
  echo "creating $LECTERN_SANDBOX_NAME for attempt $LECTERN_ATTEMPT_ID" >&2
  echo "$d"
exec: |
  cd "$LECTERN_SANDBOX_ID" && cd "${LECTERN_CWD:-.}" && bash -c "$LECTERN_COMMAND"
suspend: |
  touch "$LECTERN_SANDBOX_ID/.suspended"
resume: |
  rm -f "$LECTERN_SANDBOX_ID/.suspended"
destroy: |
  rm -rf "$LECTERN_SANDBOX_ID"
attach: |
  cd "$LECTERN_SANDBOX_ID" && exec bash -c "$LECTERN_COMMAND"
env:
  GREETING: from-the-file
`

func trustedScript(t *testing.T) (Config, string) {
	t.Helper()
	repo := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "lectern.sandbox.yaml"), []byte(dirHooks), 0644); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Provider: "script", ConfigPath: "{repo}/lectern.sandbox.yaml", Env: map[string]string{"SANDBOX_ROOT": root}}
	cfg.Trusted = map[string]string{filepath.Join(repo, "lectern.sandbox.yaml"): Hash([]byte(dirHooks))}
	return cfg, repo
}

func TestScriptProviderRunsEveryHookForReal(t *testing.T) {
	cfg, repo := trustedScript(t)
	ctx := context.Background()
	p, err := LoadScript(ctx, executor.NewLocal(), cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	id, err := p.Create(ctx, CreateRequest{AttemptID: 7, Env: map[string]string{"GREETING": "from-dispatch"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(id), "sb-") {
		t.Fatalf("id %q", id)
	}
	if b, _ := os.ReadFile(filepath.Join(id, "greeting")); string(b) != "from-dispatch" {
		t.Fatalf("a dispatch's env must win over the file's: %q", b)
	}
	inside := p.Exec(id)
	if err := inside.WriteFile(ctx, "work/notes.txt", []byte("hello\nworld\n")); err != nil {
		t.Fatal(err)
	}
	r, err := inside.Run(ctx, "pwd; cat notes.txt | wc -l", executor.RunOpts{Cwd: "work"})
	if err != nil || r.Stdout != id+"/work\n2\n" {
		t.Fatalf("exec: %+v %v", r, err)
	}
	if got, _ := inside.ReadFile(ctx, "work/notes.txt", 6); string(got) != "world\n" {
		t.Fatalf("read %q", got)
	}
	if err := p.Suspend(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(id, ".suspended")); err != nil {
		t.Fatal("suspend hook did not run")
	}
	if err := p.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	argv, err := p.Attach(id, "echo attached")
	if err != nil || argv[0] != "bash" {
		t.Fatalf("attach %v %v", argv, err)
	}
	if err := p.Destroy(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(id); !os.IsNotExist(err) {
		t.Fatal("destroy hook did not run")
	}
}

// An agent that edits the hooks file must not get its edit run on the
// Lectern server: only the content a person trusted runs.
func TestScriptProviderRefusesAnUntrustedOrChangedFile(t *testing.T) {
	cfg, repo := trustedScript(t)
	os.WriteFile(filepath.Join(repo, "lectern.sandbox.yaml"), []byte(dirHooks+"\n# edited\n"), 0644)
	if _, err := LoadScript(context.Background(), executor.NewLocal(), cfg, repo); err == nil || !strings.Contains(err.Error(), "never trusted") {
		t.Fatalf("got %v", err)
	}
	cfg.Trusted = nil
	if _, err := LoadScript(context.Background(), executor.NewLocal(), cfg, repo); err == nil {
		t.Fatal("an untrusted file loaded")
	}
}

type recorder struct {
	mu   sync.Mutex
	cmds []string
}

func (r *recorder) Run(_ context.Context, cmd string, _ executor.RunOpts) (executor.Result, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return executor.Result{}, nil
}
func (r *recorder) ReadFile(context.Context, string, int64) ([]byte, error) { return nil, nil }
func (r *recorder) WriteFile(context.Context, string, []byte) error         { return nil }
func (r *recorder) Close() error                                            { return nil }

func TestDockerProviderCommands(t *testing.T) {
	rec := &recorder{}
	d := &Docker{Host: rec, Cfg: Config{Provider: "docker", Image: "ghcr.io/me/dev:1", DockerHost: "ssh://u@box",
		RunArgs: []string{"--memory", "4g"}, Env: map[string]string{"A": "1"}}}
	ctx := context.Background()
	id, err := d.Create(ctx, CreateRequest{AttemptID: 3, Name: "lec-sb-3", Env: map[string]string{"B": "two words"}})
	if err != nil || id != "lec-sb-3" {
		t.Fatal(id, err)
	}
	d.Exec(id).Run(ctx, "git status", executor.RunOpts{Cwd: "/work"})
	d.Suspend(ctx, id)
	d.Resume(ctx, id)
	d.Destroy(ctx, id)
	all := strings.Join(rec.cmds, "\n")
	for _, want := range []string{
		"docker -H ssh://u@box run -d --name lec-sb-3 --label lectern.sandbox=1 --label lectern.attempt=3 -e A=1 -e 'B=two words' --memory 4g ghcr.io/me/dev:1 sleep infinity",
		"docker -H ssh://u@box exec lec-sb-3 true",
		"docker -H ssh://u@box exec -i -w /work lec-sb-3 bash -c 'git status'",
		"docker -H ssh://u@box pause lec-sb-3",
		"docker -H ssh://u@box unpause lec-sb-3",
		"docker -H ssh://u@box rm -f lec-sb-3",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	argv, _ := (&Docker{Host: rec, Cfg: d.Cfg, Remote: []string{"ssh", "-tt", "u@box"}}).Attach(id, "tmux attach -t x")
	if strings.Join(argv, " ") != "ssh -tt u@box docker -H ssh://u@box exec -it lec-sb-3 bash -c 'tmux attach -t x'" {
		t.Fatalf("attach %v", argv)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, bad := range []Config{
		{Provider: "docker"},
		{Provider: "docker", Image: "-v /:/host"},
		{Provider: "script"},
		{Provider: "fly"},
		{Provider: "proxmox", Env: map[string]string{"BAD-NAME": "x"}},
		{Provider: "proxmox", OnFinish: "archive"},
	} {
		if bad.Validate() == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
	if ParseConfig("").Provider != "proxmox" {
		t.Fatal("an empty config is the Proxmox provider")
	}
}

// Opt in with LECTERN_REAL_DOCKER_SSH=user@host and LECTERN_REAL_DOCKER_IMAGE
// (an image with bash already on that machine): a container is made, used,
// paused, resumed and removed through that machine's docker CLI.
func TestDockerProviderAgainstARealDaemon(t *testing.T) {
	dest, image := os.Getenv("LECTERN_REAL_DOCKER_SSH"), os.Getenv("LECTERN_REAL_DOCKER_IMAGE")
	if dest == "" || image == "" {
		t.Skip("set LECTERN_REAL_DOCKER_SSH=user@host and LECTERN_REAL_DOCKER_IMAGE")
	}
	user, host, _ := strings.Cut(dest, "@")
	machine := executor.NewOpenSSH(host, user, 22, "", "", executor.SSHOptions{})
	defer machine.Close()
	d := &Docker{Host: machine, Cfg: Config{Provider: "docker", Image: image}}
	ctx := context.Background()
	id, err := d.Create(ctx, CreateRequest{AttemptID: 99, Env: map[string]string{"LECTERN_WS": "ws-99"}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Destroy(ctx, id)
	inside := d.Exec(id)
	if err := inside.WriteFile(ctx, "/tmp/lec/a.txt", []byte("inside the container\n")); err != nil {
		t.Fatal(err)
	}
	r, err := inside.Run(ctx, "cat a.txt; echo $LECTERN_WS; cat /proc/1/cmdline | tr '\\0' ' '", executor.RunOpts{Cwd: "/tmp/lec", Timeout: 30})
	if err != nil || !strings.Contains(r.Stdout, "inside the container\nws-99\nsleep infinity") {
		t.Fatalf("exec: %+v %v", r, err)
	}
	if err := d.Suspend(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, _ := machine.Run(ctx, "docker inspect -f '{{.State.Status}}' "+id, executor.RunOpts{})
	if strings.TrimSpace(st.Stdout) != "paused" {
		t.Fatalf("after suspend: %q", st.Stdout)
	}
	if err := d.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := d.Destroy(ctx, id); err != nil {
		t.Fatal(err)
	}
	gone, _ := machine.Run(ctx, "docker ps -a --filter name="+id+" -q", executor.RunOpts{})
	if strings.TrimSpace(gone.Stdout) != "" {
		t.Fatal("container still exists")
	}
	t.Logf("real docker sandbox %s on %s: create, exec, write, pause, unpause, remove", id, host)
}
