// Package trackers reads and acts on the places work is tracked outside
// Lectern: pull requests and issues on GitHub (through the target's own `gh`)
// and GitLab (through the target's own `glab`), and issues in Linear and Jira
// (through their HTTP APIs with a key stored like a trigger source's).
//
// Forge calls run through the project's target executor, so they use exactly
// the login the commit/PR button and the CI loop already use and need no token
// in Lectern. Linear and Jira run from the Lectern server itself, the same as
// the Linear trigger. Nothing here decides who may do what: internal/api gates
// every write on a signed-in human.
package trackers

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// Label is a tracker label with its display colour (hex, no '#'), when the
// tracker has one.
type Label struct {
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// Item is one row of a list: a pull/merge request or an issue from any
// tracker, in one shape so the hub can mix them.
type Item struct {
	Source string `json:"source"` // github | gitlab | linear | jira
	Kind   string `json:"kind"`   // pr | issue
	// ID is what the tracker calls it: "42" for a forge number, "ENG-12" for
	// Linear, "PROJ-7" for Jira.
	ID           string   `json:"id"`
	ConnectionID int64    `json:"connection_id,omitempty"`
	Title        string   `json:"title"`
	URL          string   `json:"url"`
	State        string   `json:"state"` // forge: open | closed | merged; Linear/Jira: the status name
	StatusType   string   `json:"status_type,omitempty"`
	Author       string   `json:"author,omitempty"`
	Assignees    []string `json:"assignees"`
	Labels       []Label  `json:"labels"`
	UpdatedAt    string   `json:"updated_at"`
	Draft        bool     `json:"draft,omitempty"`
	Checks       string   `json:"checks,omitempty"` // pass | fail | pending | none
	Review       string   `json:"review,omitempty"` // approved | changes_requested | review_required
	Conflicts    bool     `json:"conflicts,omitempty"`
	Head         string   `json:"head,omitempty"`
	Base         string   `json:"base,omitempty"`
	Priority     string   `json:"priority,omitempty"`
	Parent       string   `json:"parent,omitempty"`
}

// Filter narrows a list. Mine is "", "assigned", "authored" or "review".
type Filter struct {
	State string // open (default) | closed | merged | all
	Query string
	Mine  string
	Limit int
}

func (f Filter) limit() int {
	if f.Limit <= 0 || f.Limit > 100 {
		return 50
	}
	return f.Limit
}

// Reaction is one emoji's count on a comment or body.
type Reaction struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
}

// Event is one entry of a conversation timeline.
type Event struct {
	// ID is what React takes to react to this comment; empty when the host
	// cannot react to it.
	ID        string     `json:"id,omitempty"`
	Kind      string     `json:"kind"` // comment | review | commit | event
	Author    string     `json:"author,omitempty"`
	Body      string     `json:"body,omitempty"`
	State     string     `json:"state,omitempty"` // a review's verdict
	At        string     `json:"at"`
	URL       string     `json:"url,omitempty"`
	Reactions []Reaction `json:"reactions,omitempty"`
}

// Check is one CI check on a pull request. ID is what JobLog takes back.
type Check struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Workflow string `json:"workflow,omitempty"`
	Status   string `json:"status"` // pass | fail | pending | skipping | cancel
	URL      string `json:"url,omitempty"`
	HasLog   bool   `json:"has_log"`
}

// Reviewer is someone asked for, or who gave, a review.
type Reviewer struct {
	Login string `json:"login"`
	State string `json:"state"` // requested | approved | changes_requested | commented | dismissed
	Team  bool   `json:"team,omitempty"`
}

// StackEntry is one pull request in a stack: the chain of PRs whose base is
// another PR's head branch.
type StackEntry struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Head    string `json:"head"`
	Base    string `json:"base"`
	URL     string `json:"url"`
	State   string `json:"state"`
	Current bool   `json:"current,omitempty"`
	Depth   int    `json:"depth"`
}

// MergeOptions is what the merge button may offer.
type MergeOptions struct {
	Methods             []string `json:"methods"` // merge | squash | rebase, repository-permitted
	Default             string   `json:"default"`
	DeleteBranchDefault bool     `json:"delete_branch_default"`
	AutoMergeAllowed    bool     `json:"auto_merge_allowed"`
	MergeQueue          bool     `json:"merge_queue"`
	CanMerge            bool     `json:"can_merge"`
}

// AutoMerge describes an armed auto-merge.
type AutoMerge struct {
	Method    string `json:"method,omitempty"`
	EnabledBy string `json:"enabled_by,omitempty"`
}

// PRDetail is everything the pull request page shows.
type PRDetail struct {
	Item
	Body       string     `json:"body"`
	HeadSHA    string     `json:"head_sha"`
	CrossRepo  bool       `json:"cross_repo"`
	Mergeable  string     `json:"mergeable"`   // mergeable | conflicting | unknown
	MergeState string     `json:"merge_state"` // clean | blocked | behind | dirty | unstable | draft | unknown
	AutoMerge  *AutoMerge `json:"auto_merge"`
	Reviewers  []Reviewer `json:"reviewers"`
	Timeline   []Event    `json:"timeline"`
	// CheckRuns is every check; Item.Checks is their one-word rollup.
	CheckRuns    []Check      `json:"check_runs"`
	Additions    int          `json:"additions"`
	Deletions    int          `json:"deletions"`
	ChangedFiles int          `json:"changed_files"`
	Stack        []StackEntry `json:"stack"`
	Merge        MergeOptions `json:"merge"`
	Reactions    []Reaction   `json:"reactions,omitempty"`
	CreatedAt    string       `json:"created_at,omitempty"`
	MergedAt     string       `json:"merged_at,omitempty"`
	ClosedAt     string       `json:"closed_at,omitempty"`
}

// Transition is a status an issue can be moved to: a Linear workflow state
// or a Jira transition.
type Transition struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

// IssueDetail is everything an issue page shows, for any tracker.
type IssueDetail struct {
	Item
	Body        string       `json:"body"`
	Timeline    []Event      `json:"timeline"`
	Reactions   []Reaction   `json:"reactions,omitempty"`
	CreatedAt   string       `json:"created_at,omitempty"`
	Children    []Item       `json:"children"`
	ParentItem  *Item        `json:"parent_item,omitempty"`
	Transitions []Transition `json:"transitions"`
	Team        string       `json:"team,omitempty"`
	// Editable is set when the description can be edited here (Linear,
	// Jira); BodyLossy when saving an edit would drop formatting the editor
	// cannot represent (a Jira table, panel or colour).
	Editable  bool `json:"editable,omitempty"`
	BodyLossy bool `json:"body_lossy,omitempty"`
	// UID is the tracker's internal id where it differs from ID (Linear's
	// UUID), for mutations that want it.
	UID string `json:"uid,omitempty"`
	// BranchName is the branch a session started from this issue should
	// use: the tracker's own suggestion when it has one (Linear), else ours.
	BranchName string `json:"branch_name"`
}

// MergeRequest is one merge action. HeadSHA, when set, makes the merge fail
// if the branch moved since the page was read — the confirmation named a
// commit, and that is the commit that merges.
type MergeRequest struct {
	Method       string `json:"method"`
	DeleteBranch bool   `json:"delete_branch"`
	Auto         bool   `json:"auto"`
	HeadSHA      string `json:"head_sha"`
}

// User is a person who can be asked for review.
type User struct {
	Login string `json:"login"`
	Name  string `json:"name,omitempty"`
}

// Forge is a code host reached through its CLI on the target.
type Forge interface {
	Kind() string
	Repo() RepoRef
	List(ctx context.Context, kind string, f Filter) ([]Item, error)
	PR(ctx context.Context, n int) (*PRDetail, error)
	Issue(ctx context.Context, n int) (*IssueDetail, error)
	// JobLog is one check's log, raw; callers trim and redact.
	JobLog(ctx context.Context, pr *PRDetail, checkID string) (string, error)
	Merge(ctx context.Context, n int, m MergeRequest) (string, error)
	DisableAutoMerge(ctx context.Context, n int) error
	EditReviewers(ctx context.Context, n int, add, remove []string) error
	EditLabels(ctx context.Context, kind string, n int, add, remove []string) error
	Comment(ctx context.Context, kind string, n int, body string) error
	SetState(ctx context.Context, kind string, n int, open bool) error
	Labels(ctx context.Context) ([]Label, error)
	Users(ctx context.Context) ([]User, error)
	// FetchRefs is what `git fetch origin` needs to have a PR's base and
	// head locally: the base branch, then the head.
	FetchRefs(pr *PRDetail) (base, head string)
	// React adds an emoji reaction (one of Emojis) to the item itself
	// (subject "") or to one of its comments (an Event.ID).
	React(ctx context.Context, kind string, n int, subject, emoji string) error
}

// Emojis are the reactions every host that has them shares, keyed the way
// GitHub names them; each adapter maps them to its own names.
var Emojis = []string{"+1", "-1", "laugh", "hooray", "confused", "heart", "rocket", "eyes"}

// ValidEmoji reports whether e is one of Emojis.
func ValidEmoji(e string) bool {
	for _, x := range Emojis {
		if x == e {
			return true
		}
	}
	return false
}

// EmojiGlyph is how a reaction name is shown.
var EmojiGlyph = map[string]string{"+1": "👍", "-1": "👎", "laugh": "😄", "hooray": "🎉", "confused": "😕", "heart": "❤️", "rocket": "🚀", "eyes": "👀"}

// QueueEntry is one pull request waiting in a merge queue (GitHub) or merge
// train (GitLab).
type QueueEntry struct {
	// ID is what Dequeue takes.
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Author     string `json:"author,omitempty"`
	Position   int    `json:"position"`
	Status     string `json:"status"`
	EnqueuedAt string `json:"enqueued_at,omitempty"`
	// ETASeconds is the host's own estimate, when it gives one.
	ETASeconds int    `json:"eta_seconds,omitempty"`
	Pipeline   string `json:"pipeline,omitempty"`
}

// Queuer is a forge with a merge queue.
type Queuer interface {
	Queue(ctx context.Context, base string) ([]QueueEntry, error)
	Dequeue(ctx context.Context, entry QueueEntry) error
	DefaultBranch(ctx context.Context) (string, error)
}

// ErrUnsupported is returned for an action a host does not have.
var ErrUnsupported = errors.New("this code host does not support that action")

// ---- repository detection ------------------------------------------------

// RepoRef names a repository on a code host.
type RepoRef struct {
	Kind string `json:"kind"` // github | gitlab | bitbucket | gitea | azure
	Host string `json:"host"`
	// Path is "owner/repo", "group/sub/repo" on GitLab, "PROJECT/repo" on
	// Bitbucket Data Center, "org/project/repo" on Azure DevOps.
	Path string `json:"path"`
	// Flavor tells Bitbucket Cloud ("cloud") from Data Center ("server").
	Flavor string `json:"flavor,omitempty"`
}

// Slug is the -R argument both CLIs take: OWNER/REPO on github.com, else
// HOST/OWNER/REPO.
func (r RepoRef) Slug() string {
	if r.Host == "" || r.Host == "github.com" {
		return r.Path
	}
	return r.Host + "/" + r.Path
}

// WebURL is the repository's page.
func (r RepoRef) WebURL() string {
	host := r.Host
	if host == "" {
		host = "github.com"
	}
	switch {
	case r.Kind == "azure":
		if org, rest, ok := strings.Cut(r.Path, "/"); ok {
			project, repo, _ := strings.Cut(rest, "/")
			return "https://" + host + "/" + org + "/" + project + "/_git/" + repo
		}
	case r.Kind == "bitbucket" && r.Flavor == "server":
		project, repo, _ := strings.Cut(r.Path, "/")
		return "https://" + host + "/projects/" + project + "/repos/" + repo
	}
	return "https://" + host + "/" + r.Path
}

var (
	scpRemoteRe = regexp.MustCompile(`^(?:[^@/\s]+@)?([^:/\s]+):(.+)$`)
	pathRe      = regexp.MustCompile(`^[A-Za-z0-9_.%-]+(?:/[A-Za-z0-9_.%-]+)+$`)
)

// ParseRemote reads a git remote URL (https, ssh:// or scp-like) into a
// RepoRef. kindHint ("github"/"gitlab"), when set, overrides guessing the
// host kind from its name — a self-hosted GitLab need not say "gitlab".
func ParseRemote(remote, kindHint string) (RepoRef, error) {
	remote = strings.TrimSpace(remote)
	var host, p string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		host, p = u.Hostname(), u.EscapedPath()
	} else if m := scpRemoteRe.FindStringSubmatch(remote); m != nil {
		host, p = m[1], m[2]
	} else {
		return RepoRef{}, fmt.Errorf("cannot read a repository from remote %q", remote)
	}
	host = strings.ToLower(host)
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	kind := kindHint
	if kind == "" {
		kind = hostKind(host)
		if kind == "" {
			return RepoRef{}, fmt.Errorf("%s is not a code host Lectern recognises; set the host kind in the project's tracker settings", host)
		}
	}
	ref := RepoRef{Kind: kind, Host: host}
	switch kind {
	case "azure":
		// dev.azure.com/ORG/PROJECT/_git/REPO, ORG.visualstudio.com/PROJECT/_git/REPO,
		// ssh.dev.azure.com:v3/ORG/PROJECT/REPO
		p = strings.TrimPrefix(p, "v3/")
		p = strings.Replace(p, "/_git/", "/", 1)
		if org, ok := strings.CutSuffix(host, ".visualstudio.com"); ok {
			p = org + "/" + p
		}
		ref.Host = "dev.azure.com"
		if strings.Count(p, "/") != 2 {
			return RepoRef{}, fmt.Errorf("%q is not an Azure DevOps organisation/project/repository", p)
		}
	case "bitbucket":
		ref.Flavor = "server"
		if host == "bitbucket.org" {
			ref.Flavor = "cloud"
		}
		p = strings.TrimPrefix(p, "scm/")
		if strings.Count(p, "/") != 1 {
			return RepoRef{}, fmt.Errorf("%q is not a Bitbucket workspace/repository", p)
		}
	case "github", "gitea":
		if strings.Count(p, "/") != 1 {
			return RepoRef{}, fmt.Errorf("%q is not an owner/repository path", p)
		}
	}
	if !pathRe.MatchString(p) {
		return RepoRef{}, fmt.Errorf("remote %q does not name an owner/repository", remote)
	}
	ref.Path = p
	return ref, nil
}

// hostKind guesses a code host from its name; "" when the name says nothing.
func hostKind(host string) string {
	switch {
	case host == "dev.azure.com" || host == "ssh.dev.azure.com" || strings.HasSuffix(host, ".visualstudio.com"):
		return "azure"
	case strings.Contains(host, "bitbucket"):
		return "bitbucket"
	case host == "codeberg.org" || strings.Contains(host, "gitea") || strings.Contains(host, "forgejo"):
		return "gitea"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	case strings.Contains(host, "github"):
		return "github"
	}
	return ""
}

// ---- branch names --------------------------------------------------------

// BranchName suggests a branch for work on an issue: the identifier, then a
// short slug of the title — "42-fix-login-timeout", "ENG-12-add-sso". It is
// always a valid git branch name.
func BranchName(id, title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	// Long titles are cut at a word boundary, not mid-word.
	if len(slug) > 48 {
		slug = slug[:48]
		if i := strings.LastIndexByte(slug, '-'); i > 0 {
			slug = slug[:i]
		}
	}
	idPart := strings.Trim(nonBranchRe.ReplaceAllString(id, "-"), "-")
	switch {
	case idPart == "" && slug == "":
		return "work"
	case idPart == "":
		return slug
	case slug == "":
		return idPart
	}
	return idPart + "-" + slug
}

var nonBranchRe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// ---- stacks --------------------------------------------------------------

// BuildStack finds the stack pull request n sits in, given every open PR
// (plus n itself): its ancestors (PRs whose head is its base, walked up), then
// n, then its descendants (PRs based on its head, depth first). A PR that is
// in no stack gets a one-entry result. Depth is 0 at the stack's root.
func BuildStack(all []StackEntry, n int) []StackEntry {
	byHead := map[string]StackEntry{}
	var cur *StackEntry
	for i := range all {
		byHead[all[i].Head] = all[i]
		if all[i].Number == n {
			c := all[i]
			cur = &c
		}
	}
	if cur == nil {
		return nil
	}
	var up []StackEntry
	seen := map[int]bool{cur.Number: true}
	base := cur.Base
	for {
		p, ok := byHead[base]
		if !ok || seen[p.Number] {
			break
		}
		seen[p.Number] = true
		up = append([]StackEntry{p}, up...)
		base = p.Base
	}
	out := make([]StackEntry, 0, len(up)+1)
	for i, e := range up {
		e.Depth = i
		out = append(out, e)
	}
	c := *cur
	c.Current, c.Depth = true, len(up)
	out = append(out, c)
	var walk func(head string, depth int)
	walk = func(head string, depth int) {
		for _, e := range all {
			if e.Base == head && !seen[e.Number] {
				seen[e.Number] = true
				e.Depth = depth
				out = append(out, e)
				walk(e.Head, depth+1)
			}
		}
	}
	walk(cur.Head, len(up)+1)
	return out
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
