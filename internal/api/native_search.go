package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/shellq"
	"github.com/JeremiahM37/lectern/internal/store"
)

//go:embed scripts/native_search.py
var nativeSearchScript string

//go:embed scripts/native_search_read.py
var nativeSearchReadScript string

type searchProgress struct {
	Complete  bool     `json:"complete"`
	Pending   int      `json:"pending_files"`
	Bytes     int64    `json:"scanned_bytes"`
	Documents int      `json:"documents"`
	Oversized int      `json:"oversized_entries"`
	Issues    []string `json:"issues"`
}
type searchMatch struct {
	Document    int64   `json:"document"`
	CID         string  `json:"cid"`
	Cwd         string  `json:"cwd"`
	Title       string  `json:"title"`
	Modified    float64 `json:"modified"`
	Role        string  `json:"role"`
	Offset      int64   `json:"offset"`
	Fingerprint string  `json:"fingerprint"`
	Snippet     string  `json:"snippet"`
}
type searchWorkerReply struct {
	Profile  string         `json:"profile_key"`
	Progress searchProgress `json:"progress"`
	Matches  []searchMatch  `json:"matches"`
	More     bool           `json:"more"`
	Error    string         `json:"error"`
}
type searchLaunchChoice struct {
	ID            string `json:"id"`
	Label         string `json:"label"`
	Model         string `json:"model"`
	Supported     bool   `json:"supported"`
	configuration *sessions.LaunchConfiguration
	signature     string
}
type conversationSearchScope struct {
	ID       string         `json:"id"`
	TargetID int64          `json:"target_id"`
	Target   string         `json:"target"`
	Agent    string         `json:"agent"`
	State    string         `json:"state"`
	Error    string         `json:"error,omitempty"`
	Progress searchProgress `json:"progress"`
	More     bool           `json:"more"`
	target   *store.Target
	prefix   string
	profile  string
	matches  []searchMatch
	launches []searchLaunchChoice
}
type conversationSearchJob struct {
	mu        sync.Mutex
	id, query string
	scopes    []*conversationSearchScope
	cancel    context.CancelFunc
	created   time.Time
	done      bool
}
type conversationSearchResult struct {
	ID           string  `json:"id"`
	Scope        string  `json:"scope"`
	TargetID     int64   `json:"target_id"`
	Target       string  `json:"target"`
	Agent        string  `json:"agent"`
	Conversation string  `json:"conversation_id"`
	Cwd          string  `json:"cwd"`
	Title        string  `json:"title"`
	Modified     float64 `json:"modified"`
	Role         string  `json:"role"`
	Snippet      string  `json:"snippet"`
}

// Search scope configuration is private. Public responses never serialize env,
// executor commands, or native cache paths.
func (s *Server) conversationSearchScopes(targetID int64, agent string) ([]*conversationSearchScope, error) {
	targets, err := s.DB.Targets()
	if err != nil {
		return nil, err
	}
	projects, err := s.DB.Projects()
	if err != nil {
		return nil, err
	}
	profiles, err := s.DB.LaunchProfiles()
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Sessions(true)
	if err != nil {
		return nil, err
	}
	byID := map[int64]*store.Target{}
	for _, target := range targets {
		byID[target.ID] = target
	}
	if targetID != 0 && byID[targetID] == nil {
		return nil, fmt.Errorf("target not found")
	}
	seen := map[string]*conversationSearchScope{}
	scopes := []*conversationSearchScope{}
	add := func(row *store.Session) {
		if (agent != "" && row.Agent != agent) || (row.Agent != "claude" && row.Agent != "codex") || (targetID != 0 && row.TargetID != targetID) {
			return
		}
		target := byID[row.TargetID]
		if target == nil {
			return
		}
		cfg, err := s.Sessions.SessionLaunchConfiguration(row)
		prefix := ""
		if err == nil {
			prefix, err = sessions.EnvPrefix(cfg.Spec.Env)
		}
		key := fmt.Sprintf("%d/%s/%s", target.ID, row.Agent, prefix)
		if err != nil {
			key = fmt.Sprintf("%d/%s/invalid/%d", target.ID, row.Agent, row.ID)
		}
		scope, exists := seen[key]
		if !exists {
			scope = &conversationSearchScope{ID: strconv.Itoa(len(scopes) + 1), TargetID: target.ID, Target: target.Name, Agent: row.Agent, State: "queued", target: target, prefix: prefix}
			seen[key] = scope
		}
		if cfg != nil && err == nil {
			raw, _ := json.Marshal(cfg)
			signature := string(raw) + "\x00" + row.Model
			duplicate := false
			for _, choice := range scope.launches {
				if choice.signature == signature {
					duplicate = true
					break
				}
			}
			if !duplicate {
				label := "Current agent settings"
				if row.ID != 0 {
					label = "Current settings for session: " + row.Name
					if row.LaunchConfigJSON != "" {
						label = "Saved settings from session: " + row.Name
					}
				} else if cfg.ProfileID != 0 {
					label = "Launch profile: " + cfg.ProfileName
				} else if row.ProjectID != nil {
					for _, project := range projects {
						if project.ID == *row.ProjectID {
							label = "Project settings: " + project.Name
							break
						}
					}
				}
				scope.launches = append(scope.launches, searchLaunchChoice{ID: scope.ID + "-" + strconv.Itoa(len(scope.launches)+1), Label: label, Model: row.Model, Supported: len(cfg.Spec.ForkArgs) > 0, configuration: cfg, signature: signature})
			}
		}
		if exists {
			return
		}
		if err != nil {
			scope.State = "error"
			scope.Error = "Saved agent configuration is unavailable"
		}
		if target.Kind != "local" && target.Kind != "ssh" && target.Kind != "pct" {
			scope.State = "error"
			scope.Error = "Native search is unavailable for this target kind"
		}
		scopes = append(scopes, scope)
	}
	// Saved settings come first so historical profiles survive later settings edits.
	for _, row := range rows {
		add(row)
	}
	for _, project := range projects {
		for _, name := range []string{"claude", "codex"} {
			add(&store.Session{TargetID: project.TargetID, ProjectID: &project.ID, Agent: name})
		}
	}
	for _, target := range targets {
		for _, profile := range profiles {
			if profile.Agent != "claude" && profile.Agent != "codex" {
				continue
			}
			options, err := s.Sessions.ApplyLaunchProfile(sessions.LaunchOpts{ProfileID: profile.ID})
			encoded := "{"
			if err == nil {
				encoded = store.J(options.Configuration)
			}
			add(&store.Session{TargetID: target.ID, Agent: profile.Agent, Model: options.Model, LaunchConfigJSON: encoded})
		}
		for _, name := range []string{"claude", "codex"} {
			add(&store.Session{TargetID: target.ID, Agent: name})
		}
	}
	if len(scopes) > 64 {
		return nil, fmt.Errorf("too many native profiles; filter by target and agent")
	}
	return scopes, nil
}
func (s *Server) startConversationSearch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Query    string `json:"query"`
		TargetID int64  `json:"target_id"`
		Agent    string `json:"agent"`
		Reset    bool   `json:"reset"`
	}
	if decodeBody(r, &in) != nil || strings.TrimSpace(in.Query) == "" || len(in.Query) > 500 || in.TargetID < 0 || (in.Agent != "" && in.Agent != "claude" && in.Agent != "codex") {
		httpError(w, 422, "enter a query of 1–500 characters and a valid target/agent filter")
		return
	}
	scopes, err := s.conversationSearchScopes(in.TargetID, in.Agent)
	if err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	s.searchMu.Lock()
	if s.searchJobs == nil {
		s.searchJobs = map[string]*conversationSearchJob{}
		s.searchSlots = make(chan struct{}, 4)
	}
	active := 0
	var oldest *conversationSearchJob
	for id, job := range s.searchJobs {
		job.mu.Lock()
		done := job.done
		job.mu.Unlock()
		if time.Since(job.created) > 15*time.Minute {
			job.cancel()
			delete(s.searchJobs, id)
			continue
		}
		if !done {
			active++
		} else if oldest == nil || job.created.Before(oldest.created) {
			oldest = job
		}
	}
	if active >= 4 {
		s.searchMu.Unlock()
		httpError(w, 429, "four searches are already running; cancel one or wait")
		return
	}
	if len(s.searchJobs) >= 16 && oldest != nil {
		delete(s.searchJobs, oldest.id)
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		s.searchMu.Unlock()
		httpError(w, 500, "could not start search")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	job := &conversationSearchJob{id: hex.EncodeToString(token), query: strings.TrimSpace(in.Query), scopes: scopes, cancel: cancel, created: time.Now()}
	s.searchJobs[job.id] = job
	s.searchMu.Unlock()
	go s.runConversationSearch(ctx, job, in.Reset)
	s.writeConversationSearch(w, job, 202)
}
func (s *Server) runConversationSearch(ctx context.Context, job *conversationSearchJob, reset bool) {
	defer job.cancel()
	// Give every profile one bounded pass before revisiting large histories.
	// Otherwise four cold profiles can occupy all workers for the whole job.
	pending := append([]*conversationSearchScope(nil), job.scopes...)
	for len(pending) > 0 {
		var wg sync.WaitGroup
		queue := make(chan *conversationSearchScope, len(pending))
		for _, scope := range pending {
			queue <- scope
		}
		close(queue)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for scope := range queue {
					s.runConversationSearchScope(ctx, job, scope, reset)
				}
			}()
		}
		wg.Wait()
		reset = false
		job.mu.Lock()
		next := make([]*conversationSearchScope, 0, len(pending))
		for _, scope := range pending {
			if scope.State == "indexing" {
				next = append(next, scope)
			}
		}
		job.mu.Unlock()
		pending = next
	}
	job.mu.Lock()
	job.done = true
	job.mu.Unlock()
}

// A queued job must not silently follow a target whose connection was edited.
func (s *Server) searchTargetUnchanged(saved *store.Target) bool {
	current, err := s.DB.Target(saved.ID)
	return err == nil && current.Kind == saved.Kind && current.Host == saved.Host && current.Port == saved.Port && current.User == saved.User && current.KeyPath == saved.KeyPath && current.CommandPrefix == saved.CommandPrefix
}

func (s *Server) runConversationSearchScope(ctx context.Context, job *conversationSearchJob, scope *conversationSearchScope, reset bool) {
	job.mu.Lock()
	skip := scope.State == "error"
	job.mu.Unlock()
	if skip {
		return
	}
	fail := func(message string) {
		job.mu.Lock()
		defer job.mu.Unlock()
		scope.State = "error"
		scope.Error = message
		if ctx.Err() != nil {
			scope.State = "paused"
			scope.Error = "Search stopped; run it again to continue indexing"
		}
	}
	if !s.searchTargetUnchanged(scope.target) {
		fail("Target connection changed; run the search again")
		return
	}
	ex, err := s.Reg.For(scope.target)
	if err != nil {
		fail("Could not connect to this target")
		return
	}
	select {
	case s.searchSlots <- struct{}{}:
	case <-ctx.Done():
		fail("")
		return
	}
	if ctx.Err() != nil {
		<-s.searchSlots
		fail("")
		return
	}
	job.mu.Lock()
	scope.State = "indexing"
	job.mu.Unlock()
	flags := ""
	if reset {
		flags = " --reset"
	}
	cmd := scope.prefix + "python3 -c " + shellq.Quote(nativeRecordsScript+"\n"+nativeSearchScript) + flags + " " + shellq.Quote(scope.Agent) + " -- " + shellq.Quote(job.query)
	result, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 15})
	<-s.searchSlots
	if err != nil {
		fail("Could not search this target")
		return
	}
	var reply searchWorkerReply
	if json.Unmarshal([]byte(result.Stdout), &reply) != nil || !result.OK() || reply.Profile == "" {
		fail("Native search failed on this target; check Python 3 and the native profile")
		return
	}
	job.mu.Lock()
	scope.Progress = reply.Progress
	scope.More = reply.More
	scope.profile = reply.Profile
	scope.matches = reply.Matches
	if reply.Progress.Complete {
		scope.State = "complete"
	}
	job.mu.Unlock()
}
func searchResultID(scope *conversationSearchScope, m searchMatch) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d/%s/%s/%s/%s/%d/%s", scope.TargetID, scope.Agent, scope.profile, m.CID, m.Cwd, m.Offset, m.Fingerprint)))
	return hex.EncodeToString(sum[:16])
}

// Caller holds job.mu, including while encoding the scope progress snapshot.
func searchResults(job *conversationSearchJob) []conversationSearchResult {
	results := []conversationSearchResult{}
	seen := map[string]bool{}
	for _, scope := range job.scopes {
		for _, m := range scope.matches {
			id := searchResultID(scope, m)
			if seen[id] {
				continue
			}
			seen[id] = true
			results = append(results, conversationSearchResult{ID: id, Scope: scope.ID, TargetID: scope.TargetID, Target: scope.Target, Agent: scope.Agent, Conversation: m.CID, Cwd: m.Cwd, Title: m.Title, Modified: m.Modified, Role: m.Role, Snippet: m.Snippet})
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Modified == results[j].Modified {
			return results[i].ID < results[j].ID
		}
		return results[i].Modified > results[j].Modified
	})
	return results
}
func (s *Server) writeConversationSearch(w http.ResponseWriter, job *conversationSearchJob, status int) {
	job.mu.Lock()
	complete := job.done
	scopes := make([]conversationSearchScope, 0, len(job.scopes))
	for _, scope := range job.scopes {
		if scope.State != "complete" {
			complete = false
		}
		scopes = append(scopes, *scope)
	}
	reply := map[string]any{"id": job.id, "query": job.query, "done": job.done, "complete": complete, "scopes": scopes, "results": searchResults(job)}
	job.mu.Unlock()
	// A slow browser must not hold the progress lock while the target is indexing.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, reply)
}
func (s *Server) conversationSearchJob(w http.ResponseWriter, r *http.Request) *conversationSearchJob {
	s.searchMu.Lock()
	defer s.searchMu.Unlock()
	job := s.searchJobs[r.PathValue("search")]
	if job != nil && time.Since(job.created) > 15*time.Minute {
		job.cancel()
		delete(s.searchJobs, job.id)
		job = nil
	}
	if job == nil {
		httpError(w, 404, "search expired or not found; run it again")
	}
	return job
}
func (s *Server) getConversationSearch(w http.ResponseWriter, r *http.Request) {
	if job := s.conversationSearchJob(w, r); job != nil {
		s.writeConversationSearch(w, job, 200)
	}
}
func (s *Server) cancelConversationSearch(w http.ResponseWriter, r *http.Request) {
	if job := s.conversationSearchJob(w, r); job != nil {
		job.cancel()
		s.writeConversationSearch(w, job, 202)
	}
}
func (s *Server) readConversationSearchResult(w http.ResponseWriter, r *http.Request) {
	job := s.conversationSearchJob(w, r)
	if job == nil {
		return
	}
	chosen, match := lookupSearchMatch(job, r.PathValue("result"))
	if chosen == nil {
		httpError(w, 404, "search result changed; refresh the search")
		return
	}
	if !s.searchTargetUnchanged(chosen.target) {
		httpError(w, 409, "target connection changed; run the search again")
		return
	}
	mode, anchor := "match", ""
	for _, key := range []string{"before", "after", "latest"} {
		if values, ok := r.URL.Query()[key]; ok {
			if mode != "match" || len(values) != 1 {
				httpError(w, 422, "choose one conversation page")
				return
			}
			mode = key
			if key == "latest" {
				if values[0] != "1" {
					httpError(w, 422, "latest must be 1")
					return
				}
			} else {
				value, err := strconv.ParseInt(values[0], 10, 64)
				if err != nil || value < 0 {
					httpError(w, 422, "invalid page boundary")
					return
				}
				anchor = strconv.FormatInt(value, 10)
			}
		}
	}
	reply, status, err := s.readSearchMatch(r.Context(), job, chosen, match, mode, anchor)
	if err != nil {
		httpError(w, status, "%s", err)
		return
	}
	reply["fork_options"], _ = json.Marshal(searchForkOptions(job, chosen))
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, reply)
}

func (s *Server) readSearchMatch(parent context.Context, job *conversationSearchJob, chosen *conversationSearchScope, match searchMatch, mode, anchor string) (map[string]json.RawMessage, int, error) {
	ex, err := s.Reg.For(chosen.target)
	if err != nil {
		return nil, 502, fmt.Errorf("could not connect to this target")
	}
	script := "NATIVE_SEARCH_LIBRARY=True\n" + nativeRecordsScript + "\n" + nativeSearchScript + "\n" + nativeSearchReadScript
	args := []string{chosen.Agent, strconv.FormatInt(match.Document, 10), match.CID, match.Cwd, strconv.FormatInt(match.Offset, 10), match.Fingerprint, chosen.profile, job.query}
	args = append(args, mode)
	if anchor != "" {
		args = append(args, anchor)
	}
	cmd := chosen.prefix + "python3 -c " + shellq.Quote(script) + " --"
	for _, arg := range args {
		cmd += " " + shellq.Quote(arg)
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	select {
	case s.searchSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, 408, fmt.Errorf("conversation read canceled")
	}
	defer func() { <-s.searchSlots }()
	result, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 15})
	var reply map[string]json.RawMessage
	if err != nil || json.Unmarshal([]byte(result.Stdout), &reply) != nil {
		return nil, 502, fmt.Errorf("could not read the matching conversation")
	}
	if !result.OK() {
		return nil, 409, fmt.Errorf("conversation or native profile changed; run the search again")
	}
	return reply, 200, nil
}

func searchForkOptions(job *conversationSearchJob, chosen *conversationSearchScope) []searchLaunchChoice {
	job.mu.Lock()
	defer job.mu.Unlock()
	choices := []searchLaunchChoice{}
	seen := map[string]bool{}
	for _, scope := range job.scopes {
		if scope.TargetID != chosen.TargetID || scope.Agent != chosen.Agent || scope.profile != chosen.profile {
			continue
		}
		for _, choice := range scope.launches {
			if !seen[choice.signature] {
				seen[choice.signature] = true
				choices = append(choices, choice)
			}
		}
	}
	return choices
}

func lookupSearchMatch(job *conversationSearchJob, resultID string) (*conversationSearchScope, searchMatch) {
	var chosen *conversationSearchScope
	var match searchMatch
	job.mu.Lock()
	for _, scope := range job.scopes {
		for _, m := range scope.matches {
			if searchResultID(scope, m) == resultID {
				copy := *scope
				chosen = &copy
				match = m
				break
			}
		}
		if chosen != nil {
			break
		}
	}
	job.mu.Unlock()
	return chosen, match
}
