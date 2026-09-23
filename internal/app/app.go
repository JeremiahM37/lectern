// Package app wires every component together. Both the binary and the test suite
// build an App from a Config, so nothing is only ever exercised in production.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/api"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
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
	pushSender := &push.Sender{
		PrivateKey: cfg.VAPIDPrivateKey, PublicKey: cfg.VAPIDPublicKey,
		Email: cfg.VAPIDEmail, Log: log,
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
			if resolved.Task == nil {
				continue
			}
			command := resolved.Task.Command
			if command == "" {
				command = resolved.Command
			}
			out[resolved.Name] = agents.TaskDefinition{
				Name: resolved.Name, Command: command, Args: append([]string(nil), resolved.Task.Args...),
				ModelFlag: resolved.ModelFlag, PromptTemplate: resolved.Task.PromptTemplate, OutputMode: resolved.Task.OutputMode,
				PermissionArgs: cloneArgs(resolved.Task.PermissionArgs),
				ResumeArgs:     append([]string(nil), resolved.Task.ResumeArgs...),
				Env:            cloneStringMap(resolved.Env),
			}
		}
		return out
	}
	sched.Sessions = sessMgr
	sched.Memory = mem
	terms := terminal.NewManager()

	authResolver := auth.New(auth.Settings{
		Mode: cfg.Auth, Host: cfg.Host, Token: cfg.AuthToken, Socket: cfg.TailscaleSocket,
		AllowedUsersCSV: cfg.TailscaleUsers, AllowedTagsCSV: cfg.TailscaleTags,
	}, log)

	srv := &api.Server{
		DB: db, Bus: b, Broker: br, Notifier: notifier, Reg: reg, Sched: sched,
		Terminals: terms, Push: pushSender, Cfg: cfg, Auth: authResolver, Log: log,
		Sessions: sessMgr, Memory: mem,
	}
	// a routine is a saved task, so the API layer owns firing it; the scheduler
	// only says when one is due
	sched.Routines = srv.RunDueRoutines

	app := &App{Cfg: cfg, DB: db, Bus: b, Notifier: notifier, Broker: br, Reg: reg,
		Sched: sched, Sessions: sessMgr, Memory: mem, Terminals: terms,
		Server: srv, Log: log}

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
