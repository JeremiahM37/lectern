// Package app wires every component together. Both the binary and the test suite
// build an App from a Config, so nothing is only ever exercised in production.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/alerts"
	"github.com/JeremiahM37/lectern/v2/internal/api"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/awareness"
	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/budget"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/checks"
	"github.com/JeremiahM37/lectern/v2/internal/claims"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/creds"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/push"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
	"github.com/JeremiahM37/lectern/v2/internal/triggers"
)

// App owns every long-lived component.
type App struct {
	Cfg       *config.Config
	DB        *store.DB
	Bus       *bus.Bus
	Notifier  *sinks.Notifier
	Broker    *broker.Broker
	Reg       *executor.Registry
	Sched     *scheduler.Scheduler
	Sessions  *sessions.Manager
	Memory    memory.Provider
	Terminals *terminal.Manager
	Triggers  *triggers.Manager
	Server    *api.Server
	Log       *slog.Logger
}

// New builds the whole service and starts its scheduler.
func New(cfg *config.Config, log *slog.Logger) (*App, error) {
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	b := bus.New()
	// Phone alerts must work with zero manual setup: when no VAPID env vars
	// are set, ResolveKeys loads (or, on first start, generates and persists)
	// a key pair from the database instead of leaving push permanently
	// disabled — see internal/push/keys.go.
	pushSender, err := push.ResolveKeys(db, cfg.VAPIDPrivateKey, cfg.VAPIDPublicKey, cfg.VAPIDEmail, log)
	if err != nil {
		db.Close()
		return nil, err
	}
	notifier := &sinks.Notifier{DB: db, BaseURL: cfg.BaseURL, Push: pushSender, Log: log}
	br := broker.New(db, b, notifier, cfg.ApprovalExpire)
	reg := executor.NewRegistry(cfg.Mock, cfg.MockAgentDelay)
	provisioner := creds.New(cfg.ClaudeCredsPath, cfg.CodexCredsPath, cfg.AnthropicAPIKey, log)
	var mem memory.Provider = memory.None{}
	if cfg.GrimoireURL != "" {
		provider := memory.NewGrimoire(cfg.GrimoireURL, cfg.GrimoireToken)
		if err := provider.ConfigureContext(cfg.GrimoireContextMode, cfg.GrimoireContextProjects); err != nil {
			db.Close()
			return nil, err
		}
		mem = provider
	}
	sessMgr := sessions.New(db, reg, b, sessions.Launcher{
		ClaudeBin: cfg.ClaudeBin, CodexBin: cfg.CodexBin, GeminiBin: cfg.GeminiBin,
	}, mem, log)
	sessMgr.WorktreeNamespace = cfg.WorktreeNamespace
	sessMgr.HandoffPoll = cfg.HandoffPoll
	// cfg.HookBase already defaults to cfg.BaseURL in config.Load(), but a
	// hand-built config.Config{} (every test in this repo) does not go
	// through Load() and leaves both zero — fall back explicitly so a test
	// harness that sets only BaseURL (to its own ephemeral listener) still
	// gets a hook callback URL that actually reaches it.
	sessMgr.HookBase = firstNonEmptyString(cfg.HookBase, cfg.BaseURL)
	// the agent set is the operator's, read fresh so a change takes effect
	// without a restart
	sessMgr.Specs = func() []sessions.Spec { return sessions.ParseSpecs(db.Setting("agents")) }
	sched := scheduler.New(db, b, br, notifier, reg, cfg, provisioner, log)
	sched.AgentDefinitions = func() map[string]agents.TaskDefinition {
		out := map[string]agents.TaskDefinition{}
		for _, raw := range sessMgr.Specs() {
			resolved := sessMgr.Resolve(raw)
			if resolved.Builtin {
				out[resolved.Name] = agents.TaskDefinition{Name: resolved.Name, Builtin: true}
				continue
			}
			if resolved.Task == nil && resolved.ACP == nil {
				continue
			}
			def := agents.TaskDefinition{Name: resolved.Name, ModelFlag: resolved.ModelFlag, Env: cloneStringMap(resolved.Env)}
			if resolved.Task != nil {
				command := resolved.Task.Command
				if command == "" {
					command = resolved.Command
				}
				def.Command = command
				def.Args = append([]string(nil), resolved.Task.Args...)
				def.PromptTemplate = resolved.Task.PromptTemplate
				def.OutputMode = resolved.Task.OutputMode
				def.PermissionArgs = cloneArgs(resolved.Task.PermissionArgs)
				def.ResumeArgs = append([]string(nil), resolved.Task.ResumeArgs...)
			}
			if resolved.ACP != nil {
				def.ACP = &agents.ACPDefinition{
					Command: resolved.ACP.Command,
					Args:    append([]string(nil), resolved.ACP.Args...),
					Env:     cloneStringMap(resolved.ACP.Env),
				}
			}
			out[resolved.Name] = def
		}
		return out
	}
	sched.Sessions = sessMgr
	sched.Memory = mem
	terms := terminal.NewManager()
	events := agentevents.New(db, b)
	activity := alerts.NewActivity()
	alertWatcher := &alerts.Watcher{DB: db, Notifier: notifier, Activity: activity}
	// Wired here rather than duplicating the hook-event plumbing: every
	// session-lifecycle push (docs/agent-events.md section 3) rides the
	// same ingest path session state itself does.
	events.OnHookEvent = alertWatcher.HandleHookEvent

	// checksRunner is the one place a project's check command (verify_cmd, or
	// an auto-detected .verify.yaml) actually runs — for a task's finished
	// attempt (wired into the scheduler below) and for a session's Stop
	// event/screen-idle fallback (wired into events.Stop and sessMgr.Checks).
	// *Runner satisfies agentevents.StopListener structurally; nothing here
	// imports the other way.
	checksRunner := checks.New(db, reg, b, notifier, cfg.CheckTimeout, log)
	sched.Checks = checksRunner
	sessMgr.Checks = checksRunner
	events.Stop = checksRunner

	// awarenessTracker is cross-agent awareness's whole backend (see
	// internal/awareness's package doc and docs/agent-events.md
	// "Cross-agent awareness"): peer lookups, briefing/warning text, and
	// the background repo-key resolution the hook handlers kick off.
	awarenessTracker := awareness.New(db, reg, log)

	// claimsTracker is the Claim board's whole backend (internal/claims,
	// docs/claims.md): create/release/extend, overlap queries, briefing/
	// warning text and the periodic sweep. Wired alongside awarenessTracker
	// since claims lean on awareness's repo-key resolution.
	claimsTracker := claims.New(db, log)

	authResolver := auth.New(auth.Settings{
		Mode: cfg.Auth, Host: cfg.Host, Token: cfg.AuthToken, Socket: cfg.TailscaleSocket,
		AllowedUsersCSV: cfg.TailscaleUsers, AllowedTagsCSV: cfg.TailscaleTags,
		TrustServeHeaders: cfg.TrustServeHeaders,
	}, log)

	// triggersMgr polls GitHub/Linear and holds Slack's Socket Mode
	// connections open (internal/triggers). CreateTask is wired below, once
	// srv exists, to the same task-creation-plus-dispatch path a human's
	// "New task" + dispatch button uses.
	triggersMgr := triggers.New(db, reg, log)

	srv := &api.Server{
		DB: db, Bus: b, Broker: br, Notifier: notifier, Reg: reg, Sched: sched,
		Terminals: terms, Push: pushSender, Cfg: cfg, Auth: authResolver, Log: log,
		Sessions: sessMgr, Events: events, Memory: mem, Checks: checksRunner, Activity: activity,
		Awareness: awarenessTracker, Claims: claimsTracker, Triggers: triggersMgr,
	}
	triggersMgr.CreateTask = srv.CreateTriggerTask
	// a routine is a saved task, so the API layer owns firing it; the scheduler
	// only says when one is due
	sched.Routines = func(ctx context.Context) { srv.RunDueRoutines(ctx); srv.ScheduleAutonomyTick(ctx) }
	// an eval cell is a task too — same reasoning
	sched.Evals = srv.RunEvalsTick
	// Budgets (docs/budgets.md): spend-limit/quota/anomaly threshold alerts,
	// on the scheduler's own tick like Routines/Evals above. Enforcement
	// (budget.Gate, the per-task cancel in scheduler.poll) needs no wiring —
	// it reads the DB directly — only the periodic alerts need this.
	budgetChecker := &budget.Checker{DB: db, Notifier: notifier}
	sched.Budgets = budgetChecker.Tick
	// a trigger-created task is a task too — same reasoning again; Tick also
	// reconciles Slack's live sockets and posts back finished tasks
	sched.Triggers = triggersMgr.Tick
	// Claim board sweep (docs/claims.md): releases lapsed-TTL, finished-
	// attempt and dead-session claims once per tick.
	sched.Claims = func(context.Context) {
		if _, err := claimsTracker.Sweep(); err != nil {
			log.Warn("claims sweep failed", "err", err)
		}
	}

	app := &App{Cfg: cfg, DB: db, Bus: b, Notifier: notifier, Broker: br, Reg: reg,
		Sched: sched, Sessions: sessMgr, Memory: mem, Terminals: terms,
		Triggers: triggersMgr, Server: srv, Log: log}

	if cfg.Mock {
		if err := app.SeedDemoData(); err != nil {
			return nil, err
		}
	}
	// The upgrade preparer leaves a private manifest in the systemd environment.
	// Import it after store.Open has applied normal migrations, but before the
	// startup checkpoint workers or the first recovery poll can observe rows.
	if checkpoint := strings.TrimSpace(os.Getenv("LECTERN_CHECKPOINT")); checkpoint != "" {
		report, importErr := sessions.ImportCheckpoint(checkpoint, cfg.DBPath)
		if importErr != nil {
			log.Warn("session checkpoint import incomplete", "path", checkpoint, "err", importErr)
		} else {
			log.Info("session checkpoint imported", "path", checkpoint, "imported", report.Imported, "skipped", len(report.Skipped))
			for _, skipped := range report.Skipped {
				log.Warn("session checkpoint row skipped", "session_id", skipped.ID, "reason", skipped.Reason)
				if skipped.Reason == "native identity unavailable" {
					// Keep an unidentifiable pre-restart row visible for inspection;
					// it must not enter boot recovery as if its old conversation were
					// safely resumable.
					_, _ = db.Exec("UPDATE sessions SET status=?, updated_at=? WHERE id=? AND ended_at IS NULL AND status<>?", sessions.StatusInterrupted, store.Now(), skipped.ID, sessions.StatusInterrupted)
				}
			}
		}
	}
	// Install native checkpoint workers from the local DB before exposing the
	// server. Remote boot probes and recovery launches remain asynchronous in the
	// scheduler, so one unreachable SSH target cannot delay API availability.
	err = sessMgr.PrepareStartup()
	if err != nil {
		log.Warn("session startup checkpoint setup incomplete", "err", err)
	}
	sched.Start()
	return app, nil
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func cloneArgs(in map[string][]string) map[string][]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]string, len(in))
	for k, values := range in {
		out[k] = append([]string(nil), values...)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Handler is the HTTP handler for this app.
func (a *App) Handler() http.Handler { return a.Server.Handler() }

// Close stops the scheduler and releases the database.
func (a *App) Close() {
	if a.Sched != nil {
		a.Sched.Stop()
	}
	if a.Sessions != nil {
		a.Sessions.Close()
	}
	if a.Triggers != nil {
		a.Triggers.StopSlack()
	}
	a.Server.Shutdown(context.Background())
	a.DB.Close()
}

// SeedDemoData gives mock mode a ready board, so the UI and the e2e suite always
// have something to show.
func (a *App) SeedDemoData() error {
	if targets, err := a.DB.Targets(); err != nil || len(targets) > 0 {
		return err
	}
	t1, err := a.DB.InsertTarget(&store.Target{
		Name: "lxc-101-project-env", Kind: "mock", Host: "192.0.2.10",
		Status: "online", MaxConcurrent: 4, Port: 22, User: "root"})
	if err != nil {
		return err
	}
	t2, err := a.DB.InsertTarget(&store.Target{
		Name: "aiserver-local", Kind: "mock", Status: "online",
		MaxConcurrent: 8, Port: 22, User: "root"})
	if err != nil {
		return err
	}
	if _, err := a.DB.InsertProject(&store.Project{
		Name: "demo-app", TargetID: t1.ID, RepoPath: "/mock/demo-app"}); err != nil {
		return err
	}
	_, err = a.DB.InsertProject(&store.Project{
		Name: "homelab-api", TargetID: t2.ID, RepoPath: "/mock/homelab-api"})
	return err
}
