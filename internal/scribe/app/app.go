// Package app assembles a Scribe session: config, model registry, tools,
// code map, language servers, memory, hooks, MCP servers, session storage
// and the agent itself. The TUI (through tui.Backend), the headless runner
// and the Lectern bridge all drive an *App.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/skills"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	toolexec "github.com/JeremiahM37/lectern/v2/internal/scribe/tools/exec"
)

// Options configures Open.
type Options struct {
	Workdir   string
	Model     string // provider/model override
	Mode      string
	Resume    string // session id prefix to resume ("" = new session)
	NoRepoMap bool
	System    string // extra system prompt text
	Version   string
	// Host answers approvals and questions. Nil means headless rules:
	// deny risky actions unless mode is auto, first option for questions.
	Host tools.Host
	// Ephemeral sessions are not saved (headless without --save-session).
	Ephemeral bool
	MaxSteps  int
	// Preset names a preset (config.LoadWithPreset) merged over the config.
	Preset string
}

// App is one Scribe session.
type App struct {
	opts    Options
	Workdir string
	Cfg     config.Config
	// ConfigWarning is set when the project config was ignored at start
	// (a parse error); empty otherwise. The TUI shows it once.
	ConfigWarning string

	Registry *registry.Registry
	Tools    *tools.Registry
	Agent    *agent.Agent
	Planner  *agent.Planner
	Dispatch *agent.Dispatcher

	// dump writes the lead agent's model requests to the session dir when
	// SCRIBE_REQUEST_DUMP=1 (see requestdump.go).
	dump requestDump

	Store  *session.Store
	Meta   session.Meta
	Checks *session.Checkpoints
	Memory *memory.Stores
	Skills *skills.Set

	ext      extensions // code map, lsp, jobs, mcp, hooks (see wire.go)
	services map[string]any
	host     tools.Host
	deferred *agent.Deferred // nil when deferred_tools is off

	// claimOwner identifies this App in the process-wide file claims;
	// claimLabel is how other sessions name it (see claims.go).
	claimOwner string
	claimLabel string

	// editedFiles are the files this session changed, relative to the
	// workdir, for memory recall (memory.Inject boosts matching records).
	editedMu    sync.Mutex
	editedFiles map[string]bool
	// restoreNote counts restored message parts that were dropped on load.
	restoreNote int

	mu        sync.Mutex
	listeners []agent.Sink
	turn      int
	instr     []agent.Instruction
	codeMap   string
	mapMu     sync.Mutex // guards mapGen
	mapGen    uint64     // the repomap generation codeMap was rendered at
	date      string
	branch    string
	pending   strings.Builder // coalesced text deltas for the transcript
	pendKind  agent.EventKind
	pendAgent string
	// uiCount is the number of saved UI records (loaded once, then kept);
	// uiMarks is uiCount at the start of each turn of this process.
	uiCount   int
	uiCounted bool
	uiMarks   map[int]int
	closed    bool
	// closeHooks run once when the app closes, before the agent is aborted.
	// They let surfaces such as the Lectern bridge deny what is still waiting.
	closeHooks []func()
}

// FindRoot returns the git repository root containing dir, or dir.
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		p := filepath.Dir(d)
		if p == d {
			return abs
		}
		d = p
	}
}

// chosenModel reports whether the model came from a flag, the config or a
// resumed session, rather than from DefaultModel's fallback.
func (a *App) chosenModel() bool {
	return a.opts.Model != "" || a.Cfg.Model != "" || a.opts.Resume != ""
}

// DefaultModel picks a model when config and flags name none: the first
// provider with a key, else a local Ollama.
func DefaultModel(reg *registry.Registry) string {
	order := []string{"anthropic", "openai", "deepseek", "openrouter", "gemini", "groq", "xai", "mistral"}
	byID := map[string]registry.ProviderInfo{}
	for _, p := range reg.List() {
		byID[p.ID] = p
	}
	for _, id := range order {
		if p, ok := byID[id]; ok && p.HasKey && p.DefaultModel != "" {
			return id + "/" + p.DefaultModel
		}
	}
	for _, p := range reg.List() {
		if p.HasKey && p.NeedsKey && p.DefaultModel != "" {
			return p.ID + "/" + p.DefaultModel
		}
	}
	if p, ok := byID["ollama"]; ok {
		return "ollama/" + p.DefaultModel
	}
	return ""
}

// startConfig loads the merged config for a session. A broken project config
// must not stop Scribe (spec 5.5): the global config is used alone and the
// returned warning says so. A broken global file, or an unknown preset, still
// fails. A global file missing on first run is created with defaults (spec
// 02 5.5); if that cannot be done, for example in a read-only home, Scribe
// still starts on built-in defaults and the warning says why.
func startConfig(root, preset string) (config.Config, string, error) {
	warn := ""
	if _, err := config.EnsureGlobal(); err != nil {
		warn = fmt.Sprintf("could not create %s: %v", config.GlobalPath(), err)
	}
	cfg, err := config.Load(root)
	if err != nil {
		g, gerr := config.Load("")
		if gerr != nil {
			return config.Config{}, "", fmt.Errorf("config: %w", gerr)
		}
		cfg = g
		warn = joinWarnings(warn, fmt.Sprintf("project config ignored: %v", err))
	}
	if preset == "" {
		return cfg, warn, nil
	}
	p, perr := config.FindPreset(root, preset)
	if perr != nil {
		return config.Config{}, "", fmt.Errorf("config: %w", perr)
	}
	return config.ApplyPreset(cfg, p), warn, nil
}

// joinWarnings joins two startup notes, skipping an empty one.
func joinWarnings(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// Open builds a session.
func Open(ctx context.Context, o Options) (*App, error) {
	if o.Workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		o.Workdir = wd
	}
	root := FindRoot(o.Workdir)
	cfg, cfgWarning, err := startConfig(root, o.Preset)
	if err != nil {
		return nil, err
	}
	a := &App{opts: o, Workdir: root, Cfg: cfg, date: time.Now().Format("2006-01-02"), ConfigWarning: cfgWarning}
	excludeScribeDir(root)
	a.Registry = registry.New(cfg)
	a.Store = session.NewStore(root)
	// Model lists are cached per provider and prewarmed in parallel at boot,
	// so the first model picker opens with its lists ready. It runs in the
	// background and is bounded, so startup does not wait on the network.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a.Registry.Prewarm(ctx)
	}()

	modelRef := firstNonEmpty(o.Model, cfg.Model)
	mode := firstNonEmpty(o.Mode, cfg.Mode, "default")

	// Session: resume or create.
	var history []llm.Message
	if o.Resume != "" {
		st, meta, err := a.findSession(o.Resume)
		if err != nil {
			return nil, err
		}
		a.Store, a.Meta = st, meta
		a.Workdir = meta.Workdir
		if a.Workdir == "" {
			a.Workdir = root
		}
		if history, err = a.Store.LoadMessages(meta.ID); err != nil {
			return nil, err
		}
		var dropped int
		history, dropped = session.ValidateRestored(history)
		a.restoreNote = dropped
		modelRef = firstNonEmpty(o.Model, meta.Model, modelRef)
		a.turn = meta.Turns
	} else {
		a.Meta = session.Meta{Workdir: a.Workdir, Mode: mode}
		if !o.Ephemeral {
			if err := a.Store.Create(&a.Meta); err != nil {
				return nil, err
			}
		}
	}
	if modelRef == "" {
		modelRef = DefaultModel(a.Registry)
	}
	if modelRef == "" {
		return nil, errors.New("no model configured: set one with --model provider/model, or add an API key with `lectern code --set-key PROVIDER KEY`")
	}
	model, err := a.resolve(modelRef)
	if err != nil {
		return nil, err
	}
	a.Meta.Model = model.Ref

	if !o.Ephemeral {
		if a.Checks, err = session.NewCheckpoints(a.Store, a.Meta.ID); err != nil {
			return nil, err
		}
	}
	if config.Bool(cfg.Memory, true) {
		if a.Memory, err = memory.Open(a.Workdir); err != nil {
			a.Memory = nil // memory is optional; never block a session on it
		} else {
			a.bindMemorySession()
			// A provider embedder is opt-in. With no model set nothing here
			// touches the network, and recall keeps the local hash.
			if id := cfg.MemoryEmbeddingModel; id != "" {
				var e memory.Embedder
				provider, _, err := memory.ParseEmbeddingModel(id)
				var base, key string
				if err == nil {
					base, key, err = a.Registry.EmbeddingCredentials(provider)
				}
				if err == nil {
					e, err = memory.NewEmbedder(id, base, key)
				}
				if err == nil {
					// SetEmbedder smoke-tests the provider, then backfills in the background.
					err = a.Memory.SetEmbedder(e)
				}
				if err != nil {
					// Recall stays on the hash; the user sees why once, at backend start.
					a.ConfigWarning = joinWarnings(a.ConfigWarning, "memory embeddings off: "+err.Error())
				}
			}
		}
	}
	if set, err := skills.Discover(a.Workdir); err == nil {
		a.Skills = set
	}
	a.instr = agent.LoadInstructions(a.Workdir, config.Home(), cfg.Instructions)
	a.branch = gitBranch(a.Workdir)

	a.claimOwner, a.claimLabel = newClaimOwner(), claimLabelFor(a.Meta.ID)
	a.host = o.Host
	if a.host == nil {
		a.host = headlessHost{}
	}
	a.Tools = tools.NewRegistry()
	a.services = map[string]any{}
	if err := a.wire(ctx); err != nil {
		return nil, err
	}
	a.Planner = &agent.Planner{Dir: a.Workdir, Sink: a.emit}
	a.Planner.Register(a.Tools)
	a.Dispatch = &agent.Dispatcher{New: a.newSubagent, MaxConcurrent: cfg.Router.MaxAgents, Sink: a.emit,
		// Long subagent reports are archived here, beside the session state.
		ArchiveDir: filepath.Join(config.Home(), "agent-reports")}
	if a.ext.hookR != nil {
		a.Dispatch.Hooks = a.ext.hookR // SubagentStart and SubagentStop
	}
	a.Dispatch.Register(a.Tools)
	a.Dispatch.Features = a.dispatchFeatures()
	if config.Bool(cfg.DeferredTools, true) {
		a.deferred = agent.NewDeferred(coreToolNames)
		agent.RegisterDeferred(a.Tools, a.deferred)
	}

	a.Agent = agent.New(a.agentOptions(model, mode))
	if a.Checks != nil {
		a.Checks.SetHistory(a.Agent) // undo and redo keep the messages in step with the files
	}
	a.Planner.Agent = a.Agent
	if len(history) > 0 {
		a.Agent.SetMessages(history)
	}
	if raw, err := a.Store.LoadPlan(a.Meta.ID); err == nil && len(raw) > 0 {
		var pl agent.Plan
		if json.Unmarshal(raw, &pl) == nil {
			a.Planner.SetPlan(&pl)
		}
	}
	if a.restoreNote > 0 {
		a.emit(agent.Event{Kind: agent.EvNotice, Text: fmt.Sprintf("Restored session: dropped %d message part(s) that no longer match a tool call.", a.restoreNote)})
	}
	a.ext.sessionStart(ctx, a)
	runOpenHooks(a)
	return a, nil
}

func (a *App) findSession(prefix string) (*session.Store, session.Meta, error) {
	if m, err := a.Store.Open(prefix); err == nil {
		return a.Store, m, nil
	}
	st, m, err := session.FindGlobal(prefix)
	if err != nil {
		return nil, session.Meta{}, fmt.Errorf("no session matching %q: %w", prefix, err)
	}
	return st, m, nil
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if x != "" {
			return x
		}
	}
	return ""
}

// excludeScribeDir adds .scribe/ to the repository's info/exclude so Scribe's
// own state never shows up as untracked files. It works for linked worktrees
// too (git resolves the shared exclude path) and leaves .gitignore alone.
// It does nothing outside a git repository and is idempotent.
func excludeScribeDir(root string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rel, err := gitRun(ctx, root, "rev-parse", "--git-path", "info/exclude")
	if err != nil || strings.TrimSpace(rel) == "" {
		return
	}
	path := strings.TrimSpace(rel)
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return
	}
	for _, line := range strings.Split(string(existing), "\n") {
		if t := strings.TrimSpace(line); t == ".scribe/" || t == "/.scribe/" || t == ".scribe" {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	var b strings.Builder
	b.Write(existing)
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("# Scribe session state\n.scribe/\n")
	_ = os.WriteFile(path, []byte(b.String()), 0o644)
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolve turns "provider/model" into an agent.Model.
func (a *App) resolve(ref string) (agent.Model, error) {
	p, id, err := a.Registry.Resolve(ref)
	if err != nil {
		return agent.Model{}, err
	}
	info := a.Registry.ModelInfo(ref)
	if info.ID == "" {
		info.ID = id
	}
	prov, _ := llm.SplitModelRef(ref)
	if prov == "" {
		prov = p.ID()
	}
	return agent.Model{Ref: prov + "/" + id, ID: id, Provider: p, Info: info}, nil
}

// coreToolNames are always offered to the model. Every other registered tool
// is deferred (request_tools) when deferred_tools is on.
var coreToolNames = []string{
	"read", "edit_file", "multi_edit", "write_file", "grep", "glob", "shell", "project",
	"find_symbol", "code_map", "memory", "dispatch", "task_list", "plan", "update_plan_step",
	"ask_user", "ast_edit", "final_response",
}

func (a *App) systemPrompt(sub string) func(string) string {
	return func(mode string) string {
		a.refreshCodeMap()
		a.mu.Lock()
		in := agent.PromptInput{
			Workdir: a.Workdir, Date: a.date, Branch: a.branch,
			Instructions: a.instr, CodeMap: a.codeMap, Extra: a.opts.System, Subagent: sub,
		}
		a.mu.Unlock()
		// Both lookups are cached briefly inside the exec package.
		in.Commands = toolexec.ProjectCommandsLine(in.Workdir)
		if a.Memory != nil {
			in.MemoryIndex = a.Memory.Index() // counts only; recall does the searching
		}
		in.GitContext = toolexec.GitContextBlock(in.Workdir)
		if a.Agent != nil {
			in.ModelRef = a.Agent.CurrentModel().Ref
		}
		if a.Skills != nil && a.Cfg.AgentFeatures.Enabled("agent_skills") {
			in.Skills = a.Skills.Catalog()
		}
		// Subagents get every tool they can use, so only the lead lists the
		// deferred ones.
		if a.deferred != nil && sub == "" {
			in.Deferred = agent.DeferredCatalog(a.Tools, a.deferred.IsCore)
		}
		// The lead's loaded skills are latched into the history instead
		// (skills_latch.go), so its system prompt does not change when they
		// do. Subagents have no history to latch into, so they get them here.
		if sub != "" {
			return agent.BuildSystem(in, mode) + a.activeSkillsSection()
		}
		return agent.BuildSystem(in, mode)
	}
}

func (a *App) env() tools.Env {
	e := tools.Env{Workdir: a.Workdir, Host: a.host, Services: a.services, Claims: tabClaims{a}}
	if a.Checks != nil {
		e.Files = a.Checks
	} else {
		e.Files = newMemTracker()
	}
	return e
}

func (a *App) activeTools() map[string]bool {
	if len(a.Cfg.DisabledTools) == 0 {
		return nil
	}
	off := map[string]bool{}
	for _, n := range a.Cfg.DisabledTools {
		off[n] = true
	}
	act := map[string]bool{}
	for _, n := range a.Tools.Names() {
		if !off[n] {
			act[n] = true
		}
	}
	return act
}

func (a *App) agentOptions(model agent.Model, mode string) agent.Options {
	o := agent.Options{
		Model:     model,
		Tools:     a.Tools,
		Active:    a.activeTools(),
		Env:       a.env(),
		System:    a.systemPrompt(""),
		Mode:      mode,
		MaxTokens: a.Cfg.MaxTokens,
		Reasoning: a.Cfg.Reasoning,
		Tune:      a.tune,
		Redact:    redact.Default().Redact,
		Sink:      a.emit,
		MaxSteps:  a.opts.MaxSteps,
		// Zero keeps the agent's defaults (config.Retry).
		MaxRetries:      a.Cfg.Retry.Transient,
		MaxStallRetries: a.Cfg.Retry.Stall,
		Hooks:           a.claimHooks(a.ext.hooks),
		Headers:         map[string]string{"X-Session-Id": a.Meta.ID},
		Deferred:        a.deferred,
		// The dispatcher is the guard: while subagents run, the shell and git
		// tools refuse repository changes (agent publishes it to Services).
		Guard: a.Dispatch,
	}
	o.RequestDump = a.requestDump()
	applyWatchdog(&o, a.Cfg.WatchdogTimeouts)
	if config.Bool(a.Cfg.AutoCheck, true) {
		o.Verify = autoVerify(a.Workdir)
	}
	if a.ext.postEdit != nil {
		o.AfterTool = a.ext.postEdit.AfterTool
		o.BeforeTool = a.ext.postEdit.BeforeTool
	}
	for _, ref := range a.Cfg.Router.Fallback {
		if m, err := a.resolve(ref); err == nil {
			o.Fallback = append(o.Fallback, m)
		}
	}
	// Per-model fallback chains (router.fallback_by_model) replace the shared
	// list for the model they name.
	for primary, refs := range a.Cfg.Router.FallbackByModel {
		var chain []agent.Model
		for _, ref := range refs {
			if m, err := a.resolve(ref); err == nil {
				chain = append(chain, m)
			}
		}
		if o.FallbackFor == nil {
			o.FallbackFor = map[string][]agent.Model{}
		}
		o.FallbackFor[primary] = chain
	}
	// compact_strategy "disabled" turns compaction off; v1 and v2 choose the
	// summary style (see agent.SummaryCompactor).
	compactOn := config.Bool(a.Cfg.AutoCompact, true) && a.Cfg.CompactStrategy != "disabled"
	if compactOn {
		cm := model
		if a.Cfg.Router.Compact != "" {
			if m, err := a.resolve(a.Cfg.Router.Compact); err == nil {
				cm = m
			}
		}
		o.Compactor = agent.SummaryCompactor{Model: cm, Strategy: a.Cfg.CompactStrategy}
		o.CompactAt = a.Cfg.CompactAt
	} else {
		o.CompactAt = -1
	}
	if a.Memory != nil {
		o.Recall = func(_ context.Context, prompt string) string {
			return a.Memory.Inject(prompt, a.recallFiles(prompt))
		}
	}
	if !a.opts.Ephemeral {
		o.Persist = a.persist
	}
	return o
}

// subagentTools is the tool set a subagent of role gets: explore agents
// read only, web agents search and fetch only, code agents everything but
// the lead's planning and dispatch tools.
func (a *App) subagentTools(role string) *tools.Registry {
	reg := tools.NewRegistry()
	for _, n := range a.Tools.Names() {
		t, _ := a.Tools.Get(n)
		switch n {
		case "dispatch", "plan", "update_plan_step", "ask_user", "task_list", "memory":
			continue
		}
		if role == "explore" && !t.ReadOnly() {
			continue
		}
		if role == "web" && n != "web_search" && n != "fetch_page" {
			continue
		}
		if role == "explore" && n == "read" {
			t = capReadLines{Tool: t, max: exploreReadLines}
		}
		reg.Register(t)
	}
	if role == "explore" && a.ext.jobs != nil {
		// Read-only checks for explore and review agents: the project's own
		// check, with no fix and no write path.
		reg.Register(readOnlyProjectTool(a.ext.jobs))
	}
	return reg
}

// dispatchFeatures maps the config agent_features switches onto the
// dispatcher. Unset keys keep their documented defaults (config.AgentFeatures).
func (a *App) dispatchFeatures() *agent.Features {
	f := a.Cfg.AgentFeatures
	return &agent.Features{
		Desloppify:           f.Enabled("desloppify"),
		TierRouting:          f.Enabled("tier_routing"),
		DispatchCache:        f.Enabled("dispatch_cache"),
		TargetFileValidation: f.Enabled("target_file_validation"),
		VerifyEdits:          f.Enabled("verify_edits"),
	}
}

// exploreReadLines caps one read call made by an explore subagent (spec 1.3).
const exploreReadLines = 750

// capReadLines wraps the read tool for explore subagents so one call never
// returns more than max lines. An absent or larger limit is set to max; a
// smaller one is kept.
type capReadLines struct {
	tools.Tool
	max int
}

func (t capReadLines) Run(ctx context.Context, env *tools.Env, raw json.RawMessage) tools.Result {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return t.Tool.Run(ctx, env, raw)
	}
	cur := 0
	if v, ok := args["limit"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil {
			cur = n
		} else {
			var str string
			if json.Unmarshal(v, &str) == nil {
				fmt.Sscan(str, &cur)
			}
		}
	}
	if cur < 1 || cur > t.max {
		b, _ := json.Marshal(t.max)
		args["limit"] = b
		if re, err := json.Marshal(args); err == nil {
			raw = re
		}
	}
	return t.Tool.Run(ctx, env, raw)
}

// newSubagent builds the subagent a spec describes: its role sets the tools,
// steps and budget, its purpose and role together pick the model slot.
func (a *App) newSubagent(spec agent.SubagentSpec, sink agent.Sink) *agent.Agent {
	role, id := spec.Role, spec.ID
	model := a.Agent.CurrentModel()
	if ref := subagentRef(a.Cfg.Router, spec, a.Cfg.AgentFeatures.Enabled("tier_routing")); ref != "" {
		if m, err := a.resolve(ref); err == nil {
			model = m
		}
	}
	reg := a.subagentTools(role)
	if spec.Bus != nil {
		if spec.ReadCache {
			reg = withReadCacheFor(reg, spec.Bus, spec.ID)
		}
		for _, t := range agent.BusTools(id, spec.Bus) {
			reg.Register(t)
		}
	}
	steps := agent.ExploreMaxSteps
	switch role {
	case "code":
		steps = agent.CodeMaxSteps
	case "web":
		steps = agent.WebMaxSteps
	}
	mode := "default"
	if role == "explore" {
		mode = "architect" // read-only gate as a second line of defence
	}
	// Subagents get the parent's reasoning effort; explore runs at low effort
	// (spec 03 §3.4). Their budgets nudge them to wrap up (spec 03 §3.5).
	reasoning := a.Agent.Reasoning()
	if role == "explore" && reasoning != "" {
		reasoning = "low"
	}
	var budget *agent.StepBudget
	window := subagentWindow(model.Info.ContextWindow)
	switch role {
	case "explore":
		budget = &agent.StepBudget{Kind: "explore", NudgeAt: 8, Window: window}
	case "code":
		budget = &agent.StepBudget{Kind: "code", NudgeAt: 12, Window: window}
	}
	return agent.New(agent.Options{
		ID:        id,
		Model:     model,
		Tools:     reg,
		Env:       a.env(),
		System:    a.systemPrompt(role),
		Mode:      mode,
		MaxTokens: a.Cfg.MaxTokens,
		Reasoning: reasoning,
		Sink: func(e agent.Event) {
			e.AgentID = id
			sink(e)
			a.emit(e)
		},
		MaxSteps:        steps,
		MaxRetries:      spec.MaxRetries,
		MaxStallRetries: spec.MaxStallRetries,
		CompactAt:       -1,
		RetryBase:       2 * time.Second, // subagents back off from 2 s (spec 03 §10)
		Budget:          budget,
		// Peer findings reach each step, not only check_findings calls.
		Peers: agent.PeerFeed(id, spec.Bus),
		Hooks: a.subagentClaimHooks(a.ext.hooks),
	})
}

// editedFilesSnapshot returns the files this session has changed, sorted.
func (a *App) editedFilesSnapshot() []string {
	a.editedMu.Lock()
	defer a.editedMu.Unlock()
	out := make([]string, 0, len(a.editedFiles))
	for f := range a.editedFiles {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// noteEdited records that the session changed rel (relative to the workdir).
func (a *App) noteEdited(rel string) {
	a.editedMu.Lock()
	defer a.editedMu.Unlock()
	if a.editedFiles == nil {
		a.editedFiles = map[string]bool{}
	}
	a.editedFiles[filepath.ToSlash(rel)] = true
}

// Subscribe adds an event listener (TUI, headless printer, Lectern bridge).
func (a *App) Subscribe(s agent.Sink) {
	a.mu.Lock()
	a.listeners = append(a.listeners, s)
	a.mu.Unlock()
}

func (a *App) emit(e agent.Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	a.mu.Lock()
	ls := append([]agent.Sink(nil), a.listeners...)
	a.mu.Unlock()
	for _, l := range ls {
		l(e)
	}
	a.record(e)
	// Compaction drops the turns that carried recalled memories, so they may
	// be recalled again (a skipped compaction replaced nothing).
	if e.Kind == agent.EvCompactDone && e.AgentID == "" && a.Memory != nil {
		if skipped, _ := e.Meta["skipped"].(bool); !skipped {
			_ = a.Memory.ResetSurfaced()
		}
	}
	if e.Kind == agent.EvPlan && e.Plan != nil && !a.opts.Ephemeral {
		if b, err := json.Marshal(e.Plan); err == nil {
			_ = a.Store.SavePlan(a.Meta.ID, b)
		}
	}
}

// record appends the event to the session's UI transcript, coalescing
// text and reasoning deltas so the file stays small.
func (a *App) record(e agent.Event) {
	if a.opts.Ephemeral || e.Kind == agent.EvToolArgs || e.Kind == agent.EvToolProgress || e.Kind == agent.EvContext {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []json.RawMessage
	if (e.Kind == agent.EvText || e.Kind == agent.EvReasoning) && (a.pendKind == "" || (a.pendKind == e.Kind && a.pendAgent == e.AgentID)) {
		a.pendKind, a.pendAgent = e.Kind, e.AgentID
		a.pending.WriteString(e.Text)
		return
	}
	if a.pendKind != "" {
		b, _ := json.Marshal(agent.Event{Kind: a.pendKind, AgentID: a.pendAgent, Text: a.pending.String(), At: e.At})
		out = append(out, b)
		a.pending.Reset()
		a.pendKind = ""
	}
	if e.Kind == agent.EvText || e.Kind == agent.EvReasoning {
		a.pendKind, a.pendAgent = e.Kind, e.AgentID
		a.pending.WriteString(e.Text)
	} else {
		b, _ := json.Marshal(e)
		out = append(out, b)
	}
	if len(out) > 0 {
		a.countUILocked()
		if a.Store.AppendUI(a.Meta.ID, out...) == nil {
			a.uiCount += len(out)
		}
	}
}

// countUILocked loads the number of saved UI records once, so later appends
// can keep the count without reading the file. The caller holds a.mu.
func (a *App) countUILocked() {
	if a.uiCounted || a.Store == nil {
		return
	}
	if recs, err := a.Store.LoadUI(a.Meta.ID); err == nil {
		a.uiCount = len(recs)
	}
	a.uiCounted = true
}

// markTurnUI records where the UI log stands as turn starts, after writing
// any text still buffered from the turn before, so sending from an earlier
// checkpoint can cut the transcript there too (BranchFrom).
func (a *App) markTurnUI(turn int) {
	if a.opts.Ephemeral || a.Store == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendKind != "" {
		b, _ := json.Marshal(agent.Event{Kind: a.pendKind, AgentID: a.pendAgent, Text: a.pending.String()})
		a.pending.Reset()
		a.pendKind = ""
		a.countUILocked()
		if a.Store.AppendUI(a.Meta.ID, b) == nil {
			a.uiCount++
		}
	}
	a.countUILocked()
	if a.uiMarks == nil {
		a.uiMarks = map[int]int{}
	}
	a.uiMarks[turn] = a.uiCount
}

// cutUIAfter drops the UI records of the turns after turn, when the start of
// the first dropped turn was recorded in this process. It reports whether it
// cut.
func (a *App) cutUIAfter(turn int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	first, mark := 0, -1
	for t, m := range a.uiMarks {
		if t > turn && (first == 0 || t < first) {
			first, mark = t, m
		}
	}
	if mark < 0 || a.Store.TruncateUI(a.Meta.ID, mark) != nil {
		return false
	}
	a.uiCount = mark
	for t := range a.uiMarks {
		if t > turn {
			delete(a.uiMarks, t)
		}
	}
	return true
}

// Transcript returns the recorded events of this session.
func (a *App) Transcript() []agent.Event {
	if a.opts.Ephemeral {
		return nil
	}
	raw, err := a.Store.LoadUI(a.Meta.ID)
	if err != nil {
		return nil
	}
	out := make([]agent.Event, 0, len(raw))
	for _, r := range raw {
		var e agent.Event
		if json.Unmarshal(r, &e) == nil {
			out = append(out, e)
		}
	}
	return out
}

// persist saves message history (Options.Persist).
func (a *App) persist(msgs []llm.Message, rewritten bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	saved, _ := a.Store.LoadMessages(a.Meta.ID)
	if rewritten || len(saved) > len(msgs) {
		_ = a.Store.ReplaceMessages(a.Meta.ID, msgs)
		return
	}
	if len(msgs) > len(saved) {
		_ = a.Store.AppendMessages(a.Meta.ID, msgs[len(saved):])
	}
}

// Run executes one user turn synchronously.
func (a *App) Run(ctx context.Context, prompt string, images []llm.Part) (agent.TurnStats, error) {
	a.mu.Lock()
	a.turn++
	turn := a.turn
	a.mu.Unlock()
	a.markTurnUI(turn)
	if a.Checks != nil {
		a.Checks.Begin(turn, prompt)
	}
	if !a.opts.Ephemeral {
		_ = session.AppendHistory(prompt, a.Workdir)
	}
	a.Agent.SetSkills(a.loadedSkillsBlock())
	stats, err := a.runTurnBudgeted(ctx, prompt, images)
	if a.Checks != nil {
		_ = a.Checks.End()
	}
	a.ext.afterTurn(a, stats)
	a.extractMemories(prompt, stats) // opt-in; runs in the background
	a.saveMeta(prompt, stats)
	return stats, err
}

func (a *App) saveMeta(prompt string, stats agent.TurnStats) {
	if a.opts.Ephemeral {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.Meta.Title == "" {
		a.Meta.Title = session.DeriveTitle(prompt)
	}
	a.Meta.Turns = a.turn
	a.Meta.Tokens, a.Meta.CostUSD = a.Agent.Usage()
	a.Meta.Model = a.Agent.CurrentModel().Ref
	a.Meta.Mode = a.Agent.Mode()
	a.Meta.Updated = time.Now()
	_ = a.Store.SaveMeta(&a.Meta)
}

// SetModel switches model.
func (a *App) SetModel(ref string) error {
	m, err := a.resolve(ref)
	if err != nil {
		return err
	}
	a.Agent.SetModel(m)
	a.mu.Lock()
	a.Meta.Model = m.Ref
	a.mu.Unlock()
	// A model the user picked ranks higher in the picker next time (spec
	// frecency). A failed write only costs the ranking.
	_ = session.RecordPick("model", m.Ref)
	if !a.opts.Ephemeral {
		_ = a.Store.SaveMeta(&a.Meta)
	}
	return nil
}

// Close releases everything.
func (a *App) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	hooks := a.closeHooks
	a.mu.Unlock()
	for _, f := range hooks {
		f()
	}
	a.Agent.Abort()
	fileClaims.release(a.claimOwner)
	a.ext.close(a)
	if a.Memory != nil {
		_ = a.Memory.Close()
	}
	return nil
}

// OnClose registers f to run once when the app closes. It runs before the
// agent is aborted, so approvals still waiting can be denied with a reason.
func (a *App) OnClose(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closeHooks = append(a.closeHooks, f)
}

// SessionID returns the session id ("" for ephemeral).
func (a *App) SessionID() string { return a.Meta.ID }

// Opts returns the options the app was opened with.
func (a *App) Opts() Options { return a.opts }

// headlessHost answers prompts without a user.
type headlessHost struct{}

func (headlessHost) Approve(_ context.Context, ap tools.Approval) tools.Decision {
	if ap.Kind == "web" || ap.Kind == "mcp" {
		return tools.AllowSession
	}
	return tools.Deny
}

func (headlessHost) Ask(_ context.Context, q tools.Question) (string, error) {
	if q.Kind == "plan_review" {
		return "Execute", nil
	}
	if len(q.Options) > 0 {
		return q.Options[0], nil
	}
	return "No user is available; use your best judgement.", nil
}

func (headlessHost) Progress(string, string) {}

// memTracker is a FileTracker for ephemeral sessions.
type memTracker struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func newMemTracker() *memTracker { return &memTracker{seen: map[string]time.Time{}} }

func (m *memTracker) BeforeWrite(string) {}
func (m *memTracker) MarkRead(abs string) {
	st, err := os.Stat(abs)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.seen[abs] = st.ModTime()
	}
}
func (m *memTracker) StaleSinceRead(abs string) bool {
	m.mu.Lock()
	t, ok := m.seen[abs]
	m.mu.Unlock()
	if !ok {
		return false
	}
	st, err := os.Stat(abs)
	return err == nil && !st.ModTime().Equal(t)
}
func (m *memTracker) WasRead(abs string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.seen[abs]
	return ok
}
