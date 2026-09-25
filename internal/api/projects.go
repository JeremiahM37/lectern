package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agents"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/isolation"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/skills"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

var (
	// "steerable" is claude-only: acceptEdits-equivalent permissions (no
	// PreToolUse gate), launched through the streaming-input driver so a
	// running task can receive a mid-run message instead of only a
	// cancel-and-redispatch follow-up. See internal/drivers.Select.
	permissionModes = []string{"default", "acceptEdits", "plan", "bypassPermissions", "steerable"}
	capProfiles     = []string{"restricted", "parity"}
)

type projectIn struct {
	SetupCmd          string  `json:"setup_cmd"`
	Name              string  `json:"name"`
	TargetID          int64   `json:"target_id"`
	RepoPath          string  `json:"repo_path"`
	DefaultBaseBranch *string `json:"default_base_branch"`
	WorkrootOverride  string  `json:"workroot_override"`
	VerifyCmd         string  `json:"verify_cmd"`
	KeepWorktrees     bool    `json:"keep_worktrees"`
	ReviewGate        bool    `json:"review_gate"`
	// Env is extra env for the agent — the any-model door (ANTHROPIC_BASE_URL &c).
	Env map[string]any `json:"env"`
	// ContextPaths stack on top of the target's bundle.
	ContextPaths []string       `json:"context_paths"`
	MCP          map[string]any `json:"mcp"`
	StrictMCP    bool           `json:"strict_mcp"`
	Permissions  map[string]any `json:"permissions"`
	GateMatcher  string         `json:"gate_matcher"`
	DefaultAgent *string        `json:"default_agent"`
	// CapabilityProfile 'parity' grants the tools, MCP servers and memory dir a
	// terminal session has; 'restricted' keeps only the rules set explicitly.
	CapabilityProfile *string `json:"capability_profile"`
	// DefaultPermissionMode '' means "use the task default" (acceptEdits). Set
	// 'default' to make every dispatch on this project stop for approval — the
	// right setting when a project's blast radius is infrastructure, not a diff.
	DefaultPermissionMode *string  `json:"default_permission_mode"`
	SkillSources          []string `json:"skill_sources"`
	// Isolation is this project's default sandbox tier for a new session or
	// task launch (internal/isolation) — a launch's own explicit choice
	// still wins. Absent/nil means none (today's unsandboxed behavior).
	Isolation *isolation.Config `json:"isolation"`
}

// isolationJSON encodes a project's default isolation.Config, normalizing
// nil to "{}" (none) rather than storing a JSON null.
func isolationJSON(c *isolation.Config) string {
	if c == nil {
		return "{}"
	}
	return store.J(c.Normalized())
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.Projects()
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var in projectIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if len(in.SetupCmd) > 16384 || strings.ContainsRune(in.SetupCmd, 0) {
		httpError(w, 422, "setup_cmd must be at most 16384 bytes without NUL")
		return
	}
	if in.Name == "" || in.RepoPath == "" {
		httpError(w, 422, "name and repo_path are required")
		return
	}
	if in.DefaultAgent != nil && !s.knownAgent(*in.DefaultAgent) {
		httpError(w, 422, "default_agent must be one of %v", s.knownAgentNames())
		return
	}
	if in.CapabilityProfile != nil && !oneOf(*in.CapabilityProfile, capProfiles...) {
		httpError(w, 422, "capability_profile must be one of %v", capProfiles)
		return
	}
	if in.DefaultPermissionMode != nil && *in.DefaultPermissionMode != "" &&
		!oneOf(*in.DefaultPermissionMode, permissionModes...) {
		httpError(w, 422, "default_permission_mode must be one of %v", permissionModes)
		return
	}
	if in.Isolation != nil {
		if err := in.Isolation.Validate(); err != nil {
			httpError(w, 422, "%s", err.Error())
			return
		}
	}
	if _, err := s.DB.Target(in.TargetID); err != nil {
		httpError(w, 400, "no such target")
		return
	}
	permJSON := store.J(orEmptyMap(in.Permissions))
	// reject typo'd permission keys NOW, not as a mystery denial mid-run
	if _, err := agents.ParsePermissions(permJSON); err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	p := &store.Project{
		Name: in.Name, TargetID: in.TargetID, RepoPath: in.RepoPath, SetupCmd: in.SetupCmd,
		DefaultBaseBranch: strOr(in.DefaultBaseBranch, "main"),
		WorkrootOverride:  in.WorkrootOverride, VerifyCmd: in.VerifyCmd,
		KeepWorktrees: boolInt(in.KeepWorktrees), ReviewGate: boolInt(in.ReviewGate),
		EnvJSON:     store.J(orEmptyMap(in.Env)),
		ContextJSON: store.J(orEmpty(in.ContextPaths)),
		MCPJSON:     store.J(orEmptyMap(in.MCP)),
		StrictMCP:   boolInt(in.StrictMCP), PermissionsJSON: permJSON,
		GateMatcher:          in.GateMatcher,
		DefaultAgent:         strOr(in.DefaultAgent, "claude"),
		CapabilityProfile:    strOr(in.CapabilityProfile, "restricted"),
		DefaultIsolationJSON: isolationJSON(in.Isolation),
		SkillSourcesJSON:     store.J(orEmpty(in.SkillSources)),
	}
	if in.DefaultPermissionMode != nil {
		p.DefaultPermissionMode = *in.DefaultPermissionMode
	}
	out, err := s.DB.InsertProject(p)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.provisionProjectMemory(r.Context(), out)
	writeJSON(w, 201, out)
}

type projectPatch struct {
	SetupCmd *string `json:"setup_cmd"`
	// Name is patchable because import derives it from the directory, and a
	// directory name is not always the project's name — /opt/docker is "the
	// compose stack", not "docker".
	Name                  *string           `json:"name"`
	VerifyCmd             *string           `json:"verify_cmd"`
	DefaultBaseBranch     *string           `json:"default_base_branch"`
	KeepWorktrees         *bool             `json:"keep_worktrees"`
	ReviewGate            *bool             `json:"review_gate"`
	Policy                *map[string]any   `json:"policy"`
	Env                   *map[string]any   `json:"env"`
	ContextPaths          *[]string         `json:"context_paths"`
	MCP                   *map[string]any   `json:"mcp"`
	StrictMCP             *bool             `json:"strict_mcp"`
	Permissions           *map[string]any   `json:"permissions"`
	GateMatcher           *string           `json:"gate_matcher"`
	DefaultAgent          *string           `json:"default_agent"`
	CapabilityProfile     *string           `json:"capability_profile"`
	DefaultPermissionMode *string           `json:"default_permission_mode"`
	SkillSources          *[]string         `json:"skill_sources"`
	Isolation             *isolation.Config `json:"isolation"`
}

func (s *Server) patchProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	var p projectPatch
	if err := decodeBody(r, &p); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	// Dedicated MCP edits use a conditional revision. Serialise legacy PATCHes
	// that touch the same fields so they cannot interleave a read/modify/write.
	if p.MCP != nil || p.StrictMCP != nil {
		s.mcpMu.Lock()
		defer s.mcpMu.Unlock()
	}
	if p.SetupCmd != nil && (len(*p.SetupCmd) > 16384 || strings.ContainsRune(*p.SetupCmd, 0)) {
		httpError(w, 422, "setup_cmd must be at most 16384 bytes without NUL")
		return
	}
	if p.DefaultAgent != nil && !s.knownAgent(*p.DefaultAgent) {
		httpError(w, 422, "default_agent must be one of %v", s.knownAgentNames())
		return
	}
	if p.CapabilityProfile != nil && !oneOf(*p.CapabilityProfile, capProfiles...) {
		httpError(w, 422, "capability_profile must be one of %v", capProfiles)
		return
	}
	if p.DefaultPermissionMode != nil && *p.DefaultPermissionMode != "" &&
		!oneOf(*p.DefaultPermissionMode, permissionModes...) {
		httpError(w, 422, "default_permission_mode must be one of %v", permissionModes)
		return
	}
	if p.Isolation != nil {
		if err := p.Isolation.Validate(); err != nil {
			httpError(w, 422, "%s", err.Error())
			return
		}
	}
	if _, err := s.DB.Project(id); err != nil {
		httpError(w, 404, "no such project")
		return
	}
	fields := map[string]any{}
	if p.Name != nil && strings.TrimSpace(*p.Name) != "" {
		fields["name"] = strings.TrimSpace(*p.Name)
	}
	setStr(fields, "verify_cmd", p.VerifyCmd)
	setStr(fields, "default_base_branch", p.DefaultBaseBranch)
	setStr(fields, "gate_matcher", p.GateMatcher)
	setStr(fields, "default_agent", p.DefaultAgent)
	setStr(fields, "setup_cmd", p.SetupCmd)
	setStr(fields, "capability_profile", p.CapabilityProfile)
	setStr(fields, "default_permission_mode", p.DefaultPermissionMode)
	if p.Isolation != nil {
		fields["default_isolation_json"] = isolationJSON(p.Isolation)
	}
	if p.SkillSources != nil {
		fields["skill_sources_json"] = store.J(orEmpty(*p.SkillSources))
	}
	setBool(fields, "keep_worktrees", p.KeepWorktrees)
	setBool(fields, "review_gate", p.ReviewGate)
	setBool(fields, "strict_mcp", p.StrictMCP)
	setJSON(fields, "policy_json", p.Policy)
	setJSON(fields, "env_json", p.Env)
	setJSON(fields, "mcp_json", p.MCP)
	if p.ContextPaths != nil {
		fields["context_json"] = store.J(orEmpty(*p.ContextPaths))
	}
	if p.Permissions != nil {
		raw := store.J(*p.Permissions)
		if _, err := agents.ParsePermissions(raw); err != nil {
			httpError(w, 400, "%s", err.Error())
			return
		}
		fields["permissions_json"] = raw
	}
	if len(fields) > 0 {
		if err := s.DB.Update("projects", id, fields); err != nil {
			respondErr(w, err)
			return
		}
	}
	out, err := s.DB.Project(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// projectCapability reports what this project's agents can ACTUALLY do —
// resolved, not requested.
//
// The UI states facts with this instead of inferring them from config: which MCP
// servers are reachable depends on the target kind, and a memory store is only
// usable if the sandbox was opened for it too.
func (s *Server) projectCapability(w http.ResponseWriter, r *http.Request) {
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
	target, err := s.DB.Target(proj.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	mcp := store.UnjObj(proj.MCPJSON)
	servers := scheduler.EffectiveMCPServers(s.Cfg.HostClaudeConfig, proj, target, mcp)
	perms, err := agents.ParsePermissions(proj.PermissionsJSON)
	if err != nil {
		respondErr(w, err)
		return
	}
	profile := proj.CapabilityProfile
	if profile == "" {
		profile = "restricted"
	}
	settings, err := agents.BuildSettings(agents.SettingsInput{
		Permissions: perms, Profile: profile, MCPServers: servers,
		MemoryDir: target.MemoryDir})
	if err != nil {
		respondErr(w, err)
		return
	}
	resolved, _ := settings["permissions"].(agents.Permissions)

	notes := []string{}
	if profile != "parity" {
		notes = append(notes, "restricted: only rules you set explicitly are granted — "+
			"headless denies everything else without asking")
	}
	if target.Kind != "local" && target.Kind != "mock" && len(mcp) == 0 {
		notes = append(notes, target.Kind+" target has no MCP servers; the host's own "+
			"are not portable (local binaries, secrets in env)")
	}
	if target.MemoryDir == "" {
		notes = append(notes, "no memory store on this target — agents start memory-blind")
	}
	writeJSON(w, 200, map[string]any{
		"profile": profile, "target_kind": target.Kind,
		"mcp_servers": nonNil(servers), "memory_dir": target.MemoryDir,
		"allow": nonNil(resolved.Allow), "deny": nonNil(resolved.Deny),
		"additional_directories": nonNil(resolved.AdditionalDirectories),
		"notes":                  notes,
	})
}

func (s *Server) projectNotes(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	rows, err := s.DB.ProjectNotes(id, 100)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) {
	id, err1 := pathID(r, "id")
	noteID, err2 := pathID(r, "noteID")
	if err1 != nil || err2 != nil {
		httpError(w, 404, "no such note")
		return
	}
	s.DB.Exec(`DELETE FROM memories WHERE id=? AND project_id=?`, noteID, id)
	w.WriteHeader(204)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	if s.DB.Exists("task_takeovers", "status!='ready' AND task_id IN (SELECT id FROM tasks WHERE project_id=?)", id) {
		httpError(w, 409, "A task takeover is in progress; finish it before deleting the project")
		return
	}
	// Skill ownership is target-local. Clean every recorded symlink before the
	// project row can disappear; otherwise an offline target would leave links
	// with no recovery record. Foreign or changed files block deletion and stay
	// untouched.
	if project, projectErr := s.DB.Project(id); projectErr == nil {
		if rows, rowsErr := s.DB.ProjectSkills(id, ""); rowsErr != nil {
			respondErr(w, rowsErr)
			return
		} else if len(rows) > 0 {
			target, targetErr := s.DB.Target(project.TargetID)
			if targetErr != nil {
				respondErr(w, targetErr)
				return
			}
			ex, exErr := s.Reg.For(target)
			if exErr != nil {
				respondErr(w, exErr)
				return
			}
			defer ex.Close()
			for _, skill := range rows {
				mats, matErr := s.DB.Materializations(skill.ID)
				if matErr != nil {
					respondErr(w, matErr)
					return
				}
				if len(mats) == 0 {
					httpError(w, 409, "skill %q has no ownership record; project retained", skill.SkillID)
					return
				}
				for _, mat := range mats {
					if err := skills.RemoveMaterialization(r.Context(), ex, project, skill, mat); err != nil {
						httpError(w, 409, "skill %q materialization could not be cleaned; project retained: %v", skill.SkillID, err)
						return
					}
					if err := s.DB.DeleteMaterialization(mat.ID); err != nil {
						respondErr(w, err)
						return
					}
				}
				if err := s.DB.DeleteProjectSkill(skill.ID); err != nil {
					respondErr(w, err)
					return
				}
			}
		}
	}
	// A project with history is refused unless the caller says explicitly that
	// the history goes too. Deleting eighty-one stale projects should not be a
	// way to silently lose the record of what was done in them.
	if s.DB.Exists("tasks", "project_id=?", id) {
		if r.URL.Query().Get("cascade") != "true" {
			n, _ := s.DB.Count("tasks", "project_id=?", id)
			httpError(w, 409, "project has %d tasks — pass ?cascade=true to delete them too", n)
			return
		}
		if _, err := s.DB.Exec(`DELETE FROM events WHERE attempt_id IN
			(SELECT a.id FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.project_id=?)`, id); err != nil {
			respondErr(w, err)
			return
		}
		for _, stmt := range []string{
			`DELETE FROM approvals WHERE attempt_id IN
				(SELECT a.id FROM attempts a JOIN tasks t ON t.id=a.task_id WHERE t.project_id=?)`,
			`DELETE FROM task_takeovers WHERE task_id IN (SELECT id FROM tasks WHERE project_id=?)`,
			`DELETE FROM attempts WHERE task_id IN (SELECT id FROM tasks WHERE project_id=?)`,
			`DELETE FROM tasks WHERE project_id=?`,
		} {
			if _, err := s.DB.Exec(stmt, id); err != nil {
				respondErr(w, err)
				return
			}
		}
	}
	if _, err := s.DB.Exec(`DELETE FROM memories WHERE project_id=?`, id); err != nil {
		respondErr(w, err)
		return
	}
	// Sessions reference the project, and a foreign key made deleting one with
	// any attached session fail as a bare 500. Unassigning is the right answer
	// rather than refusing or cascading: the tmux session is a real thing that
	// outlives this record, so it goes back to being unassigned — exactly what
	// it was before it was promoted, and reversible from the same picker.
	if _, err := s.DB.Exec(
		`UPDATE sessions SET project_id=NULL WHERE project_id=?`, id); err != nil {
		respondErr(w, err)
		return
	}
	if _, err := s.DB.Exec(`DELETE FROM projects WHERE id=?`, id); err != nil {
		respondErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func setStr(fields map[string]any, col string, v *string) {
	if v != nil {
		fields[col] = *v
	}
}

func setBool(fields map[string]any, col string, v *bool) {
	if v != nil {
		fields[col] = boolInt(*v)
	}
}

func setJSON(fields map[string]any, col string, v *map[string]any) {
	if v != nil {
		fields[col] = store.J(*v)
	}
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// ---- project usage, for deciding what is dead --------------------------

// projectUsage is what makes an old project identifiable as old. Deleting one
// is easy; knowing which of eighty-one is no longer worked on is the hard part,
// so the list carries the two facts that answer it: how much is attached, and
// when anything last happened.
type projectUsage struct {
	ProjectID  int64   `json:"project_id"`
	Tasks      int     `json:"tasks"`
	OpenTasks  int     `json:"open_tasks"`
	Sessions   int     `json:"sessions"`
	LastActive float64 `json:"last_active_at"`
}

// repoActivity asks each repository when it was last committed to.
//
// Batched one command per target rather than one per project: eighty-one
// round trips to a remote host to draw one screen is not a screen anyone waits
// for. Cached, because a commit date does not change by the minute.
func (s *Server) repoActivity(ctx context.Context, projects []*store.Project) map[int64]float64 {
	s.repoMu.Lock()
	if s.repoCache != nil && time.Since(s.repoCachedAt) < repoCacheTTL {
		out := s.repoCache
		s.repoMu.Unlock()
		return out
	}
	s.repoMu.Unlock()

	byTarget := map[int64][]*store.Project{}
	for _, p := range projects {
		if p.RepoPath != "" {
			byTarget[p.TargetID] = append(byTarget[p.TargetID], p)
		}
	}
	// Targets are probed concurrently. Sequentially, one unreachable host holds
	// the whole list hostage for its full timeout before the reachable ones are
	// even tried — and the estate has nine targets. (The git calls themselves
	// are free: eighty-one of them measure 0.05s. The cost is the SSH round
	// trip, which is exactly what parallelising removes.)
	out := map[int64]float64{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for targetID, group := range byTarget {
		wg.Add(1)
		go func(targetID int64, group []*store.Project) {
			defer wg.Done()
			s.repoActivityFor(ctx, targetID, group, &mu, out)
		}(targetID, group)
	}
	wg.Wait()

	s.repoMu.Lock()
	s.repoCache, s.repoCachedAt = out, time.Now()
	s.repoMu.Unlock()
	return out
}

// repoActivityFor probes one target's repositories.
func (s *Server) repoActivityFor(ctx context.Context, targetID int64,
	group []*store.Project, mu *sync.Mutex, out map[int64]float64) {
	{
		target, err := s.DB.Target(targetID)
		if err != nil {
			return
		}
		ex, err := s.Reg.For(target)
		if err != nil {
			return
		}
		// One subshell per repository, run in parallel: eighty-one sequential
		// `git log` calls measured six seconds, which is too long to wait for a
		// list to sort itself on a phone. Each writes a single short line, and a
		// write under PIPE_BUF is atomic, so the lines cannot interleave.
		var b strings.Builder
		for _, p := range group {
			fmt.Fprintf(&b, "{ printf '%%s\\t%%s\\n' %s \"$(git -C %s log -1 --format=%%ct 2>/dev/null)\"; } & ",
				shellq.Quote(fmt.Sprint(p.ID)), shellq.Quote(p.RepoPath))
		}
		b.WriteString("wait")
		// short: this is decoration on a list, not something worth waiting on
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		res, err := ex.Run(cctx, b.String(), executor.RunOpts{Timeout: 15})
		cancel()
		if err != nil {
			return
		}
		for _, line := range strings.Split(res.Stdout, "\n") {
			id, ts, ok := strings.Cut(strings.TrimSpace(line), "\t")
			if !ok || ts == "" {
				continue
			}
			var pid int64
			var when float64
			if _, err := fmt.Sscan(id, &pid); err != nil {
				continue
			}
			if _, err := fmt.Sscan(ts, &when); err != nil || when <= 0 {
				continue
			}
			mu.Lock()
			out[pid] = when
			mu.Unlock()
		}
	}
}

// repoCacheTTL: a repository's last commit does not change by the minute.
const repoCacheTTL = 10 * time.Minute

func (s *Server) projectsUsage(w http.ResponseWriter, r *http.Request) {
	// one pass per table rather than a query per project: with eighty-one
	// projects the N+1 version is eighty-one round trips to render one screen
	usage := map[int64]*projectUsage{}
	at := func(id int64) *projectUsage {
		if u, ok := usage[id]; ok {
			return u
		}
		u := &projectUsage{ProjectID: id}
		usage[id] = u
		return u
	}
	projects, err := s.DB.Projects()
	if err != nil {
		respondErr(w, err)
		return
	}
	for _, p := range projects {
		u := at(p.ID)
		u.LastActive = p.CreatedAt
	}
	// An imported project has no tasks and no sessions, so its created_at is
	// when lectern learned about it — which for a bulk import is the same
	// instant for all of them, and tells you nothing about which are dead. The
	// repository's own last commit is the honest answer, so it wins when it is
	// older or newer than anything lectern knows.
	for id, when := range s.repoActivity(r.Context(), projects) {
		u := at(id)
		if when > 0 && u.Tasks == 0 && u.Sessions == 0 {
			u.LastActive = when
		}
	}

	rows, err := s.DB.Query(`SELECT project_id, COUNT(*),
		SUM(CASE WHEN status IN ('queued','running','review') THEN 1 ELSE 0 END),
		MAX(COALESCE(updated_at, created_at))
		FROM tasks GROUP BY project_id`)
	if err == nil {
		for rows.Next() {
			var id int64
			var total, open int
			var last float64
			if rows.Scan(&id, &total, &open, &last) == nil {
				u := at(id)
				u.Tasks, u.OpenTasks = total, open
				if last > u.LastActive {
					u.LastActive = last
				}
			}
		}
		rows.Close()
	}

	rows, err = s.DB.Query(`SELECT project_id, COUNT(*),
		MAX(COALESCE(last_activity_at, updated_at, created_at))
		FROM sessions WHERE project_id IS NOT NULL GROUP BY project_id`)
	if err == nil {
		for rows.Next() {
			var id int64
			var n int
			var last float64
			if rows.Scan(&id, &n, &last) == nil {
				u := at(id)
				u.Sessions = n
				if last > u.LastActive {
					u.LastActive = last
				}
			}
		}
		rows.Close()
	}

	out := make([]*projectUsage, 0, len(usage))
	for _, u := range usage {
		out = append(out, u)
	}
	writeJSON(w, 200, out)
}

// projectTerminal opens a plain shell where the project's code lives.
//
// The board is for steering agents, but sometimes you just need to look at the
// thing yourself — read a file, fix one line, check what a command actually
// prints. This is the same terminal machinery an agent session uses, pointed at
// a shell instead: a tmux session per project, so closing the tab and coming
// back returns you to it rather than starting over.
func (s *Server) projectTerminal(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	att, target, err := s.resolveAttachment("project", fmt.Sprint(id))
	if err != nil {
		httpError(w, 404, "%s", err.Error())
		return
	}
	port, err := s.Terminals.Attach(r.Context(), att, target)
	if err != nil {
		httpError(w, 503, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"port": port,
		"url": fmt.Sprintf("/term/project/%d/", id), "tmux_session": att.TmuxSession})
}
