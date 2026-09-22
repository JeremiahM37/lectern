package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/delegation"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Delegated builds: the lead plans and reviews, a cheaper worker builds.
//
//	GET  /api/delegation          settings + whether the worker is usable
//	PUT  /api/delegation          settings
//	POST /api/delegation/preset   install the DeepSeek Flash worker into the registry
//	POST /api/delegation/check    one small real request to the worker

func (s *Server) delegationSettings() delegation.Settings {
	return delegation.Load(s.DB.Setting)
}

// delegationView is what the UI and the MCP server see: the settings plus a
// verdict on the worker, so "on" never silently means "on, but nothing will
// run".
func (s *Server) delegationView() map[string]any {
	cfg := s.delegationSettings()
	out := map[string]any{"settings": cfg, "worker_ready": false, "worker_problem": ""}
	if cfg.WorkerAgent == "" {
		out["worker_problem"] = "no worker agent chosen"
		return out
	}
	spec, ok := s.taskAgent(cfg.WorkerAgent)
	if !ok {
		out["worker_problem"] = fmt.Sprintf("agent %q has no task definition", cfg.WorkerAgent)
		return out
	}
	if err := taskPermissionError(spec, cfg.PermissionMode); err != nil {
		out["worker_problem"] = err.Error()
		return out
	}
	out["worker_ready"] = true
	out["worker_command"] = spec.Task.Command
	if out["worker_command"] == "" {
		out["worker_command"] = spec.Command
	}
	// The board's Orchestrate entry is only offered when a description typed
	// there would actually run: the feature is on and the worker answers.
	out["orchestrate_ready"] = cfg.Enabled
	return out
}

// orchestrationLead is who runs an orchestrated task: the request's agent
// and model when given, else the configured lead, else the project's
// default. It is refused when nothing could run behind it.
type orchestrationLead struct {
	agent, model string
	cycles       int
}

func (s *Server) orchestrationLead(project *store.Project, agent *string, model string) (orchestrationLead, error) {
	view := s.delegationView()
	cfg := s.delegationSettings()
	if !cfg.Enabled {
		return orchestrationLead{}, fmt.Errorf("orchestration needs Delegated builds ON: turn it on in Settings (the big card at the top) and choose a worker")
	}
	if ready, _ := view["worker_ready"].(bool); !ready {
		return orchestrationLead{}, fmt.Errorf("delegated builds is on but the worker is not runnable: %v", view["worker_problem"])
	}
	lead := orchestrationLead{agent: strOr(agent, orDefault(cfg.LeadAgent, orDefault(project.DefaultAgent, "claude"))),
		model: orDefault(model, cfg.LeadModel), cycles: cfg.CorrectionCycles}
	if !delegation.LeadAgentAllowed(lead.agent) {
		return orchestrationLead{}, fmt.Errorf("the lead must be Claude Code or Codex (got %q): it is launched with the Lectern MCP server, which only those can attach", lead.agent)
	}
	target, err := s.DB.Target(project.TargetID)
	if err != nil {
		return orchestrationLead{}, err
	}
	if !scheduler.HostLocalKinds[target.Kind] {
		return orchestrationLead{}, fmt.Errorf("orchestration runs on local targets only (project %q is on %s): the lead's MCP server is this Lectern binary", project.Name, target.Kind)
	}
	return lead, nil
}

func (s *Server) getDelegation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.delegationView())
}

func (s *Server) putDelegation(w http.ResponseWriter, r *http.Request) {
	cfg := s.delegationSettings()
	var in struct {
		Enabled          *bool   `json:"enabled"`
		WorkerAgent      *string `json:"worker_agent"`
		WorkerModel      *string `json:"worker_model"`
		PermissionMode   *string `json:"permission_mode"`
		CorrectionCycles *int    `json:"correction_cycles"`
		LeadAgent        *string `json:"lead_agent"`
		LeadModel        *string `json:"lead_model"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.LeadAgent != nil {
		cfg.LeadAgent = strings.TrimSpace(*in.LeadAgent)
	}
	if in.LeadModel != nil {
		cfg.LeadModel = strings.TrimSpace(*in.LeadModel)
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.WorkerAgent != nil {
		cfg.WorkerAgent = strings.TrimSpace(*in.WorkerAgent)
	}
	if in.WorkerModel != nil {
		cfg.WorkerModel = strings.TrimSpace(*in.WorkerModel)
	}
	if in.PermissionMode != nil {
		cfg.PermissionMode = strings.TrimSpace(*in.PermissionMode)
	}
	if in.CorrectionCycles != nil {
		cfg.CorrectionCycles = *in.CorrectionCycles
	}
	if err := cfg.Validate(); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	if cfg.Enabled {
		if _, ok := s.taskAgent(cfg.WorkerAgent); !ok {
			httpError(w, 400, "agent %q cannot run tasks; pick one with a task definition (the preset installs one)", cfg.WorkerAgent)
			return
		}
	}
	if err := s.DB.SetSetting(delegation.SettingKey, cfg.Encode()); err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "delegation", s.delegationView())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.delegationView())
}

// installDelegationPreset upserts the DeepSeek Flash worker into the agent
// registry and selects it, leaving every other entry untouched. The key is
// optional on a re-run: an existing entry keeps the one it has.
func (s *Server) installDelegationPreset(w http.ResponseWriter, r *http.Request) {
	var in struct {
		APIKey string `json:"api_key"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	specs := s.agentSpecs()
	key := strings.TrimSpace(in.APIKey)
	var kept []sessions.Spec
	for _, sp := range specs {
		if sp.Builtin {
			continue
		}
		if sp.Name == delegation.PresetAgent {
			if key == "" {
				key = sp.Env["DEEPSEEK_API_KEY"]
			}
			continue
		}
		kept = append(kept, sp)
	}
	if key == "" {
		httpError(w, 400, "a DeepSeek API key is required the first time")
		return
	}
	kept = append(kept, delegation.PresetSpec(key))
	raw, err := json.Marshal(kept)
	if err != nil {
		respondErr(w, err)
		return
	}
	body, err := sessions.NormalizeBuiltinEntries(string(raw))
	if err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	if err := sessions.ValidateSpecs(body); err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	if err := s.DB.SetSetting("agents", body); err != nil {
		respondErr(w, err)
		return
	}
	cfg := s.delegationSettings()
	cfg.WorkerAgent = delegation.PresetAgent
	if cfg.WorkerModel == "" {
		cfg.WorkerModel = delegation.PresetModel
	}
	if err := s.DB.SetSetting(delegation.SettingKey, cfg.Encode()); err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "delegation", s.delegationView())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.delegationView())
}

// checkDelegationWorker runs the worker once on the local target with a
// prompt whose answer is fixed, so the settings card can say "the worker
// answers" rather than "the settings were saved".
func (s *Server) checkDelegationWorker(w http.ResponseWriter, r *http.Request) {
	cfg := s.delegationSettings()
	spec, ok := s.taskAgent(cfg.WorkerAgent)
	if !ok {
		httpError(w, 400, "no runnable worker agent")
		return
	}
	targets, err := s.DB.Targets()
	if err != nil {
		respondErr(w, err)
		return
	}
	var local *store.Target
	for i := range targets {
		if targets[i].Kind == "local" {
			local = targets[i]
			break
		}
	}
	if local == nil {
		httpError(w, 400, "the check needs a local target")
		return
	}
	ex, err := s.Reg.For(local)
	if err != nil {
		respondErr(w, err)
		return
	}
	command := spec.Task.Command
	if command == "" {
		command = spec.Command
	}
	parts := []string{executor.ShellQuote(command)}
	for _, a := range spec.Task.Args {
		parts = append(parts, executor.ShellQuote(a))
	}
	if cfg.WorkerModel != "" && spec.ModelFlag != "" {
		parts = append(parts, executor.ShellQuote(spec.ModelFlag), executor.ShellQuote(cfg.WorkerModel))
	}
	var env []string
	for k, v := range spec.Env {
		env = append(env, k+"="+executor.ShellQuote(v))
	}
	prefix := ""
	if len(env) > 0 {
		prefix = "env " + strings.Join(env, " ") + " "
	}
	marker := fmt.Sprintf("LECTERN-WORKER-%d", time.Now().UnixNano()%100000)
	// A throwaway git repository: CLIs refuse to run outside a trusted
	// checkout, and the check must not run inside anyone's real one.
	script := `d=$(mktemp -d) && cd "$d" && git init -q && ` + prefix + strings.Join(parts, " ") + " " + executor.ShellQuote("Reply with exactly: "+marker) + ` < /dev/null 2>&1 | tail -c 4000; rm -rf "$d"`
	started := time.Now()
	res, err := ex.Run(r.Context(), script, executor.RunOpts{Timeout: 180})
	out := map[string]any{"ok": false, "seconds": time.Since(started).Seconds()}
	if err != nil {
		out["error"] = err.Error()
	} else {
		out["ok"] = strings.Contains(res.Stdout, marker)
		out["tail"] = tail(res.Stdout, 600)
	}
	writeJSON(w, 200, out)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
