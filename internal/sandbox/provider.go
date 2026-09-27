package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"gopkg.in/yaml.v3"
)

// A Provider makes and drives sandboxes of one kind (docs/sandboxes.md):
// Proxmox linked clones, Docker containers, or anything a user can script.
// Every provider's commands run through a host executor, so mock mode and a
// remote Docker machine work the same way as the control plane itself.
type Provider interface {
	Name() string
	// Create makes a sandbox, starts it and waits until it runs commands.
	Create(ctx context.Context, req CreateRequest) (string, error)
	// Exec is an executor whose commands run inside the sandbox.
	Exec(id string) executor.Executor
	Suspend(ctx context.Context, id string) error
	Resume(ctx context.Context, id string) error
	Destroy(ctx context.Context, id string) error
	// Can reports whether an optional operation ("suspend", "resume",
	// "attach") is available.
	Can(op string) bool
	// Attach is the argv that runs inner (already a command line) inside the
	// sandbox with a terminal, for the web terminal.
	Attach(id string, inner string) ([]string, error)
}

// CreateRequest is what a sandbox is made for.
type CreateRequest struct {
	AttemptID int64
	Name      string
	Env       map[string]string
}

// Config is a sandbox target's sandbox_json.
type Config struct {
	// Provider is "proxmox" (the default: host holds the template vmid),
	// "docker" or "script".
	Provider string `json:"provider,omitempty"`
	// Docker: Image to run, the Machine (a target name) whose docker CLI to
	// use ("" = this server), an optional DockerHost (-H) and extra run args.
	Image      string   `json:"image,omitempty"`
	Machine    string   `json:"machine,omitempty"`
	DockerHost string   `json:"docker_host,omitempty"`
	RunArgs    []string `json:"run_args,omitempty"`
	// Script: path to a lectern.sandbox.yaml on the machine that runs the
	// hooks. "{repo}" is the project's repo path. Hooks only run while the
	// file matches a hash a person trusted (Trusted, path -> sha256).
	ConfigPath string            `json:"config_path,omitempty"`
	Trusted    map[string]string `json:"trusted,omitempty"`
	// Env is given to every sandbox this target makes.
	Env map[string]string `json:"env,omitempty"`
	// OnFinish is what happens when an attempt ends: "destroy" (default),
	// "suspend" or "keep".
	OnFinish string `json:"on_finish,omitempty"`
}

// ParseConfig reads targets.sandbox_json; empty means Proxmox.
func ParseConfig(raw string) Config {
	var c Config
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &c)
	}
	if c.Provider == "" {
		c.Provider = "proxmox"
	}
	return c
}

// JSON renders the config for storage.
func (c Config) JSON() string {
	b, _ := json.Marshal(c)
	return string(b)
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks a config a person is saving.
func (c Config) Validate() error {
	switch c.Provider {
	case "proxmox":
	case "docker":
		if strings.TrimSpace(c.Image) == "" {
			return fmt.Errorf("docker sandboxes need an image")
		}
		if strings.HasPrefix(c.Image, "-") || strings.ContainsAny(c.Image, " \n") {
			return fmt.Errorf("image is not an image name")
		}
		if c.DockerHost != "" && strings.ContainsAny(c.DockerHost, " \n'\"") {
			return fmt.Errorf("docker_host has spaces or quotes")
		}
	case "script":
		if strings.TrimSpace(c.ConfigPath) == "" {
			return fmt.Errorf("script sandboxes need config_path (e.g. {repo}/lectern.sandbox.yaml)")
		}
	default:
		return fmt.Errorf("provider must be proxmox, docker or script")
	}
	for k := range c.Env {
		if !envKey.MatchString(k) {
			return fmt.Errorf("env name %q is not a variable name", k)
		}
	}
	switch c.OnFinish {
	case "", "destroy", "suspend", "keep":
	default:
		return fmt.Errorf("on_finish must be destroy, suspend or keep")
	}
	return nil
}

// ResolvePath fills {repo} in the script config path.
func (c Config) ResolvePath(repo string) string {
	return strings.ReplaceAll(c.ConfigPath, "{repo}", strings.TrimRight(repo, "/"))
}

// envPrefix renders env as `export K=V; ` lines, sorted for stable commands.
func envPrefix(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		if envKey.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString("export " + k + "=" + executor.ShellQuote(env[k]) + "; ")
	}
	return b.String()
}

func merged(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func runOK(ctx context.Context, host executor.Executor, cmd string, timeout float64, what string) (executor.Result, error) {
	r, err := host.Run(ctx, cmd, executor.RunOpts{Timeout: timeout})
	if err != nil {
		return r, err
	}
	if !r.OK() {
		return r, executor.Errf("%s failed (exit %d): %s", what, r.RC, clip(strings.TrimSpace(r.Stderr+" "+r.Stdout), 400))
	}
	return r, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// ---- Proxmox -------------------------------------------------------------------

// Proxmox is the original provider: a linked clone of a template LXC.
type Proxmox struct {
	Host     executor.Executor
	Template string
	Log      *slog.Logger
}

func (p *Proxmox) Name() string { return "proxmox" }

func (p *Proxmox) Create(ctx context.Context, req CreateRequest) (string, error) {
	return Provision(ctx, p.Host, p.Template, req.AttemptID, p.Log)
}

func (p *Proxmox) Exec(id string) executor.Executor { return executor.NewPct(id) }

func (p *Proxmox) Suspend(ctx context.Context, id string) error {
	_, err := runOK(ctx, p.Host, "sudo pct suspend "+executor.ShellQuote(id), 120, "pct suspend")
	return err
}

func (p *Proxmox) Resume(ctx context.Context, id string) error {
	_, err := runOK(ctx, p.Host, "sudo pct resume "+executor.ShellQuote(id), 120, "pct resume")
	return err
}

func (p *Proxmox) Destroy(ctx context.Context, id string) error {
	Destroy(ctx, p.Host, id, p.Log)
	return nil
}

func (p *Proxmox) Can(op string) bool { return true }

func (p *Proxmox) Attach(id, inner string) ([]string, error) {
	return []string{"sudo", "pct", "exec", id, "--", "bash", "-c", inner}, nil
}

// ---- Docker --------------------------------------------------------------------

// Docker runs each sandbox as a container, on this server or on any machine
// Lectern reaches (its docker CLI is driven through that machine's executor).
type Docker struct {
	Host   executor.Executor
	Cfg    Config
	Remote []string // ssh argv prefix to the machine, for terminals; nil = local
}

func (d *Docker) Name() string { return "docker" }

func (d *Docker) docker() string {
	if d.Cfg.DockerHost != "" {
		return "docker -H " + executor.ShellQuote(d.Cfg.DockerHost)
	}
	return "docker"
}

func (d *Docker) Create(ctx context.Context, req CreateRequest) (string, error) {
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("lec-sb-%d-%d", req.AttemptID, time.Now().Unix()%100000)
	}
	args := []string{d.docker(), "run", "-d", "--name", executor.ShellQuote(name),
		"--label", "lectern.sandbox=1", "--label", executor.ShellQuote(fmt.Sprintf("lectern.attempt=%d", req.AttemptID))}
	env := merged(d.Cfg.Env, req.Env)
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if envKey.MatchString(k) {
			args = append(args, "-e", executor.ShellQuote(k+"="+env[k]))
		}
	}
	for _, a := range d.Cfg.RunArgs {
		args = append(args, executor.ShellQuote(a))
	}
	// A long sleep keeps the container up; the agent runs in tmux inside it.
	args = append(args, executor.ShellQuote(d.Cfg.Image), "sleep", "infinity")
	if _, err := runOK(ctx, d.Host, strings.Join(args, " "), 600, "docker run"); err != nil {
		return "", err
	}
	for i := 0; i < 30; i++ {
		if r, err := d.Host.Run(ctx, d.docker()+" exec "+executor.ShellQuote(name)+" true", executor.RunOpts{Timeout: 20}); err == nil && r.OK() {
			return name, nil
		}
		select {
		case <-ctx.Done():
			_ = d.Destroy(context.Background(), name)
			return "", ctx.Err()
		case <-time.After(time.Second):
		}
	}
	_ = d.Destroy(ctx, name)
	return "", executor.Errf("container %s never ran a command", name)
}

func (d *Docker) Exec(id string) executor.Executor {
	return &hookExec{host: d.Host, wrap: func(cmd, cwd string) string {
		w := ""
		if cwd != "" {
			w = "-w " + executor.ShellQuote(cwd) + " "
		}
		return d.docker() + " exec -i " + w + executor.ShellQuote(id) + " bash -c " + executor.ShellQuote(cmd)
	}}
}

func (d *Docker) Suspend(ctx context.Context, id string) error {
	_, err := runOK(ctx, d.Host, d.docker()+" pause "+executor.ShellQuote(id), 60, "docker pause")
	return err
}

func (d *Docker) Resume(ctx context.Context, id string) error {
	_, err := runOK(ctx, d.Host, d.docker()+" unpause "+executor.ShellQuote(id), 60, "docker unpause")
	return err
}

func (d *Docker) Destroy(ctx context.Context, id string) error {
	_, err := runOK(ctx, d.Host, d.docker()+" rm -f "+executor.ShellQuote(id), 120, "docker rm")
	return err
}

func (d *Docker) Can(op string) bool { return true }

func (d *Docker) Attach(id, inner string) ([]string, error) {
	cmd := d.docker() + " exec -it " + executor.ShellQuote(id) + " bash -c " + executor.ShellQuote(inner)
	if d.Remote != nil {
		return append(append([]string{}, d.Remote...), cmd), nil
	}
	return []string{"bash", "-c", cmd}, nil
}

// ---- Script --------------------------------------------------------------------

// Hooks is a lectern.sandbox.yaml: shell snippets for each lifecycle step.
// Every hook sees LECTERN_SANDBOX_ID (except create), LECTERN_ATTEMPT_ID,
// LECTERN_SANDBOX_NAME and the sandbox env. exec also sees LECTERN_COMMAND and
// LECTERN_CWD, and must pass stdin through.
type Hooks struct {
	Create  string            `yaml:"create"`
	Exec    string            `yaml:"exec"`
	Suspend string            `yaml:"suspend"`
	Resume  string            `yaml:"resume"`
	Destroy string            `yaml:"destroy"`
	Attach  string            `yaml:"attach"`
	Env     map[string]string `yaml:"env"`
}

// ParseHooks reads a lectern.sandbox.yaml.
func ParseHooks(data []byte) (Hooks, error) {
	var h Hooks
	if err := yaml.Unmarshal(data, &h); err != nil {
		return h, err
	}
	if strings.TrimSpace(h.Create) == "" || strings.TrimSpace(h.Exec) == "" || strings.TrimSpace(h.Destroy) == "" {
		return h, fmt.Errorf("lectern.sandbox.yaml needs create, exec and destroy hooks")
	}
	return h, nil
}

// Hash is what a person trusts: the file's sha256.
func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Script is the bring-your-own provider: Fly, Modal, Vercel, a VM API,
// anything with a CLI, wired through the hooks file.
type Script struct {
	Host  executor.Executor
	Hooks Hooks
	Env   map[string]string
	Name_ string
}

// LoadScript reads the hooks file through host and refuses it unless its
// hash is the one a person trusted, so an agent that edits the file cannot
// get its hooks run on the Lectern server.
func LoadScript(ctx context.Context, host executor.Executor, cfg Config, repo string) (*Script, error) {
	p := cfg.ResolvePath(repo)
	data, err := readWhole(ctx, host, p)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, executor.Errf("sandbox hooks file %s is missing or empty", p)
	}
	if cfg.Trusted[p] != Hash(data) {
		return nil, executor.Errf("sandbox hooks file %s changed or was never trusted: review it and press Trust in Settings → Machines", p)
	}
	h, err := ParseHooks(data)
	if err != nil {
		return nil, err
	}
	return &Script{Host: host, Hooks: h, Env: merged(h.Env, cfg.Env)}, nil
}

func readWhole(ctx context.Context, host executor.Executor, p string) ([]byte, error) {
	if _, ok := host.(*executor.Local); ok {
		data, err := os.ReadFile(filepath.Clean(p))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		return data, nil
	}
	return host.ReadFile(ctx, p, 0)
}

func (s *Script) Name() string { return "script" }

func (s *Script) hook(body string, env map[string]string) string {
	return envPrefix(merged(s.Env, env)) + "\n" + body
}

func (s *Script) Create(ctx context.Context, req CreateRequest) (string, error) {
	name := req.Name
	if name == "" {
		name = fmt.Sprintf("lec-sb-%d", req.AttemptID)
	}
	env := merged(req.Env, map[string]string{"LECTERN_ATTEMPT_ID": fmt.Sprint(req.AttemptID), "LECTERN_SANDBOX_NAME": name})
	r, err := runOK(ctx, s.Host, s.hook(s.Hooks.Create, env), 900, "create hook")
	if err != nil {
		return "", err
	}
	// The id is the create hook's last line of output; a hook that prints
	// nothing is using the name it was given.
	id := name
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		id = last
	}
	if strings.ContainsAny(id, "\n\x00") || len(id) > 200 {
		return "", executor.Errf("create hook printed an unusable sandbox id")
	}
	return id, nil
}

func (s *Script) idEnv(id string) map[string]string {
	return map[string]string{"LECTERN_SANDBOX_ID": id}
}

func (s *Script) Exec(id string) executor.Executor {
	return &hookExec{host: s.Host, wrap: func(cmd, cwd string) string {
		return s.hook(s.Hooks.Exec, merged(s.idEnv(id), map[string]string{"LECTERN_COMMAND": cmd, "LECTERN_CWD": cwd}))
	}}
}

func (s *Script) step(ctx context.Context, body, what, id string) error {
	if strings.TrimSpace(body) == "" {
		return executor.Errf("this sandbox provider has no %s hook", what)
	}
	_, err := runOK(ctx, s.Host, s.hook(body, s.idEnv(id)), 600, what+" hook")
	return err
}

func (s *Script) Suspend(ctx context.Context, id string) error {
	return s.step(ctx, s.Hooks.Suspend, "suspend", id)
}
func (s *Script) Resume(ctx context.Context, id string) error {
	return s.step(ctx, s.Hooks.Resume, "resume", id)
}
func (s *Script) Destroy(ctx context.Context, id string) error {
	return s.step(ctx, s.Hooks.Destroy, "destroy", id)
}

func (s *Script) Can(op string) bool {
	switch op {
	case "suspend":
		return strings.TrimSpace(s.Hooks.Suspend) != ""
	case "resume":
		return strings.TrimSpace(s.Hooks.Resume) != ""
	case "attach":
		return strings.TrimSpace(s.Hooks.Attach) != ""
	}
	return true
}

func (s *Script) Attach(id, inner string) ([]string, error) {
	if !s.Can("attach") {
		return nil, executor.Errf("this sandbox provider has no attach hook, so it has no terminal")
	}
	if _, ok := s.Host.(*executor.Local); !ok {
		return nil, executor.Errf("script sandbox terminals need the hooks to run on this server")
	}
	return []string{"bash", "-c", s.hook(s.Hooks.Attach, merged(s.idEnv(id), map[string]string{"LECTERN_COMMAND": inner}))}, nil
}

// ---- the executor inside a Docker or script sandbox ------------------------------

// hookExec runs every command through wrap on the host executor.
type hookExec struct {
	host executor.Executor
	wrap func(cmd, cwd string) string
}

func (h *hookExec) Run(ctx context.Context, cmd string, opts executor.RunOpts) (executor.Result, error) {
	return h.host.Run(ctx, h.wrap(cmd, opts.Cwd), executor.RunOpts{Timeout: opts.Timeout})
}

func (h *hookExec) ReadFile(ctx context.Context, p string, offset int64) ([]byte, error) {
	r, err := h.Run(ctx, fmt.Sprintf("tail -c +%d %s 2>/dev/null || true", offset+1, executor.ShellQuote(p)), executor.RunOpts{Timeout: 60})
	if err != nil {
		return nil, err
	}
	return []byte(r.Stdout), nil
}

// WriteFile moves bytes in bounded base64 chunks: the host may itself be a
// remote machine, and a hook is not guaranteed to pass stdin through.
func (h *hookExec) WriteFile(ctx context.Context, p string, data []byte) error {
	return executor.WriteFileChunks(ctx, h.Run, p, data, 16<<10)
}

func (h *hookExec) Stream(ctx context.Context, cmd string, w io.Writer, timeout float64) (executor.Result, error) {
	if s, ok := h.host.(executor.Streamer); ok {
		return s.Stream(ctx, h.wrap(cmd, ""), w, timeout)
	}
	r, err := h.Run(ctx, cmd, executor.RunOpts{Timeout: timeout})
	if err == nil {
		_, _ = io.WriteString(w, r.Stdout)
	}
	return r, err
}

// DialTarget reaches the sandbox's loopback through the python relay, when
// the host is this server (the relay needs stdin to reach the sandbox).
func (h *hookExec) DialTarget(ctx context.Context, addr string) (net.Conn, error) {
	if _, ok := h.host.(*executor.Local); !ok {
		return nil, executor.ErrNoDial
	}
	return executor.BridgeCommand(ctx, addr, func(relay string) string { return h.wrap(relay, "") })
}

func (h *hookExec) Close() error { return nil }
