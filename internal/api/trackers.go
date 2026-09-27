// The Tasks hub (docs/trackers.md): pull requests and issues from the
// project's GitHub/GitLab repository and issues from Linear and Jira, read
// and acted on without leaving Lectern. internal/trackers holds the
// adapters; this file is the HTTP surface for connections, the forge a
// project resolves to, and the aggregated list. trackers_pr.go has the pull
// request page and its actions; trackers_work.go starts agents from items.
//
// Trust rules, the same as the rest of the API: reads need an authenticated
// caller; anything that changes a tracker (merge, reviewers, labels,
// comments, state, transitions) or configures a credential needs a signed-in
// human (Auth.CanDecide), exactly like approvals and arming the CI loop.
// Credentials are never returned — only which fields are set.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/trackers"
	"github.com/JeremiahM37/lectern/v2/internal/triggers"
)

// registerTrackerRoutes is called from Handler; kept here so the route list
// for this feature lives beside its handlers.
func (s *Server) registerTrackerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{id}/trackers", s.listTrackers)
	mux.HandleFunc("POST /api/projects/{id}/trackers", s.createTracker)
	mux.HandleFunc("PATCH /api/trackers/{tid}", s.patchTracker)
	mux.HandleFunc("DELETE /api/trackers/{tid}", s.deleteTracker)
	mux.HandleFunc("POST /api/trackers/{tid}/test", s.testTracker)
	mux.HandleFunc("GET /api/projects/{id}/work", s.listWork)
	mux.HandleFunc("POST /api/projects/{id}/work/start", s.startWork)

	mux.HandleFunc("GET /api/projects/{id}/forge/meta", s.forgeMeta)
	mux.HandleFunc("GET /api/projects/{id}/forge/prs/{n}", s.forgePR)
	mux.HandleFunc("GET /api/projects/{id}/forge/prs/{n}/conflicts", s.forgeConflicts)
	mux.HandleFunc("GET /api/projects/{id}/forge/prs/{n}/log", s.forgeCheckLog)
	mux.HandleFunc("POST /api/projects/{id}/forge/prs/{n}/merge", s.forgeMerge)
	mux.HandleFunc("DELETE /api/projects/{id}/forge/prs/{n}/auto-merge", s.forgeDisableAutoMerge)
	mux.HandleFunc("POST /api/projects/{id}/forge/prs/{n}/reviewers", s.forgeReviewers)
	mux.HandleFunc("POST /api/projects/{id}/forge/prs/{n}/resolve", s.forgeResolve)
	mux.HandleFunc("POST /api/projects/{id}/forge/prs/{n}/fix-checks", s.forgeFixChecks)
	mux.HandleFunc("GET /api/projects/{id}/forge/issues/{n}", s.forgeIssue)
	mux.HandleFunc("POST /api/projects/{id}/forge/{kind}/{n}/labels", s.forgeLabels)
	mux.HandleFunc("POST /api/projects/{id}/forge/{kind}/{n}/comments", s.forgeComment)
	mux.HandleFunc("POST /api/projects/{id}/forge/{kind}/{n}/state", s.forgeState)

	mux.HandleFunc("GET /api/trackers/{tid}/teams", s.trackerTeams)
	mux.HandleFunc("GET /api/trackers/{tid}/states", s.trackerStates)
	mux.HandleFunc("GET /api/trackers/{tid}/issues", s.trackerIssues)
	mux.HandleFunc("GET /api/trackers/{tid}/issues/{key}", s.trackerIssue)
	mux.HandleFunc("POST /api/trackers/{tid}/issues/{key}/status", s.trackerIssueStatus)
	mux.HandleFunc("POST /api/trackers/{tid}/issues/{key}/comments", s.trackerIssueComment)

	mux.HandleFunc("POST /api/projects/{id}/forge/{kind}/{n}/reactions", s.forgeReact)
	mux.HandleFunc("POST /api/trackers/{tid}/issues/{key}/reactions", s.trackerIssueReact)
	mux.HandleFunc("POST /api/trackers/{tid}/issues/{key}/description", s.trackerIssueDescription)
	mux.HandleFunc("GET /api/projects/{id}/forge/queue", s.forgeQueue)
	mux.HandleFunc("POST /api/projects/{id}/forge/queue/remove", s.forgeDequeue)
}

// trackerHuman is the write gate — see the file comment.
func (s *Server) trackerHuman(w http.ResponseWriter, r *http.Request, what string) bool {
	p, _ := auth.FromContext(r.Context())
	if s.Auth == nil || !s.Auth.CanDecide(p) {
		httpError(w, 403, "%s requires a signed-in human (tailscale identity or access token), not an automated caller", what)
		return false
	}
	return true
}

// ---- connections ---------------------------------------------------------

type trackerView struct {
	*store.TrackerConnection
	Config  map[string]any  `json:"config"`
	Secrets map[string]bool `json:"secrets"`
	// Borrowed is set when the key comes from a trigger source of the same
	// kind on this project rather than from this connection.
	Borrowed bool `json:"borrowed,omitempty"`
}

func (s *Server) trackerView(c *store.TrackerConnection) trackerView {
	v := trackerView{TrackerConnection: c, Config: store.UnjObj(c.ConfigJSON), Secrets: triggers.RedactSecrets(c.SecretsJSON)}
	if c.Kind == "linear" || c.Kind == "jira" {
		key := "api_key"
		if c.Kind == "jira" {
			key = "token"
		}
		if !v.Secrets[key] && s.borrowedSecret(c) != "" {
			v.Borrowed = true
		}
	}
	return v
}

type forgeView struct {
	Kind   string `json:"kind,omitempty"`
	Host   string `json:"host,omitempty"`
	Repo   string `json:"repo,omitempty"`
	URL    string `json:"url,omitempty"`
	Source string `json:"source,omitempty"` // remote | connection
	Error  string `json:"error,omitempty"`
}

func (s *Server) listTrackers(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.trackerProject(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.TrackerConnections(proj.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	views := make([]trackerView, 0, len(rows))
	for _, c := range rows {
		views = append(views, s.trackerView(c))
	}
	fv := forgeView{}
	if ref, source, err := s.forgeRef(r.Context(), proj); err != nil {
		fv.Error = err.Error()
	} else {
		fv = forgeView{Kind: ref.Kind, Host: ref.Host, Repo: ref.Path, URL: ref.WebURL(), Source: source}
	}
	writeJSON(w, 200, map[string]any{"forge": fv, "connections": views})
}

type trackerIn struct {
	Kind    string          `json:"kind"`
	Name    string          `json:"name"`
	Config  json.RawMessage `json:"config"`
	Secrets json.RawMessage `json:"secrets"`
}

// validateTracker checks a connection's settings; secrets may be empty for
// Linear/Jira when a trigger source of the same kind can lend its key.
func validateTracker(kind, configJSON string) error {
	cfg := store.UnjObj(configJSON)
	str := func(k string) string { v, _ := cfg[k].(string); return strings.TrimSpace(v) }
	switch kind {
	case "github", "gitlab", "bitbucket", "gitea", "azure":
		if repo := str("repo"); repo != "" {
			if kind == "gitea" && str("host") == "" {
				return fmt.Errorf(`a Gitea/Forgejo repository needs its "host"`)
			}
			if _, err := trackers.ParseRemote("https://"+firstNonEmptyStr(str("host"), forgeDefaultHost[kind])+"/"+repo, kind); err != nil {
				return err
			}
		}
		if b := str("base_url"); b != "" && !strings.HasPrefix(b, "https://") && !strings.HasPrefix(b, "http://") {
			return fmt.Errorf(`"base_url" must be an http(s) URL`)
		}
		if f := str("flavor"); f != "" && f != "cloud" && f != "server" {
			return fmt.Errorf(`"flavor" must be cloud or server`)
		}
	case "linear":
	case "jira":
		j := trackers.Jira{BaseURL: str("base_url"), Flavor: str("flavor"), Email: str("email"), Token: "x"}
		if err := j.Validate(); err != nil {
			return err
		}
		if f := str("flavor"); f != "" && f != "cloud" && f != "server" {
			return fmt.Errorf(`jira "flavor" must be cloud or server`)
		}
	default:
		return fmt.Errorf("unknown tracker kind %q; use github, gitlab, bitbucket, gitea, azure, linear or jira", kind)
	}
	return nil
}

func (s *Server) createTracker(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "connecting a tracker") {
		return
	}
	proj, ok := s.trackerProject(w, r)
	if !ok {
		return
	}
	var in trackerIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	configJSON, secretsJSON := jsonOrEmpty(in.Config), jsonOrEmpty(in.Secrets)
	if err := validateTracker(in.Kind, configJSON); err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = forgeName[in.Kind]
	}
	c, err := s.DB.InsertTrackerConnection(&store.TrackerConnection{ProjectID: proj.ID, Kind: in.Kind, Name: name,
		ConfigJSON: configJSON, SecretsJSON: secretsJSON})
	if err != nil {
		respondErr(w, err)
		return
	}
	s.forgeCacheDrop(proj.ID)
	writeJSON(w, 201, s.trackerView(c))
}

func (s *Server) trackerParam(w http.ResponseWriter, r *http.Request) (*store.TrackerConnection, bool) {
	id, err := pathID(r, "tid")
	if err != nil {
		httpError(w, 404, "no such tracker connection")
		return nil, false
	}
	c, err := s.DB.TrackerConnection(id)
	if err != nil {
		httpError(w, 404, "no such tracker connection")
		return nil, false
	}
	return c, true
}

func (s *Server) patchTracker(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "changing a tracker connection") {
		return
	}
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	var in trackerIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{}
	if n := strings.TrimSpace(in.Name); n != "" {
		fields["name"] = n
	}
	if len(in.Config) > 0 {
		if err := validateTracker(c.Kind, string(in.Config)); err != nil {
			httpError(w, 400, "%s", err.Error())
			return
		}
		fields["config_json"] = string(in.Config)
	}
	// An empty secrets body leaves stored secrets untouched, as for triggers.
	if len(in.Secrets) > 0 && strings.TrimSpace(string(in.Secrets)) != "{}" {
		fields["secrets_json"] = string(in.Secrets)
	}
	if len(fields) > 0 {
		fields["updated_at"] = store.Now()
		if err := s.DB.Update("tracker_connections", c.ID, fields); err != nil {
			respondErr(w, err)
			return
		}
	}
	s.forgeCacheDrop(c.ProjectID)
	fresh, err := s.DB.TrackerConnection(c.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, s.trackerView(fresh))
}

func (s *Server) deleteTracker(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "removing a tracker connection") {
		return
	}
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	if err := s.DB.DeleteTrackerConnection(c.ID); err != nil {
		respondErr(w, err)
		return
	}
	s.forgeCacheDrop(c.ProjectID)
	w.WriteHeader(204)
}

func (s *Server) testTracker(w http.ResponseWriter, r *http.Request) {
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var msg string
	var err error
	switch c.Kind {
	case "linear":
		var who string
		if who, err = s.linearClient(c).Viewer(ctx); err == nil {
			msg = "Linear key works as " + who
		}
	case "jira":
		var who string
		if who, err = s.jiraClient(c).Myself(ctx); err == nil {
			msg = "Jira token works as " + who
		}
	default:
		var proj *store.Project
		if proj, err = s.DB.Project(c.ProjectID); err == nil {
			var f trackers.Forge
			if f, err = s.projectForge(ctx, proj); err == nil {
				if _, err = f.List(ctx, "pr", trackers.Filter{Limit: 1}); err == nil {
					if cli := map[string]string{"github": "gh", "gitlab": "glab"}[f.Kind()]; cli != "" {
						msg = fmt.Sprintf("%s on %s can read %s", cli, proj.TargetName, f.Repo().Path)
					} else {
						msg = fmt.Sprintf("the %s token can read %s", forgeName[f.Kind()], f.Repo().Path)
					}
				}
			}
		}
	}
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": msg})
}

// borrowedSecret is a Linear or Jira key from one of the project's trigger
// sources of the same kind, so a key configured once for the trigger also
// works for the hub. Jira only borrows from a source for the same site.
func (s *Server) borrowedSecret(c *store.TrackerConnection) string {
	sources, err := s.DB.TriggerSources(c.ProjectID)
	if err != nil {
		return ""
	}
	cfg := store.UnjObj(c.ConfigJSON)
	for _, src := range sources {
		if src.Kind != c.Kind {
			continue
		}
		switch c.Kind {
		case "linear":
			if sec, err := triggers.ParseLinearSecrets(src.SecretsJSON); err == nil && sec.APIKey != "" {
				return sec.APIKey
			}
		case "jira":
			tc, _ := triggers.ParseJiraConfig(src.ConfigJSON)
			base, _ := cfg["base_url"].(string)
			if strings.TrimRight(tc.BaseURL, "/") != strings.TrimRight(base, "/") {
				continue
			}
			if sec, err := triggers.ParseJiraSecrets(src.SecretsJSON); err == nil && sec.Token != "" {
				return sec.Token
			}
		}
	}
	return ""
}

func (s *Server) trackerHTTP() *http.Client {
	if s.Triggers != nil && s.Triggers.HTTP != nil {
		return s.Triggers.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (s *Server) linearClient(c *store.TrackerConnection) *trackers.Linear {
	sec, _ := triggers.ParseLinearSecrets(c.SecretsJSON)
	key := sec.APIKey
	if key == "" {
		key = s.borrowedSecret(c)
	}
	cfg := store.UnjObj(c.ConfigJSON)
	u, _ := cfg["api_url"].(string) // test seam and self-proxied setups
	return &trackers.Linear{APIKey: key, URL: u, HTTP: s.trackerHTTP()}
}

func (s *Server) jiraClient(c *store.TrackerConnection) *trackers.Jira {
	sec, _ := triggers.ParseJiraSecrets(c.SecretsJSON)
	tok := sec.Token
	if tok == "" {
		tok = s.borrowedSecret(c)
	}
	cfg := store.UnjObj(c.ConfigJSON)
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	return &trackers.Jira{BaseURL: str("base_url"), Flavor: str("flavor"), Email: str("email"), Token: tok, HTTP: s.trackerHTTP()}
}

// ---- the project's forge -------------------------------------------------

func (s *Server) trackerProject(w http.ResponseWriter, r *http.Request) (*store.Project, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return nil, false
	}
	proj, err := s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return nil, false
	}
	return proj, true
}

func (s *Server) trackerExec(proj *store.Project) (executor.Executor, error) {
	target, err := s.DB.Target(proj.TargetID)
	if err != nil {
		return nil, err
	}
	return s.Reg.For(target)
}

type forgeCacheEntry struct {
	ref RepoRefWithSource
	at  time.Time
}

// RepoRefWithSource is a detected repository and where it came from.
type RepoRefWithSource struct {
	trackers.RepoRef
	Source string
}

var (
	forgeCacheMu sync.Mutex
	forgeCache   = map[string]forgeCacheEntry{}
)

func (s *Server) forgeCacheKey(projectID int64) string { return fmt.Sprintf("%p/%d", s, projectID) }

func (s *Server) forgeCacheDrop(projectID int64) {
	forgeCacheMu.Lock()
	delete(forgeCache, s.forgeCacheKey(projectID))
	forgeCacheMu.Unlock()
}

// forgeRef is the project's code host repository: a github/gitlab
// connection's explicit repo when there is one, else the clone's origin
// remote read on the target (cached for ten minutes — it rarely changes and
// costs an ssh round trip).
func (s *Server) forgeRef(ctx context.Context, proj *store.Project) (trackers.RepoRef, string, error) {
	conns, _ := s.DB.TrackerConnections(proj.ID)
	hint, flavor := "", ""
	for _, c := range conns {
		if !isForgeKind(c.Kind) {
			continue
		}
		hint = c.Kind
		cfg := store.UnjObj(c.ConfigJSON)
		repo, _ := cfg["repo"].(string)
		host, _ := cfg["host"].(string)
		flavor, _ = cfg["flavor"].(string)
		if strings.TrimSpace(repo) != "" {
			ref, err := trackers.ParseRemote("https://"+firstNonEmptyStr(strings.TrimSpace(host), forgeDefaultHost[c.Kind])+"/"+strings.TrimSpace(repo), c.Kind)
			if flavor != "" {
				ref.Flavor = flavor
			}
			return ref, "connection", err
		}
	}
	key := s.forgeCacheKey(proj.ID)
	forgeCacheMu.Lock()
	e, ok := forgeCache[key]
	forgeCacheMu.Unlock()
	if ok && time.Since(e.at) < 10*time.Minute && (hint == "" || e.ref.Kind == hint) {
		ref := e.ref.RepoRef
		if flavor != "" {
			ref.Flavor = flavor
		}
		return ref, e.ref.Source, nil
	}
	ex, err := s.trackerExec(proj)
	if err != nil {
		return trackers.RepoRef{}, "", err
	}
	res, err := ex.Run(ctx, "git -C "+executor.ShellQuote(proj.RepoPath)+" remote get-url origin", executor.RunOpts{Timeout: 20})
	if err != nil {
		return trackers.RepoRef{}, "", err
	}
	if !res.OK() || strings.TrimSpace(res.Stdout) == "" {
		return trackers.RepoRef{}, "", fmt.Errorf("this project's clone has no origin remote; add a GitHub or GitLab connection naming the repository")
	}
	ref, err := trackers.ParseRemote(strings.TrimSpace(res.Stdout), hint)
	if err != nil {
		return trackers.RepoRef{}, "", err
	}
	forgeCacheMu.Lock()
	forgeCache[key] = forgeCacheEntry{ref: RepoRefWithSource{ref, "remote"}, at: time.Now()}
	forgeCacheMu.Unlock()
	return ref, "remote", nil
}

// forgeName and forgeDefaultHost describe the code hosts a project's
// repository can live on.
var forgeName = map[string]string{"github": "GitHub", "gitlab": "GitLab", "bitbucket": "Bitbucket", "gitea": "Gitea",
	"azure": "Azure DevOps", "linear": "Linear", "jira": "Jira"}

var forgeDefaultHost = map[string]string{"github": "github.com", "gitlab": "gitlab.com", "bitbucket": "bitbucket.org", "azure": "dev.azure.com"}

func isForgeKind(k string) bool {
	return k == "github" || k == "gitlab" || k == "bitbucket" || k == "gitea" || k == "azure"
}

// projectForge is the adapter for the project's repository. GitHub and
// GitLab go through the target's own CLI login; Bitbucket, Gitea/Forgejo and
// Azure DevOps go over HTTP with the token of the project's connection of
// that kind, from the Lectern server, like Linear and Jira.
func (s *Server) projectForge(ctx context.Context, proj *store.Project) (trackers.Forge, error) {
	ref, _, err := s.forgeRef(ctx, proj)
	if err != nil {
		return nil, err
	}
	switch ref.Kind {
	case "github", "gitlab":
		ex, err := s.trackerExec(proj)
		if err != nil {
			return nil, err
		}
		if ref.Kind == "gitlab" {
			return trackers.NewGitLab(ex, ref), nil
		}
		return trackers.NewGitHub(ex, ref), nil
	}
	conns, _ := s.DB.TrackerConnections(proj.ID)
	for _, c := range conns {
		if c.Kind != ref.Kind {
			continue
		}
		cfg := store.UnjObj(c.ConfigJSON)
		str := func(k string) string { v, _ := cfg[k].(string); return strings.TrimSpace(v) }
		var sec struct {
			Token string `json:"token"`
		}
		_ = json.Unmarshal([]byte(c.SecretsJSON), &sec)
		if sec.Token == "" {
			break
		}
		cr := trackers.Creds{BaseURL: str("base_url"), Username: str("username"), Token: sec.Token}
		switch ref.Kind {
		case "bitbucket":
			return trackers.NewBitbucket(ref, cr, s.trackerHTTP()), nil
		case "gitea":
			return trackers.NewGitea(ref, cr, s.trackerHTTP()), nil
		case "azure":
			return trackers.NewAzure(ref, cr, s.trackerHTTP()), nil
		}
	}
	return nil, fmt.Errorf("this project's repository is on %s (%s); add a %s connection with an access token in the project's Tasks hub settings",
		forgeName[ref.Kind], ref.Path, forgeName[ref.Kind])
}

// ---- the hub list --------------------------------------------------------

type workSource struct {
	Source       string `json:"source"`
	Kind         string `json:"kind,omitempty"`
	ConnectionID int64  `json:"connection_id,omitempty"`
	Name         string `json:"name"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	Count        int    `json:"count"`
}

func workFilter(r *http.Request) trackers.Filter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	return trackers.Filter{State: q.Get("state"), Query: q.Get("q"), Mine: q.Get("mine"), Limit: limit}
}

// listWork is the hub: every source for the project, fetched in parallel,
// merged newest first. One source failing (a missing gh login, a revoked
// key) is reported beside the others' results, never instead of them.
func (s *Server) listWork(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.trackerProject(w, r)
	if !ok {
		return
	}
	f := workFilter(r)
	want := r.URL.Query().Get("source") // "" = all
	kind := r.URL.Query().Get("kind")   // pr | issue | "" = both
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	type job struct {
		src workSource
		run func() ([]trackers.Item, error)
	}
	var jobs []job
	conns, _ := s.DB.TrackerConnections(proj.ID)
	forge, forgeErr := s.projectForge(ctx, proj)
	fsrc := "github"
	if forge != nil {
		fsrc = forge.Kind()
	}
	if want == "" || want == "github" || want == "gitlab" {
		for _, k := range []string{"pr", "issue"} {
			k := k
			// "review requested" only means something for pull requests
			if (kind != "" && kind != k) || (f.Mine == "review" && k == "issue") {
				continue
			}
			name := map[string]string{"pr": "Pull requests", "issue": "Issues"}[k]
			if forgeErr != nil {
				jobs = append(jobs, job{src: workSource{Source: fsrc, Kind: k, Name: name}, run: func() ([]trackers.Item, error) { return nil, forgeErr }})
				continue
			}
			jobs = append(jobs, job{src: workSource{Source: fsrc, Kind: k, Name: name}, run: func() ([]trackers.Item, error) { return forge.List(ctx, k, f) }})
		}
	}
	for _, c := range conns {
		c := c
		if (want != "" && want != c.Kind) || kind == "pr" || f.Mine == "review" {
			continue
		}
		cfg := store.UnjObj(c.ConfigJSON)
		str := func(k string) string { v, _ := cfg[k].(string); return v }
		switch c.Kind {
		case "linear":
			team := firstNonEmptyStr(r.URL.Query().Get("team"), str("team_key"))
			jobs = append(jobs, job{src: workSource{Source: "linear", Kind: "issue", ConnectionID: c.ID, Name: c.Name},
				run: func() ([]trackers.Item, error) { return s.linearClient(c).Issues(ctx, team, f) }})
		case "jira":
			jobs = append(jobs, job{src: workSource{Source: "jira", Kind: "issue", ConnectionID: c.ID, Name: c.Name},
				run: func() ([]trackers.Item, error) {
					return s.jiraClient(c).Issues(ctx, str("project_key"), str("jql"), f)
				}})
		}
	}
	results := make([][]trackers.Item, len(jobs))
	sources := make([]workSource, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			items, err := j.run()
			src := j.src
			if err != nil {
				src.Error = err.Error()
			} else {
				src.OK, src.Count = true, len(items)
				for k := range items {
					items[k].ConnectionID = src.ConnectionID
				}
			}
			results[i], sources[i] = items, src
		}(i, j)
	}
	wg.Wait()
	items := []trackers.Item{}
	for _, rs := range results {
		items = append(items, rs...)
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].UpdatedAt > items[j].UpdatedAt })
	writeJSON(w, 200, map[string]any{"items": items, "sources": sources})
}

// ---- Linear / Jira items -------------------------------------------------

func (s *Server) trackerTeams(w http.ResponseWriter, r *http.Request) {
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	if c.Kind != "linear" {
		writeJSON(w, 200, []trackers.Team{})
		return
	}
	teams, err := s.linearClient(c).Teams(r.Context())
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	writeJSON(w, 200, teams)
}

// trackerStates lists a Linear team's workflow states — the board's
// columns. Jira has no single list (transitions depend on the issue), so it
// answers empty and the board groups by status category instead.
func (s *Server) trackerStates(w http.ResponseWriter, r *http.Request) {
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	if c.Kind != "linear" {
		writeJSON(w, 200, []trackers.Transition{})
		return
	}
	team := firstNonEmptyStr(r.URL.Query().Get("team"), fmt.Sprint(store.UnjObj(c.ConfigJSON)["team_key"]))
	states, err := s.linearClient(c).States(r.Context(), team)
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	writeJSON(w, 200, states)
}

func (s *Server) trackerIssues(w http.ResponseWriter, r *http.Request) {
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	f := workFilter(r)
	cfg := store.UnjObj(c.ConfigJSON)
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	var items []trackers.Item
	var err error
	switch c.Kind {
	case "linear":
		items, err = s.linearClient(c).Issues(r.Context(), firstNonEmptyStr(r.URL.Query().Get("team"), str("team_key")), f)
	case "jira":
		items, err = s.jiraClient(c).Issues(r.Context(), str("project_key"), str("jql"), f)
	default:
		httpError(w, 400, "use /api/projects/{id}/work for a %s connection", c.Kind)
		return
	}
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	for i := range items {
		items[i].ConnectionID = c.ID
	}
	writeJSON(w, 200, items)
}

func (s *Server) trackerIssueDetail(ctx context.Context, c *store.TrackerConnection, key string) (*trackers.IssueDetail, error) {
	var d *trackers.IssueDetail
	var err error
	switch c.Kind {
	case "linear":
		d, err = s.linearClient(c).Issue(ctx, key)
	case "jira":
		d, err = s.jiraClient(c).Issue(ctx, key)
	default:
		return nil, invalid("a %s connection's issues are read through the project's forge", c.Kind)
	}
	if d != nil {
		d.ConnectionID = c.ID
	}
	return d, err
}

func (s *Server) trackerIssue(w http.ResponseWriter, r *http.Request) {
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	d, err := s.trackerIssueDetail(r.Context(), c, r.PathValue("key"))
	if err != nil {
		trackerErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

// trackerIssueStatus moves a Linear issue to a workflow state, or a Jira
// issue through a transition, by the id the issue page listed.
func (s *Server) trackerIssueStatus(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "changing an issue's status") {
		return
	}
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := decodeBody(r, &in); err != nil || strings.TrimSpace(in.ID) == "" {
		httpError(w, 422, "id (the state or transition to move to) is required")
		return
	}
	key := r.PathValue("key")
	var err error
	switch c.Kind {
	case "linear":
		var d *trackers.IssueDetail
		if d, err = s.linearClient(c).Issue(r.Context(), key); err == nil {
			// Linear's mutation wants the issue's id; the page knows its
			// identifier. Resolving it also proves the state belongs to the
			// issue's own team.
			known := false
			for _, t := range d.Transitions {
				known = known || t.ID == in.ID
			}
			if !known {
				httpError(w, 422, "that state is not one of %s's team's states", key)
				return
			}
			err = s.linearClient(c).SetState(r.Context(), firstNonEmptyStr(d.UID, key), in.ID)
		}
	case "jira":
		err = s.jiraClient(c).Transition(r.Context(), key, in.ID)
	default:
		httpError(w, 400, "not a Linear or Jira connection")
		return
	}
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	d, err := s.trackerIssueDetail(r.Context(), c, key)
	if err != nil {
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) trackerIssueComment(w http.ResponseWriter, r *http.Request) {
	if !s.trackerHuman(w, r, "commenting on an issue") {
		return
	}
	c, ok := s.trackerParam(w, r)
	if !ok {
		return
	}
	body, ok := commentBody(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	var err error
	switch c.Kind {
	case "linear":
		var d *trackers.IssueDetail
		if d, err = s.linearClient(c).Issue(r.Context(), key); err == nil {
			err = s.linearClient(c).Comment(r.Context(), firstNonEmptyStr(d.UID, key), body)
		}
	case "jira":
		err = s.jiraClient(c).Comment(r.Context(), key, body)
	default:
		httpError(w, 400, "not a Linear or Jira connection")
		return
	}
	if err != nil {
		httpError(w, 502, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func commentBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	var in struct {
		Body string `json:"body"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return "", false
	}
	body := strings.TrimSpace(in.Body)
	if body == "" {
		httpError(w, 422, "body is required")
		return "", false
	}
	if len(body) > 65000 {
		httpError(w, 422, "comment is too long")
		return "", false
	}
	return body, true
}

// trackerErr reports a tracker failure: a validation error as 422, a CLI
// that is not signed in as 424 (the fix is on the target, not here), and
// anything else the tracker said as 502.
func trackerErr(w http.ResponseWriter, err error) {
	var ce *trackers.CLIError
	var ve *validationError
	switch {
	case errors.As(err, &ve):
		httpError(w, 422, "%s", ve.Error())
	case errors.As(err, &ce) && ce.Auth:
		httpError(w, 424, "%s", ce.Error())
	default:
		httpError(w, 502, "%s", err.Error())
	}
}
