package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// sessionView is a session plus the two things a card always needs: how long it
// has been quiet, and whether a handoff is currently being written.
type sessionView struct {
	LaunchProfile string                `json:"launch_profile,omitempty"`
	CanRestore    bool                  `json:"can_restore"`
	Workspace     *worktree.Interactive `json:"workspace,omitempty"`
	*store.Session
	IdleSeconds     float64 `json:"idle_seconds"`
	UptimeSeconds   float64 `json:"uptime_seconds"`
	HandoffInFlight bool    `json:"handoff_in_flight"`
	// HandoffPhase is saving/starting while a switch runs, so a reloaded page
	// can say where the context is without guessing from timing.
	HandoffPhase       string `json:"handoff_phase,omitempty"`
	HandoffDestination string `json:"handoff_destination,omitempty"`
	HandoffError       string `json:"handoff_error,omitempty"`
	// Lineage links the conversations a switch produced. PredecessorID is the
	// session whose wrap started this one; SuccessorID is the session this one
	// handed off to, once that session exists.
	PredecessorID *int64 `json:"predecessor_id,omitempty"`
	SuccessorID   *int64 `json:"successor_id,omitempty"`
	Wraps         int    `json:"wraps"`
	// LastCheck summarises the most recent session_checks row (internal/checks)
	// for the card badge — nil until the session's first check runs.
	LastCheck *lastCheckView `json:"last_check,omitempty"`
}

// lastCheckView is the badge-sized summary of a session's most recent check;
// the full row (including output_tail) is GET /api/sessions/{id}/checks.
type lastCheckView struct {
	Status     string   `json:"status"`
	FinishedAt *float64 `json:"finished_at"`
	Command    string   `json:"command"`
}

// recentSessionView keeps the ordinary session representation while making the
// one dangerous distinction visible: a released adopted session is ended in
// the database but its tmux process was deliberately left running.
type recentSessionView struct {
	*sessionView
	Released         bool   `json:"released"`
	CanResumeRecent  bool   `json:"can_resume_recent"`
	ResumeRecentURL  string `json:"resume_recent_url,omitempty"`
	ConversationsURL string `json:"conversations_url"`
}

func (s *Server) sessionView(row *store.Session) *sessionView {
	v := &sessionView{
		Session:         row,
		CanRestore:      row.EndedAt != nil && row.Origin == "discovered" && row.Status != sessions.StatusDead && row.TrackingIdentity != "",
		IdleSeconds:     sessions.IdleFor(row).Seconds(),
		UptimeSeconds:   store.Now() - row.CreatedAt,
		HandoffInFlight: s.Sessions.InFlight(row.ID),
	}
	if row.WorktreeJSON != "" {
		json.Unmarshal([]byte(row.WorktreeJSON), &v.Workspace)
		if v.Workspace != nil {
			v.Workspace.RedactOwnership()
		}
	}
	if row.LaunchConfigJSON != "" {
		var cfg sessions.LaunchConfiguration
		if json.Unmarshal([]byte(row.LaunchConfigJSON), &cfg) == nil {
			v.LaunchProfile = cfg.ProfileName
		}
	}
	if wraps, err := s.DB.SessionWraps(row.ID); err == nil {
		v.Wraps = len(wraps)
		for _, wrap := range wraps {
			if wrap.NextSessionID != nil {
				v.SuccessorID = wrap.NextSessionID
				break
			}
		}
	}
	if wrap, err := s.DB.WrapPredecessor(row.ID); err == nil && wrap != nil {
		predecessor := wrap.SessionID
		v.PredecessorID = &predecessor
	}
	if status, ok := s.Sessions.Handoff(row.ID); ok {
		v.HandoffPhase, v.HandoffDestination = status.Phase, status.Destination
	} else {
		v.HandoffError = s.Sessions.HandoffError(row.ID)
	}
	if last, err := s.DB.LatestSessionCheck(row.ID); err == nil && last != nil {
		v.LastCheck = &lastCheckView{Status: last.Status, FinishedAt: last.FinishedAt, Command: last.Command}
	}
	return v
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	archived := r.URL.Query().Get("archived") == "true"
	setupFailures := r.URL.Query().Get("include_setup_failures") == "true"
	all := archived || r.URL.Query().Get("all") == "true"
	rows, err := s.DB.Sessions(all || setupFailures)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]*sessionView, 0, len(rows))
	for _, row := range rows {
		if setupFailures && !all && row.EndedAt != nil && row.SetupState != "failed" {
			continue
		}
		if (row.ArchivedAt != nil) != archived {
			continue
		}
		out = append(out, s.sessionView(row))
	}
	writeJSON(w, 200, out)
}

func (s *Server) recentSessions(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			httpError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	rows, err := s.DB.RecentClosedSessions(limit)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]*recentSessionView, 0, len(rows))
	for _, row := range rows {
		view := s.sessionView(row)
		out = append(out, &recentSessionView{
			sessionView:      view,
			Released:         row.Origin == "discovered" && row.Status != sessions.StatusDead,
			CanResumeRecent:  s.canResumeRecent(row),
			ResumeRecentURL:  fmt.Sprintf("/api/sessions/%d/resume-recent", row.ID),
			ConversationsURL: fmt.Sprintf("/api/sessions/%d/conversations", row.ID),
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// canResumeRecent reports whether the convenience action has enough durable
// local evidence to be useful. It intentionally does not probe the target on
// a listing request: the POST performs the authoritative, target-side history
// validation before it launches anything.
func (s *Server) canResumeRecent(row *store.Session) bool {
	if row.EndedAt == nil || row.ArchivedAt != nil || (row.NativeRecoveryCID == "" && row.ResumeID == "") {
		return false
	}
	// A released adopted row is ended in SQLite while its terminal remains
	// alive. It can be restored/tracked, but launching a continuation would
	// race that process and is therefore not offered by Recent.
	if row.Origin == "discovered" && row.Status != sessions.StatusDead {
		return false
	}
	if row.Agent != "claude" && row.Agent != "codex" {
		return false
	}
	config, err := s.Sessions.SessionLaunchConfiguration(row)
	return err == nil && len(config.Spec.ResumeIDArgs) > 0
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.sessionView(row))
}

type sessionIn struct {
	Background bool                         `json:"background"`
	ProfileID  int64                        `json:"profile_id"`
	GroupPath  string                       `json:"group_path"`
	Worktree   *worktree.InteractiveOptions `json:"worktree"`
	ProjectID  *int64                       `json:"project_id"`
	TargetID   int64                        `json:"target_id"`
	Name       string                       `json:"name"`
	Agent      string                       `json:"agent"`
	Model      string                       `json:"model"`
	Workdir    string                       `json:"workdir"`
	// Resume picks the agent's own previous conversation back up, which is what
	// you want when re-opening a project you were in yesterday.
	Resume bool   `json:"resume"`
	Prime  string `json:"prime"`
	// Brief prepends what the memory provider knows about the project, so a
	// fresh session starts with the project's knowledge rather than a blank slate.
	Brief bool `json:"brief"`
	// Scratch starts the agent in a fresh throwaway directory instead of a
	// project — an empty room. What it becomes is decided later, by promoting it.
	Scratch bool `json:"scratch"`
	// Yolo runs the agent without its approval prompts. A pointer because absent
	// means "the default", which is ON for interactive sessions: you are sitting
	// in the terminal watching it. Send false to be asked for confirmations.
	Yolo *bool `json:"yolo"`
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var in sessionIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.Agent != "" {
		if _, ok := sessions.Find(s.agentSpecs(), in.Agent); !ok {
			httpError(w, 422, "unknown agent %q — define it in /api/agents", in.Agent)
			return
		}
	}
	// A project owns its target. Callers may omit target_id, but an explicit
	// different target is ambiguous and must not launch the project elsewhere.
	if in.ProjectID != nil {
		proj, err := s.DB.Project(*in.ProjectID)
		if err != nil {
			httpError(w, 400, "no such project")
			return
		}
		if in.TargetID != 0 && in.TargetID != proj.TargetID {
			httpError(w, 409, "target_id does not match project target")
			return
		}
		in.TargetID = proj.TargetID
	}
	// a session needs somewhere to work: a project's repository, a directory you
	// name, or an explicit blank room. Saying none of the three is a mistake,
	// and it is clearer to say so here than to fail later at launch.
	if in.ProjectID == nil && in.Workdir == "" && !in.Scratch {
		httpError(w, 400, "a session needs a project, a workdir, or scratch:true for a blank room")
		return
	}
	if in.TargetID == 0 {
		// "open a blank session with codex" names no project and no host, and
		// almost always means here — so default to the control plane itself,
		// falling back to the only target when there is just one.
		in.TargetID = s.defaultTargetID()
	}
	if in.TargetID == 0 {
		httpError(w, 400, "a session needs a project or a target")
		return
	}
	if _, err := s.DB.Target(in.TargetID); err != nil {
		httpError(w, 400, "no such target")
		return
	}
	prime := in.Prime
	if in.Brief && in.ProjectID != nil {
		if proj, err := s.DB.Project(*in.ProjectID); err == nil {
			if brief := s.projectBrief(r.Context(), proj); brief != "" {
				prime = strings.TrimSpace(brief + "\n\n" + prime)
			}
		}
	}
	// yolo is on unless the caller says otherwise
	yolo := true
	if in.Yolo != nil {
		yolo = *in.Yolo
	}
	launch, status := s.Sessions.Launch, 201
	if in.Background {
		launch, status = s.Sessions.LaunchBackground, 202
	}
	sess, err := launch(r.Context(), sessions.LaunchOpts{
		ProfileID: in.ProfileID,
		GroupPath: in.GroupPath, Worktree: in.Worktree, ProjectID: in.ProjectID, TargetID: in.TargetID, Name: in.Name,
		Agent: in.Agent, Model: in.Model, Workdir: in.Workdir,
		Resume: in.Resume, Prime: prime, Scratch: in.Scratch, Yolo: yolo,
		// The manager layers project defaults before the selected launch profile.
	})
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, status, s.sessionView(sess))
}

// onboardingDocs are the files a project uses to tell a newcomer where it is.
//
// Naming them matters more than it looks: an agent only auto-reads the file its
// own CLI knows about — Claude Code reads CLAUDE.md, codex reads AGENTS.md — so
// switching agents on a project silently drops whatever the other one was
// reading. A brief that names all of them makes the handover agent-agnostic.
var onboardingDocs = []string{
	"HANDOFF.md", "AGENTS.md", "CLAUDE.md", "GOAL.md", "STATUS.md",
	"DESIGN.md", "ARCHITECTURE.md", "README.md",
}

// repoDocs asks the target which onboarding documents this project actually has.
func (s *Server) repoDocs(ctx context.Context, proj *store.Project) []string {
	target, err := s.DB.Target(proj.TargetID)
	if err != nil {
		return nil
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil
	}
	var b strings.Builder
	for _, name := range onboardingDocs {
		fmt.Fprintf(&b, "[ -f %s ] && echo %s; ",
			shellq.Quote(proj.RepoPath+"/"+name), shellq.Quote(name))
	}
	r, err := ex.Run(ctx, b.String()+"true", executor.RunOpts{Timeout: 30})
	if err != nil {
		return nil
	}
	var found []string
	for _, line := range strings.Split(r.Stdout, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			found = append(found, line)
		}
	}
	return found
}

// projectBrief is everything a fresh agent should start from: the repo's own
// onboarding docs, the durable notes previous agents left, the last session's
// handoff, and whatever the memory provider knows.
//
// It is ordered by how load-bearing each source is. The repo's own HANDOFF.md is
// first because it is the one the project maintains deliberately; recalled facts
// are last because they are the least specific.
func (s *Server) projectBrief(ctx context.Context, proj *store.Project) string {
	brief, _ := s.projectBriefWithMemory(ctx, proj)
	return brief
}

func (s *Server) projectBriefWithMemory(ctx context.Context, proj *store.Project) (string, memory.Brief) {
	var parts []string

	if docs := s.repoDocs(ctx, proj); len(docs) > 0 {
		var b strings.Builder
		b.WriteString("## Read these first\n\nThis project documents its own state. " +
			"Read them in " + proj.RepoPath + " before anything else:\n")
		for _, d := range docs {
			b.WriteString("- " + d + "\n")
		}
		parts = append(parts, strings.TrimRight(b.String(), "\n"))
	}

	if wraps, err := s.DB.Wraps(proj.ID, 1); err == nil && len(wraps) > 0 {
		parts = append(parts, "## Where the last session left off\n\n"+
			strings.TrimSpace(wraps[0].Summary))
	}

	if notes, err := s.DB.ProjectNotes(proj.ID, 10); err == nil && len(notes) > 0 {
		var b strings.Builder
		b.WriteString("## Notes previous agents left on this project\n\n")
		// oldest first, so it reads chronologically
		for i := len(notes) - 1; i >= 0; i-- {
			b.WriteString("- " + strings.TrimSpace(notes[i].Note) + "\n")
		}
		parts = append(parts, strings.TrimRight(b.String(), "\n"))
	}

	if _, automatic := s.Memory.(memory.AutomaticProvider); automatic {
		return strings.Join(parts, "\n\n"), memory.Brief{
			Provider: s.Memory.Name(), Status: "deferred",
			Message: "Automatic context follows the assigned project's memory policy at launch.", Facts: []memory.Fact{},
		}
	}
	recalled := memory.LoadBrief(ctx, s.Memory, proj.Name)
	if block := recalled.Prompt(); block != "" {
		parts = append(parts, block)
	}
	return strings.Join(parts, "\n\n"), recalled
}

// previewBrief shows exactly what a new session on this project would be handed.
// Worth having as its own endpoint: "does this agent actually have the context"
// should be answerable before you spend a session finding out.
func (s *Server) previewBrief(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	proj, err := s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	brief, recalled := s.projectBriefWithMemory(r.Context(), proj)
	if provider, ok := s.Memory.(memory.ProjectProvider); ok {
		result, lookupErr := provider.AutomaticProject(r.Context(), proj.Name, proj.MemoryTopic, "", nil)
		recalled.Status, recalled.Message = "empty", "No relevant memory found in the configured project scope."
		if lookupErr != nil {
			recalled.Status, recalled.Message = "unavailable", "Memory unavailable; work can continue."
			brief = recalled.Message + "\n" + brief
		} else if result.Context != "" {
			recalled.Status, recalled.Message = "ready", "Project-scoped memory loaded."
			brief = result.Context + "\n" + brief
		}
		if grimoire, ok := s.Memory.(*memory.Grimoire); ok {
			mode := grimoire.ContextScope(proj.Name).Mode
			if mode == "manual" || mode == "off" {
				recalled.Status, recalled.Message = "disabled", "Automatic memory is disabled for this project."
			}
		}
		if proj.MemoryStatus == "unavailable" {
			recalled.Status, recalled.Message = "unavailable", "Project memory setup failed; retry POST /api/projects/{id}/memory."
		}
		brief = memory.ProjectHint(s.Memory, proj.Name, proj.MemoryTopic) + brief
	}
	writeJSON(w, 200, map[string]any{
		"project": proj.Name, "brief": brief, "chars": len(brief), "memory": recalled,
		"docs": s.repoDocs(r.Context(), proj),
	})
}

type sessionPatch struct {
	GroupPath *string `json:"group_path"`
	// ProjectID reassigns a session. Raw, because "move it to project 4",
	// "unassign it" (an explicit null) and "leave it alone" (absent) are three
	// different requests, and a *int64 collapses the last two — an agent's
	// working directory is often a scratch dir, so which project it belongs to
	// is a judgement only the operator can make, including "none".
	ProjectID json.RawMessage `json:"project_id"`
	Name      *string         `json:"name"`
	Model     *string         `json:"model"`
}

func (s *Server) patchSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var p sessionPatch
	if err := decodeBody(r, &p); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{}
	if p.GroupPath != nil {
		group, err := sessions.NormalizeGroup(*p.GroupPath)
		if err != nil {
			httpError(w, 422, "%s", err)
			return
		}
		fields["group_path"] = group
	}
	if len(p.ProjectID) > 0 {
		if string(p.ProjectID) == "null" {
			fields["project_id"] = nil
		} else {
			var id int64
			if err := json.Unmarshal(p.ProjectID, &id); err != nil {
				httpError(w, 422, "project_id must be a number or null")
				return
			}
			if _, err := s.DB.Project(id); err != nil {
				httpError(w, 400, "no such project")
				return
			}
			fields["project_id"] = id
		}
	}
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		fields["name"] = strings.TrimSpace(*p.Name)
	}
	if p.Model != nil {
		fields["model"] = *p.Model
	}
	if len(fields) > 0 {
		fields["updated_at"] = store.Now()
		if err := s.DB.Update("sessions", row.ID, fields); err != nil {
			respondErr(w, err)
			return
		}
	}
	fresh, err := s.DB.Session(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "session", fresh)
	writeJSON(w, 200, s.sessionView(fresh))
}

type sendIn struct {
	Text string `json:"text"`
	Key  string `json:"key"`
}

// sendToSession is the phone-side keyboard: type a message, or press one of the
// allowlisted keys (Escape interrupts a turn), without attaching a terminal.
func (s *Server) sendToSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.EndedAt != nil {
		httpError(w, 409, "this session is untracked; restore tracking first")
		return
	}
	var in sendIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if len(in.Text) > 32000 {
		httpError(w, 422, "message is too long (maximum 32000 bytes)")
		return
	}
	if row.Status == sessions.StatusDead || row.EndedAt != nil {
		httpError(w, 409, "this session is ended or untracked; restore tracking first")
		return
	}
	switch {
	case in.Key != "":
		if err := s.Sessions.SendKey(r.Context(), row.ID, in.Key); err != nil {
			httpError(w, 422, "%s", err.Error())
			return
		}
	case strings.TrimSpace(in.Text) != "":
		if err := s.Sessions.SendText(r.Context(), row.ID, in.Text); err != nil {
			httpError(w, 409, "%s", err.Error())
			return
		}
	default:
		httpError(w, 400, "send needs text or key")
		return
	}
	writeJSON(w, 200, map[string]any{"sent": true})
}

func (s *Server) attachSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.SetupState == "creating" {
		httpError(w, 409, "workspace is setting up; attach becomes available when setup finishes")
		return
	}
	if row.SetupState == "failed" {
		httpError(w, 409, "workspace setup failed; inspect its error and retained files before launching again")
		return
	}
	if row.Status == sessions.StatusDead || row.EndedAt != nil {
		httpError(w, 409, "this session is ended or untracked; restore tracking first")
		return
	}
	target, err := s.DB.Target(row.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	port, err := s.Terminals.Attach(r.Context(), terminal.Attachment{
		Key:         fmt.Sprintf("session:%d", row.ID),
		TmuxSession: row.TmuxSession,
	}, target)
	if err != nil {
		httpError(w, 503, "%s", err.Error())
		return
	}
	// url is what the UI opens: same-origin, so it works through nginx, over the
	// tailnet, and on a phone. port stays for older clients and for debugging.
	writeJSON(w, 200, map[string]any{"port": port,
		"url":          fmt.Sprintf("/term/session/%d/", row.ID),
		"tmux_session": row.TmuxSession})
}

// deleteSession stops tracking a session, and kills its process ONLY when
// lectern owns that process.
//
// The asymmetry is the whole point. A session lectern launched is ours to end.
// A session the operator started themselves — the long-running conversation they
// have had open for a week — is not: adopting it was supposed to be
// non-destructive, so un-adopting it must be too. Killing one requires saying so
// explicitly with ?kill=true.
//
// This is not hypothetical. A bulk re-adopt during development called DELETE on
// seven adopted sessions and terminated seven live Claude conversations, because
// the default was "kill" for everything.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.SetupState == "creating" {
		httpError(w, 409, "workspace setup is still running; inspect its progress before stopping the session")
		return
	}
	kill := r.URL.Query().Get("kill") == "true"
	if row.EndedAt != nil {
		if kill {
			httpError(w, 409, "this record is no longer tracked; restore tracking or explicitly adopt the running session first")
			return
		}
		writeJSON(w, 200, map[string]any{"id": row.ID, "killed": false})
		return
	}
	owned := row.Origin != "discovered"
	var err error
	switch {
	case row.Status == sessions.StatusDead:
		err = s.Sessions.Dismiss(row.ID) // nothing to kill; drop the card
	case kill || owned:
		err = s.Sessions.Kill(r.Context(), row.ID)
	default:
		// adopted and no explicit kill: let go of it, leave it running
		err = s.Sessions.Release(r.Context(), row.ID)
	}
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"id": row.ID, "killed": kill || (owned && row.Status != sessions.StatusDead)})
}

type handoffIn struct {
	Successor   bool   `json:"successor"`
	KillOld     bool   `json:"kill_old"`
	Agent       string `json:"agent"`
	Model       string `json:"model"`
	ProfileID   int64  `json:"profile_id"`
	QuickSwitch bool   `json:"quick_switch"`
}

// handoffSession asks a session to write its wrap. It returns immediately: an
// agent mid-turn can take minutes to answer, and the operator should not hold a
// request open to discover that.
func (s *Server) handoffSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.Status == sessions.StatusDead {
		httpError(w, 409, "this session has ended — nothing left to ask")
		return
	}
	var in handoffIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.QuickSwitch && (row.Agent == "shell" || !in.Successor) {
		httpError(w, 422, "quick switch requires an agent session and a successor")
		return
	}
	if in.Agent != "" {
		if _, ok := sessions.Find(s.agentSpecs(), in.Agent); !ok {
			httpError(w, 422, "unknown agent %q — define it in /api/agents", in.Agent)
			return
		}
	}
	wraps, err := s.DB.SessionWraps(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	var afterWrapID int64
	for _, wrap := range wraps {
		if wrap.ID > afterWrapID {
			afterWrapID = wrap.ID
		}
	}
	if err := s.Sessions.StartHandoff(row.ID, sessions.HandoffOpts{
		Successor: in.Successor, KillOld: in.KillOld,
		Agent: in.Agent, Model: in.Model, ProfileID: in.ProfileID, QuickSwitch: in.QuickSwitch}); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 202, map[string]any{"started": true, "session_id": row.ID, "after_wrap_id": afterWrapID})
}

func (s *Server) sessionWraps(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	wraps, err := s.DB.SessionWraps(row.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, wraps)
}

// projectWraps is a project's handoff thread — the continuity that survives
// every individual conversation.
func (s *Server) projectWraps(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	wraps, err := s.DB.Wraps(id, 50)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, wraps)
}

func (s *Server) discoverSessions(w http.ResponseWriter, r *http.Request) {
	found, err := s.Sessions.Discover(r.Context())
	if err != nil {
		respondErr(w, err)
		return
	}
	if found == nil {
		found = []sessions.Candidate{}
	}
	writeJSON(w, 200, found)
}

type adoptIn struct {
	TargetID    int64  `json:"target_id"`
	TmuxSession string `json:"tmux_session"`
	ProjectID   *int64 `json:"project_id"`
	Name        string `json:"name"`
	Agent       string `json:"agent"`
	Model       string `json:"model"`
	Workdir     string `json:"workdir"`
}

func (s *Server) adoptSession(w http.ResponseWriter, r *http.Request) {
	var in adoptIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	sess, err := s.Sessions.Adopt(r.Context(), sessions.AdoptOpts{
		TargetID: in.TargetID, TmuxSession: in.TmuxSession, ProjectID: in.ProjectID,
		Name: in.Name, Agent: in.Agent, Model: in.Model, Workdir: in.Workdir,
	})
	if errors.Is(err, sessions.ErrAlreadyAdopted) {
		httpError(w, 409, "%s", err.Error())
		return
	}
	if err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	writeJSON(w, 201, s.sessionView(sess))
}

func (s *Server) sessionParam(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such session")
		return nil, false
	}
	row, err := s.DB.Session(id)
	if err != nil {
		httpError(w, 404, "no such session")
		return nil, false
	}
	return row, true
}

// agentSpecs is the operator's agent set: the built-ins plus anything defined in
// settings.
func (s *Server) agentSpecs() []sessions.Spec {
	return sessions.ParseSpecs(s.DB.Setting("agents"))
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.agentViews(s.agentSpecs()))
}

// putAgents replaces the custom agent definitions. Built-ins are always present;
// a custom entry with a built-in's name overrides it, which is how a CLI whose
// flags have drifted gets corrected without a release.
func (s *Server) putAgents(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	if err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		body = "[]"
	}
	body, err = s.agentConfigWithRetainedSecrets(body)
	if err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	body, err = sessions.NormalizeBuiltinEntries(body)
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
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.agentViews(s.agentSpecs()))
}

// defaultTargetID picks where a session with no project and no named host should
// run. Returns 0 when the answer is genuinely ambiguous, so the caller asks.
func (s *Server) defaultTargetID() int64 {
	targets, err := s.DB.Targets()
	if err != nil || len(targets) == 0 {
		return 0
	}
	if len(targets) == 1 {
		return targets[0].ID
	}
	for _, t := range targets {
		// mock mode pretends every target is this machine, so the demo board can
		// open a blank session without first asking where "here" is
		if t.Kind == "local" || (s.Cfg.Mock && t.Kind == "mock") {
			return t.ID
		}
	}
	// several remote hosts and no local one: which machine is a real question,
	// so let the caller answer it rather than guessing
	return 0
}

// ---- promoting a scratch session into a project ------------------------

type promoteIn struct {
	// ProjectID adopts the session into a project that already exists. When it
	// is absent a new project is created from where the session has been working.
	ProjectID *int64 `json:"project_id"`
	Name      string `json:"name"`
	RepoPath  string `json:"repo_path"`
	// Wrap asks the agent to write down the state of the work as it is promoted,
	// so the new project starts with a record of what happened in the scratch
	// session rather than an empty history.
	Wrap bool `json:"wrap"`
	// Expected is returned by the read-only preview. Native promotion rechecks
	// every field immediately before linking the unchanged live session.
	Expected *promotionIdentity `json:"expected_identity"`
}

// promoteSession turns work that started in a blank room into a project.
//
// The point of a scratch session is that you do not have to decide what it is
// before you start. This is where you decide afterwards: the directory the agent
// has been working in becomes the project's repository, so nothing moves and the
// conversation carries straight on.
func (s *Server) promoteSession(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	var in promoteIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if sess.ProjectID != nil && in.ProjectID == nil {
		httpError(w, 409, "this session already belongs to a project")
		return
	}
	originalTracking := ""
	if in.Expected != nil {
		originalTracking = sess.TrackingIdentity
		if in.Wrap {
			httpError(w, 422, "native promotion cannot wrap the live conversation")
			return
		}
		got, err := s.resolvePromotionIdentity(r, sess)
		if err != nil {
			httpError(w, 409, "%s", err)
			return
		}
		if got != *in.Expected || got.TargetID != sess.TargetID || got.TmuxSession != sess.TmuxSession || got.TrackingIdentity != sess.TrackingIdentity {
			httpError(w, 409, "the terminal's native process or conversation changed; refresh the promotion preview")
			return
		}
		if originalTracking == "" {
			identity, e := s.Sessions.EnsureTrackingIdentity(r.Context(), sess)
			if e != nil {
				httpError(w, 409, "%s", e)
				return
			}
			sess.TrackingIdentity = identity
			got.TrackingIdentity = identity
			confirmed, e := s.resolvePromotionIdentity(r, sess)
			if e != nil || confirmed != got {
				httpError(w, 409, "the terminal changed while establishing tracking; refresh the promotion preview")
				return
			}
			in.Expected = &confirmed
		}
		// A quick shell may have cd'd since it was created. The verified native
		// process cwd is the project directory that promotion adopts.
		sess.Workdir = got.Workspace
		sess.Agent = got.Agent
		// Preserve the native CLI's explicit home for future history/recovery.
		// The value is a path only; no environment contents are copied.
		if spec, ok := sessions.Find(s.agentSpecs(), got.Agent); ok {
			env := make(map[string]string, len(spec.Env)+1)
			for k, v := range spec.Env {
				env[k] = v
			}
			spec.Env = env
			if got.Agent == "claude" {
				spec.Env["CLAUDE_CONFIG_DIR"] = in.Expected.NativeHome
			} else {
				spec.Env["CODEX_HOME"] = in.Expected.NativeHome
			}
			cfg, _ := json.Marshal(sessions.LaunchConfiguration{Version: 1, Spec: spec})
			sess.LaunchConfigJSON = string(cfg)
		}
		if in.RepoPath != "" && cleanPromotionPath(in.RepoPath) != cleanPromotionPath(sess.Workdir) {
			httpError(w, 422, "repo_path must equal the verified native working directory")
			return
		}
		// The detected CLI owns the continuation, even though the quick-shell
		// row itself is intentionally recorded as agent=shell.
		in.Name = strings.TrimSpace(in.Name)
	}

	var project *store.Project
	var err error
	atomicBound := false
	if in.Expected != nil && in.ProjectID != nil {
		candidate, e := s.DB.Project(*in.ProjectID)
		if e != nil {
			httpError(w, 404, "no such project")
			return
		}
		if candidate.TargetID != sess.TargetID || cleanPromotionPath(candidate.RepoPath) != cleanPromotionPath(sess.Workdir) {
			httpError(w, 409, "existing project must be on the same target and exact working directory")
			return
		}
	}
	if in.Expected != nil && in.ProjectID == nil {
		if projects, e := s.DB.Projects(); e == nil {
			for _, p := range projects {
				if p.TargetID == sess.TargetID && cleanPromotionPath(p.RepoPath) == cleanPromotionPath(sess.Workdir) {
					in.ProjectID = &p.ID
					break
				}
			}
		}
	}
	if in.Expected != nil && in.ProjectID == nil {
		name := strings.TrimSpace(in.Name)
		if name == "" {
			name = projectNameFromPath(sess.Workdir)
		}
		project, err = s.DB.PromoteNewProjectAndBind(r.Context(), &store.Project{Name: name, TargetID: sess.TargetID, RepoPath: sess.Workdir, DefaultBaseBranch: s.repoBranch(r.Context(), sess.TargetID, sess.Workdir), DefaultAgent: sess.Agent, KeepWorktrees: 3}, sess.ID, sess.TmuxSession, sess.BootID, in.Expected.BootID, originalTracking, in.Expected.TrackingIdentity, in.Expected.CID, sess.LaunchConfigJSON)
		atomicBound = err == nil
	} else {
		project, err = s.resolvePromotionTarget(r.Context(), sess, in)
	}
	if err != nil {
		if strings.Contains(err.Error(), "stale") {
			httpError(w, 409, "%s", err)
			return
		}
		respondErr(w, err)
		return
	}
	if !atomicBound {
		fields := map[string]any{"project_id": project.ID}
		if in.Expected != nil && in.ProjectID != nil {
			fields["workdir"] = sess.Workdir
			fields["agent"] = sess.Agent
			fields["native_recovery_cid"] = in.Expected.CID
			if sess.LaunchConfigJSON != "" {
				fields["launch_config_json"] = sess.LaunchConfigJSON
			}
		}
		var updateErr error
		if in.Expected != nil {
			res, e := s.DB.Exec(`UPDATE sessions SET project_id=?, workdir=?, agent=?, native_recovery_cid=?, launch_config_json=?, boot_id=?, tracking_identity=? WHERE id=? AND target_id=? AND ended_at IS NULL AND archived_at IS NULL AND project_id IS NULL AND tmux_session=? AND boot_id=? AND tracking_identity=?`, project.ID, sess.Workdir, sess.Agent, in.Expected.CID, sess.LaunchConfigJSON, in.Expected.BootID, in.Expected.TrackingIdentity, sess.ID, sess.TargetID, sess.TmuxSession, sess.BootID, originalTracking)
			if e != nil {
				updateErr = e
			} else if n, e := res.RowsAffected(); e != nil || n != 1 {
				updateErr = fmt.Errorf("promotion preview is stale; the session lifecycle changed")
			}
		} else {
			updateErr = s.DB.Update("sessions", sess.ID, fields)
		}
		if updateErr != nil {
			if strings.Contains(updateErr.Error(), "stale") {
				httpError(w, 409, "%s", updateErr)
				return
			}
			respondErr(w, updateErr)
			return
		}
	}
	if in.Expected != nil {
		s.Sessions.RestartNativeCheckpoint(sess.ID, sess.Agent, sess.Workdir, in.Expected.NativeHome, sess.TmuxSession)
		s.provisionProjectMemory(r.Context(), project)
	}
	// the work so far is worth keeping even if the wrap fails, so this is
	// deliberately after the link and its error is reported rather than fatal
	wrapErr := ""
	if in.Wrap {
		if err := s.Sessions.StartHandoff(sess.ID, sessions.HandoffOpts{}); err != nil {
			wrapErr = err.Error()
		}
	}
	fresh, err := s.DB.Session(sess.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "session_promoted", map[string]any{
		"session_id": sess.ID, "project_id": project.ID, "project_name": project.Name})
	s.Log.Info("session promoted to a project", "session", sess.ID,
		"project", project.Name, "repo", project.RepoPath)
	writeJSON(w, 200, map[string]any{
		"session": s.sessionView(fresh), "project": project, "wrap_error": wrapErr})
}

// resolvePromotionTarget finds or creates the project a session is promoted into.
func (s *Server) resolvePromotionTarget(ctx context.Context, sess *store.Session,
	in promoteIn) (*store.Project, error) {
	if in.ProjectID != nil {
		proj, err := s.DB.Project(*in.ProjectID)
		if err != nil {
			return nil, store.ErrNotFound
		}
		if in.Expected != nil && (proj.TargetID != sess.TargetID || cleanPromotionPath(proj.RepoPath) != cleanPromotionPath(sess.Workdir)) {
			return nil, executor.Errf("existing project must be on the same target and exact working directory")
		}
		return proj, nil
	}
	repo := strings.TrimRight(firstNonEmptyStr(in.RepoPath, sess.Workdir), "/")
	if repo == "" {
		return nil, executor.Errf("this session has no working directory to make a project from")
	}
	// promoting twice, or promoting a second session from the same directory,
	// must not leave two projects pointing at one repository
	if existing, err := s.DB.Projects(); err == nil {
		for _, p := range existing {
			if p.TargetID == sess.TargetID && cleanPromotionPath(p.RepoPath) == cleanPromotionPath(repo) {
				return p, nil
			}
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = projectNameFromPath(repo)
	}
	agent := sess.Agent
	if in.Expected != nil {
		agent = in.Expected.Agent
	}
	project, err := s.DB.InsertProject(&store.Project{
		Name: name, TargetID: sess.TargetID, RepoPath: repo,
		DefaultBaseBranch: s.repoBranch(ctx, sess.TargetID, repo),
		DefaultAgent:      agent, KeepWorktrees: 3,
	})
	if err == nil {
		s.provisionProjectMemory(ctx, project)
	}
	return project, err
}

// repoBranch asks the repository what branch it is actually on.
//
// Assuming "main" is wrong often enough to matter: `git init` still produces
// `master` on a default install, and a promoted project whose base branch does
// not exist cannot create a worktree, so its very first dispatch fails with
// "invalid reference".
func (s *Server) repoBranch(ctx context.Context, targetID int64, repo string) string {
	const fallback = "main"
	target, err := s.DB.Target(targetID)
	if err != nil {
		return fallback
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return fallback
	}
	r, err := ex.Run(ctx, fmt.Sprintf("git -C %s symbolic-ref --short HEAD",
		shellq.Quote(repo)), executor.RunOpts{Timeout: 20})
	if err != nil || !r.OK() {
		return fallback
	}
	if branch := strings.TrimSpace(r.Stdout); branch != "" {
		return branch
	}
	return fallback
}

// scratchSuffixRe matches the date and mktemp suffix makeScratch appends.
var scratchSuffixRe = regexp.MustCompile(`-[0-9]{8}-[A-Za-z0-9]{6}$`)

// projectNameFromPath names a project after its directory, minus the timestamp a
// scratch directory carries.
func projectNameFromPath(repo string) string {
	base := path.Base(repo)
	// a scratch directory is "notes-app-20260906-a4Kd9x"; the project is "notes-app"
	base = scratchSuffixRe.ReplaceAllString(base, "")
	if base == "" || base == "." || base == "/" {
		return "untitled project"
	}
	return base
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ClaudeModels are the shorthands Claude Code accepts. Every other agent names
// its models differently and the set moves, so there is nothing honest to
// hardcode for them — what you have actually used is the better suggestion.
var ClaudeModels = []string{"fable", "opus", "sonnet", "haiku"}

// listModels answers with model suggestions per agent.
//
// The model field is free text and always has been — any string is passed
// straight to the agent's model flag. This only drives the datalist, so a model
// missing from it was never blocked, just unsuggested. It is per-agent because
// offering Claude's shorthands under codex is worse than offering nothing.
func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	out := map[string][]string{}
	for _, spec := range s.agentSpecs() {
		if spec.ModelFlag == "" {
			continue // this agent has no model switch; the field is ignored
		}
		out[spec.Name] = []string{}
	}
	if _, ok := out["claude"]; ok {
		// Claude Code takes shorthands rather than a catalog, and offers no way
		// to ask, so these are the one list lectern does carry.
		out["claude"] = append([]string{}, ClaudeModels...)
	}
	// ask each agent that can be asked; a stale hardcoded list is exactly the
	// bug this replaces, so the tool's own answer wins
	for _, spec := range s.agentSpecs() {
		if _, wanted := out[spec.Name]; !wanted {
			continue
		}
		if found := s.probeModels(r.Context(), spec); len(found) > 0 {
			out[spec.Name] = found
		}
	}
	// whatever you have actually run, so a model you use once is offered forever
	for _, table := range []string{"sessions", "tasks"} {
		rows, err := s.DB.Query(`SELECT DISTINCT agent, model FROM ` + table +
			` WHERE COALESCE(model,'') != '' ORDER BY model`)
		if err != nil {
			continue
		}
		for rows.Next() {
			var agent, model string
			if rows.Scan(&agent, &model) != nil {
				continue
			}
			if agent == "" {
				agent = "claude"
			}
			if _, known := out[agent]; !known {
				continue
			}
			if !contains(out[agent], model) {
				out[agent] = append(out[agent], model)
			}
		}
		rows.Close()
	}
	writeJSON(w, 200, out)
}

// modelCacheTTL keeps the picker from shelling out to every agent on a remote
// target each time a sheet opens. Model line-ups do not change by the minute.
const modelCacheTTL = 10 * time.Minute

type modelCacheEntry struct {
	models []string
	at     time.Time
}

// probeModels asks an agent for its own catalog, on the control plane, cached.
//
// Failure is silent and returns nothing: an agent that is not installed, or a
// CLI whose catalog command changed, must degrade to "no suggestions" rather
// than break the form you were about to launch from.
func (s *Server) probeModels(ctx context.Context, spec sessions.Spec) []string {
	probe := s.Sessions.Resolve(spec).ModelsProbe()
	if probe == "" {
		return nil
	}
	s.modelMu.Lock()
	if s.modelCache == nil {
		s.modelCache = map[string]modelCacheEntry{}
	}
	if hit, ok := s.modelCache[spec.Name]; ok && time.Since(hit.at) < modelCacheTTL {
		s.modelMu.Unlock()
		return hit.models
	}
	s.modelMu.Unlock()

	target, err := s.DB.Target(s.defaultTargetID())
	if err != nil {
		return nil
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r, err := ex.Run(ctx, probe, executor.RunOpts{Timeout: 15})
	if err != nil || !r.OK() {
		return nil
	}
	models := sessions.ParseModelCatalog(r.Stdout)
	if len(models) == 0 {
		return nil
	}
	s.modelMu.Lock()
	s.modelCache[spec.Name] = modelCacheEntry{models: models, at: time.Now()}
	s.modelMu.Unlock()
	return models
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func (s *Server) restoreSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	restored, err := s.Sessions.Restore(r.Context(), row.ID)
	if err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 200, s.sessionView(restored))
}
