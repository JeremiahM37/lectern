// Package api is lectern's HTTP surface: the REST control plane, the
// agent-facing hook endpoints, the SSE streams and the embedded PWA.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/broker"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/memory"
	"github.com/JeremiahM37/lectern/v2/internal/push"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
	"github.com/JeremiahM37/lectern/v2/web"
)

// Server wires every dependency the handlers need.
type Server struct {
	DB        *store.DB
	Bus       *bus.Bus
	Broker    *broker.Broker
	Notifier  *sinks.Notifier
	Reg       *executor.Registry
	Sched     *scheduler.Scheduler
	Sessions  *sessions.Manager
	Events    *agentevents.Ingester
	Memory    memory.Provider
	Terminals *terminal.Manager
	Push      *push.Sender
	Cfg       *config.Config
	Auth      *auth.Resolver
	Log       *slog.Logger

	// SummaryGen, when set, replaces the real headless cheap-model call PR
	// description generation makes (see review.go runSummary). Tests set this
	// to a stub so a review test never spends a real model token or needs a
	// real agent binary.
	SummaryGen func(ctx context.Context, ex executor.Executor, agent, model, prompt string) (string, error)

	streamsOnce   sync.Once
	streamsCtx    context.Context
	streamsCancel context.CancelFunc

	searchMu    sync.Mutex
	searchJobs  map[string]*conversationSearchJob
	searchSlots chan struct{}

	// mcpMu serialises conditional project MCP edits. The endpoint returns a
	// revision instead of exposing credential-bearing values to its clients.
	mcpMu  sync.Mutex
	mcpKey []byte

	// agentMu protects the process-local key used by the agent registry's typed
	// secret-retention markers. The key is deliberately never persisted.
	agentMu  sync.Mutex
	agentKey []byte

	// live holds forwarded ports and desktops; see live.go.
	live liveState

	uploadMu    sync.Mutex
	uploadCount int

	// modelCache holds each agent's self-reported model catalog; see probeModels.
	modelMu    sync.Mutex
	modelCache map[string]modelCacheEntry

	// repoCache holds each project's last commit time; see repoActivity.
	repoMu       sync.Mutex
	repoCache    map[int64]float64
	repoCachedAt time.Time
}

// Handler builds the full router, including auth and the embedded web app.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// ---- targets ----
	mux.HandleFunc("GET /api/targets", s.listTargets)
	mux.HandleFunc("POST /api/targets", s.createTarget)
	mux.HandleFunc("PATCH /api/targets/{id}", s.patchTarget)
	mux.HandleFunc("DELETE /api/targets/{id}", s.deleteTarget)
	mux.HandleFunc("POST /api/targets/{id}/check", s.checkTarget)
	mux.HandleFunc("GET /api/targets/{id}/agents", s.targetAgentCommands)

	// ---- projects ----
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("POST /api/projects/{id}/memory", s.retryProjectMemory)
	mux.HandleFunc("PATCH /api/projects/{id}", s.patchProject)
	mux.HandleFunc("GET /api/projects/{id}/mcp", s.projectMCP)
	mux.HandleFunc("GET /api/projects/{id}/skills", s.projectSkills)
	mux.HandleFunc("POST /api/projects/{id}/skills", s.attachProjectSkill)
	mux.HandleFunc("DELETE /api/projects/{id}/skills/{attachment_id}", s.detachProjectSkill)
	mux.HandleFunc("GET /api/projects/{id}/workflows", s.projectWorkflows)
	mux.HandleFunc("PUT /api/projects/{id}/workflows/{workflow_id}", s.putProjectWorkflow)
	mux.HandleFunc("PUT /api/projects/{id}/mcp", s.putProjectMCP)
	mux.HandleFunc("DELETE /api/projects/{id}", s.deleteProject)
	mux.HandleFunc("GET /api/projects/usage", s.projectsUsage)
	mux.HandleFunc("POST /api/projects/{id}/terminal", s.projectTerminal)
	mux.HandleFunc("GET /api/projects/import/scan", s.scanProjects)
	mux.HandleFunc("GET /api/skills", s.listSkills)
	mux.HandleFunc("POST /api/projects/import", s.importProjects)
	mux.HandleFunc("GET /api/projects/{id}/capability", s.projectCapability)
	mux.HandleFunc("GET /api/projects/{id}/notes", s.projectNotes)
	mux.HandleFunc("GET /api/projects/{id}/memory", s.projectMemoryLink)
	mux.HandleFunc("DELETE /api/projects/{id}/notes/{noteID}", s.deleteNote)

	// ---- tasks ----
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("GET /api/tasks/{id}", s.getTask)
	mux.HandleFunc("GET /api/tasks/{id}/messages", s.taskMessages)
	mux.HandleFunc("POST /api/tasks/{id}/messages", s.sendTaskMessage)
	mux.HandleFunc("POST /api/tasks/{id}/attachments", s.uploadTaskAttachment)
	mux.HandleFunc("PATCH /api/tasks/{id}", s.patchTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/clear", s.clearTasks)
	mux.HandleFunc("POST /api/tasks/{id}/takeover", s.takeoverTask)
	mux.HandleFunc("POST /api/tasks/{id}/dispatch", s.dispatchTask)
	mux.HandleFunc("POST /api/tasks/{id}/followup", s.followupTask)
	mux.HandleFunc("POST /api/tasks/{id}/steer", s.steerTask)
	mux.HandleFunc("POST /api/tasks/{id}/complete", s.completeTask)
	mux.HandleFunc("POST /api/tasks/{id}/cancel", s.cancelTask)
	mux.HandleFunc("POST /api/tasks/{id}/commit", s.commitTask)
	mux.HandleFunc("POST /api/tasks/{id}/cleanup", s.cleanupTask)
	mux.HandleFunc("POST /api/tasks/{id}/terminal", s.attachTerminal)
	mux.HandleFunc("GET /api/tasks/{id}/events", s.taskEvents)
	mux.HandleFunc("GET /api/tasks/{id}/diff", s.taskDiff)
	mux.HandleFunc("GET /api/tasks/{id}/wait", s.waitTask)
	mux.HandleFunc("GET /api/tasks/{id}/report", s.taskReport)
	mux.HandleFunc("POST /api/tasks/{id}/integrate", s.integrateTask)
	mux.HandleFunc("GET /api/tasks/{id}/stream", s.taskStream)
	mux.HandleFunc("GET /api/stream", s.boardStream)
	mux.HandleFunc("GET /api/live", s.listLive)
	mux.HandleFunc("POST /api/live/ports", s.openLivePort)
	mux.HandleFunc("POST /api/live/desktops", s.openLiveDesktop)
	mux.HandleFunc("POST /api/live/{id}/browser", s.liveBrowser)
	mux.HandleFunc("DELETE /api/live/{id}", s.closeLive)
	mux.HandleFunc("GET /api/media", s.listMedia)
	mux.HandleFunc("POST /api/media", s.postMedia)
	mux.HandleFunc("GET /api/media/{id}/content", s.mediaContent)
	mux.HandleFunc("DELETE /api/media/{id}", s.deleteMedia)

	// ---- approvals (operator + agent) ----
	mux.HandleFunc("GET /api/approvals", s.listApprovals)
	mux.HandleFunc("POST /api/approvals/{id}/decision", s.decideApproval)
	mux.HandleFunc("POST /api/hook/approval", s.hookCreateApproval)
	mux.HandleFunc("GET /api/hook/approval/{id}/decision", s.hookApprovalDecision)
	mux.HandleFunc("POST /api/hook/tasks", s.hookFileTask)
	mux.HandleFunc("POST /api/hook/notes", s.hookAddNote)
	// ---- agent hooks (docs/agent-events.md section 2): per-SESSION bearer
	// token, not the per-attempt token the approval hooks above use ----
	mux.HandleFunc("POST /api/hook/session/{id}/statusline", s.hookSessionStatusline)
	mux.HandleFunc("POST /api/hook/session/{id}/{event}", s.hookSessionEvent)

	mux.HandleFunc("POST /api/conversation-search", s.startConversationSearch)
	mux.HandleFunc("GET /api/conversation-search/{search}", s.getConversationSearch)
	mux.HandleFunc("DELETE /api/conversation-search/{search}", s.cancelConversationSearch)
	mux.HandleFunc("GET /api/conversation-search/{search}/results/{result}", s.readConversationSearchResult)
	mux.HandleFunc("POST /api/conversation-search/{search}/results/{result}/fork", s.forkConversationSearchResult)

	// ---- sessions: the interactive half of the board ----
	mux.HandleFunc("GET /api/agents", s.listAgents)
	mux.HandleFunc("GET /api/launch-profiles", s.listLaunchProfiles)
	mux.HandleFunc("POST /api/launch-profiles", s.saveLaunchProfile)
	mux.HandleFunc("PUT /api/launch-profiles/{id}", s.saveLaunchProfile)
	mux.HandleFunc("DELETE /api/launch-profiles/{id}", s.deleteLaunchProfile)
	mux.HandleFunc("GET /api/models", s.listModels)
	mux.HandleFunc("PUT /api/agents", s.putAgents)
	mux.HandleFunc("GET /api/sessions", s.listSessions)
	mux.HandleFunc("GET /api/sessions/recent", s.recentSessions)
	mux.HandleFunc("POST /api/sessions", s.createSession)
	mux.HandleFunc("POST /api/shells", s.createShell)
	mux.HandleFunc("GET /api/sessions/discover", s.discoverSessions)
	mux.HandleFunc("POST /api/sessions/adopt", s.adoptSession)
	mux.HandleFunc("GET /api/sessions/{id}", s.getSession)
	mux.HandleFunc("GET /api/sessions/{id}/reader", s.sessionReader)
	mux.HandleFunc("GET /api/sessions/{id}/conversations", s.nativeConversations)
	mux.HandleFunc("GET /api/sessions/{id}/conversations/{conversation}", s.nativeConversations)
	mux.HandleFunc("POST /api/sessions/{id}/fork", s.forkConversation)
	mux.HandleFunc("POST /api/sessions/{id}/resume", s.resumeConversation)
	mux.HandleFunc("POST /api/sessions/{id}/revive", s.reviveSession)
	mux.HandleFunc("POST /api/sessions/{id}/resume-recent", s.resumeRecentConversation)
	mux.HandleFunc("POST /api/sessions/{id}/archive", s.archiveSession)
	mux.HandleFunc("DELETE /api/sessions/{id}/archive", s.unarchiveSession)
	mux.HandleFunc("GET /api/sessions/{id}/archive/history", s.archivedHistory)
	mux.HandleFunc("POST /api/sessions/{id}/worktree/recover", s.recoverSessionWorktree)
	mux.HandleFunc("POST /api/sessions/{id}/setup/cancel", s.cancelSessionSetup)
	mux.HandleFunc("DELETE /api/sessions/{id}/worktree", s.removeSessionWorktree)
	mux.HandleFunc("GET /api/sessions/{id}/worktree", s.sessionWorkspaceProgress)
	mux.HandleFunc("POST /api/sessions/{id}/worktree/repositories", s.extendSessionWorkspace)
	mux.HandleFunc("GET /api/sessions/{id}/worktree/operations", s.workspaceOperations)
	mux.HandleFunc("GET /api/sessions/{id}/worktree/operations/{operation}", s.workspaceOperation)
	mux.HandleFunc("POST /api/sessions/{id}/worktree/operations/{operation}/cancel", s.cancelWorkspaceOperation)
	mux.HandleFunc("POST /api/sessions/{id}/worktree/operations/{operation}/recover", s.recoverWorkspaceOperation)
	mux.HandleFunc("PATCH /api/sessions/{id}", s.patchSession)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.deleteSession)
	mux.HandleFunc("POST /api/sessions/{id}/restore", s.restoreSession)
	mux.HandleFunc("POST /api/sessions/{id}/send", s.sendToSession)
	mux.HandleFunc("POST /api/sessions/{id}/attachments", s.uploadSessionAttachment)
	mux.HandleFunc("GET /api/sessions/{id}/memory", s.sessionMemory)
	mux.HandleFunc("POST /api/sessions/{id}/terminal", s.attachSession)
	mux.HandleFunc("POST /api/sessions/{id}/handoff", s.handoffSession)
	mux.HandleFunc("GET /api/sessions/{id}/wraps", s.sessionWraps)
	mux.HandleFunc("POST /api/sessions/{id}/promote", s.promoteSession)
	mux.HandleFunc("GET /api/sessions/{id}/promote/preview", s.previewPromotion)

	// ---- terminal workspace ----
	mux.HandleFunc("GET /terminal/{kind}/{id}", s.terminalPage)
	mux.HandleFunc("GET /api/term/{kind}/{id}/info", s.terminalInfo)
	mux.HandleFunc("GET /api/term/{kind}/{id}/history", s.terminalHistory)
	mux.HandleFunc("POST /api/term/{kind}/{id}/attachments", s.terminalUpload)
	mux.HandleFunc("GET /api/term/{kind}/{id}/files", s.terminalFiles)
	mux.HandleFunc("GET /api/term/{kind}/{id}/changes", s.terminalChanges)
	mux.HandleFunc("GET /api/term/{kind}/{id}/file", s.terminalFile)
	// ---- attached terminals (proxied on this origin; see termproxy.go) ----
	mux.HandleFunc("/term/{kind}/{id}", s.termProxy)
	mux.HandleFunc("/term/{kind}/{id}/", s.termProxy)
	mux.HandleFunc("GET /api/projects/{id}/wraps", s.projectWraps)
	mux.HandleFunc("GET /api/projects/{id}/brief", s.previewBrief)

	// ---- misc ----
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/whoami", s.whoami)
	mux.HandleFunc("GET /api/delegation", s.getDelegation)
	mux.HandleFunc("PUT /api/delegation", s.putDelegation)
	mux.HandleFunc("POST /api/delegation/preset", s.installDelegationPreset)
	mux.HandleFunc("POST /api/delegation/check", s.checkDelegationWorker)
	mux.HandleFunc("GET /api/settings", s.getSettings)
	mux.HandleFunc("PUT /api/settings", s.putSettings)
	mux.HandleFunc("POST /api/settings/test-notification", s.testNotification)
	// ---- routines: a saved job, one button, optionally scheduled ----
	mux.HandleFunc("GET /api/routines", s.listRoutines)
	mux.HandleFunc("POST /api/routines", s.createRoutine)
	mux.HandleFunc("PATCH /api/routines/{id}", s.patchRoutine)
	mux.HandleFunc("DELETE /api/routines/{id}", s.deleteRoutine)
	mux.HandleFunc("POST /api/routines/{id}/run", s.runRoutine)

	mux.HandleFunc("GET /api/templates", s.getTemplates)
	mux.HandleFunc("PUT /api/templates", s.putTemplates)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/usage", s.usageReport)
	mux.HandleFunc("POST /api/admin/janitor", s.runJanitor)
	mux.HandleFunc("GET /api/scratch", s.scratchReport)
	mux.HandleFunc("POST /api/scratch/sweep", s.scratchSweep)
	mux.HandleFunc("POST /api/scratch/discard", s.scratchDiscard)
	mux.HandleFunc("POST /api/scratch/keep", s.scratchKeep)
	mux.HandleFunc("GET /api/push/vapid", s.vapidKey)
	mux.HandleFunc("POST /api/push/subscribe", s.subscribePush)

	// ---- review: live diffs, commit/push/PR and inline comments ----
	mux.HandleFunc("GET /api/sessions/{id}/diff", s.sessionDiff)
	mux.HandleFunc("POST /api/sessions/{id}/commit", s.commitSession)
	mux.HandleFunc("POST /api/sessions/{id}/pr-description", s.sessionPRDescription)
	mux.HandleFunc("POST /api/tasks/{id}/pr-description", s.taskPRDescription)
	mux.HandleFunc("POST /api/sessions/{id}/review", s.reviewSession)
	mux.HandleFunc("POST /api/tasks/{id}/review", s.reviewTask)

	mux.Handle("/", s.staticHandler())
	return s.withAuth(mux)
}

// withAuth gates /api and /term/* behind internal/auth: tailscale identity,
// a bearer token, or nothing at all, depending on the resolved mode (see
// auth.Resolver, built once at startup in internal/app).
//
// /api/hook/* is deliberately exempt: agents authenticate with their own
// per-attempt token there. That is also the security boundary — in token mode an
// agent holds ONLY its hook token, so it cannot reach the human decision
// endpoint to approve its own gated action.
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !((strings.HasPrefix(p, "/api") && !strings.HasPrefix(p, "/api/hook/")) || strings.HasPrefix(p, "/term/")) {
			next.ServeHTTP(w, r)
			return
		}
		principal, ok := s.Auth.Authenticate(r)
		if !ok {
			writeJSON(w, 401, map[string]any{"detail": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
	})
}

// whoami reports the caller's own resolved identity — what the PWA shows in
// Settings, and the first thing worth checking when access looks wrong.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	principal, _ := auth.FromContext(r.Context())
	writeJSON(w, 200, map[string]any{
		"mode": string(s.Auth.Mode), "kind": principal.Kind,
		"login": principal.Login, "node": principal.Node, "human": principal.Human,
	})
}

// staticHandler serves the embedded PWA.
//
// Every asset is sent with Cache-Control: no-cache. This is a self-hosted app
// that updates in place: with default heuristic caching, an installed phone
// keeps running the previous app.js and style.css after a deploy, which looks
// exactly like the change not working. 'no-cache' still allows a 304 against the
// ETag, so the cost is one conditional request per asset, not a re-download.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(web.Assets, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(web.IndexHTML)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// ---- shared helpers ----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

// httpError mirrors FastAPI's {"detail": ...} shape, which the PWA and the MCP
// server both already parse.
func httpError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]any{"detail": fmt.Sprintf(format, args...)})
}

// decodeBody parses a JSON body. An empty body is allowed — several endpoints
// are POSTed with nothing at all.
func decodeBody(r *http.Request, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// validationError is a schema-level rejection: a field outside its allowed set.
// It maps to 422 so clients can tell "you sent nonsense" from "that is not
// allowed right now" (409) or "no such thing" (404).
type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return &validationError{fmt.Sprintf(format, args...)}
}

// respondErr maps an internal error onto a status code.
func respondErr(w http.ResponseWriter, err error) {
	var ve *validationError
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, 404, "not found")
	case errors.As(err, &ve):
		httpError(w, 422, "%s", ve.Error())
	default:
		httpError(w, 500, "%s", err.Error())
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

// ---- SSE ---------------------------------------------------------------------

func (s *Server) boardStream(w http.ResponseWriter, r *http.Request) { s.sse(w, r, "board") }

func (s *Server) taskStream(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such task")
		return
	}
	s.sse(w, r, fmt.Sprintf("task:%d", id))
}

// DrainStreams ends open-ended live updates before HTTP graceful shutdown waits
// for requests. Ordinary requests retain their own lifetime and drain normally.
func (s *Server) DrainStreams() {
	s.streamDone()
	s.streamsCancel()
}

func (s *Server) streamDone() <-chan struct{} {
	s.streamsOnce.Do(func() {
		s.streamsCtx, s.streamsCancel = context.WithCancel(context.Background())
	})
	return s.streamsCtx.Done()
}

func (s *Server) sse(w http.ResponseWriter, r *http.Request, channel string) {
	draining := s.streamDone()
	select {
	case <-draining:
		httpError(w, http.StatusServiceUnavailable, "Server is restarting")
		return
	default:
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpError(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// nginx buffers proxied responses by default, which holds every event until
	// the stream ends — i.e. forever
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	io.WriteString(w, ": connected\n\n")
	flusher.Flush()

	ch := s.Bus.Subscribe(channel)
	defer s.Bus.Unsubscribe(channel, ch)
	ctx := r.Context()
	keepalive := newTicker(15)
	defer keepalive.Stop()
	for {
		select {
		case <-draining:
			return
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			io.WriteString(w, bus.Format(msg))
			flusher.Flush()
		case <-keepalive.C:
			io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// Shutdown releases everything the server owns.
func (s *Server) Shutdown(ctx context.Context) {
	s.liveShutdown()
	s.DrainStreams()
	s.Terminals.Shutdown()
	s.Sched.Stop()
	s.Notifier.Wait()
	s.Reg.Reset()
}

func newTicker(seconds int) *time.Ticker {
	return time.NewTicker(time.Duration(seconds) * time.Second)
}
