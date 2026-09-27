package trackers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Jira is a Jira Cloud site or a Jira Server / Data Center instance.
//
// Cloud authenticates with an Atlassian account email plus an API token
// (HTTP Basic) and speaks REST v3, whose rich text is Atlassian Document
// Format. Server and Data Center authenticate with a personal access token
// (Bearer) and speak REST v2, whose rich text is wiki markup strings. The
// token never leaves the Lectern server.
type Jira struct {
	BaseURL string
	Flavor  string // cloud | server; empty guesses from the host
	Email   string // Cloud only
	Token   string
	HTTP    *http.Client
}

// JiraFlavor resolves an empty flavor: *.atlassian.net is Cloud, anything
// else is Server/Data Center.
func JiraFlavor(baseURL, flavor string) string {
	switch flavor {
	case "cloud", "server":
		return flavor
	}
	if u, err := url.Parse(baseURL); err == nil && strings.HasSuffix(strings.ToLower(u.Hostname()), ".atlassian.net") {
		return "cloud"
	}
	return "server"
}

func (j *Jira) cloud() bool { return JiraFlavor(j.BaseURL, j.Flavor) == "cloud" }

func (j *Jira) api() string {
	if j.cloud() {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

// Validate checks the connection settings before anything is sent.
func (j *Jira) Validate() error {
	u, err := url.Parse(strings.TrimSpace(j.BaseURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf(`jira needs "base_url" like https://yourteam.atlassian.net`)
	}
	if strings.TrimSpace(j.Token) == "" {
		return fmt.Errorf("jira needs a token: an API token (Cloud) or a personal access token (Server/Data Center)")
	}
	if j.cloud() && strings.TrimSpace(j.Email) == "" {
		return fmt.Errorf("jira cloud needs the Atlassian account email the API token belongs to")
	}
	return nil
}

func (j *Jira) do(ctx context.Context, method, path string, body any, out any) error {
	if err := j.Validate(); err != nil {
		return err
	}
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(j.BaseURL, "/")+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if j.cloud() {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(j.Email+":"+j.Token)))
	} else {
		req.Header.Set("Authorization", "Bearer "+j.Token)
	}
	client := j.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("jira: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		return fmt.Errorf("jira rejected the credentials (HTTP %d)", resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("jira returned HTTP %d: %s", resp.StatusCode, jiraErrorText(raw))
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("jira returned unexpected output: %s", clip(string(raw), 200))
	}
	return nil
}

func jiraErrorText(raw []byte) string {
	var e struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(raw, &e) == nil {
		parts := append([]string{}, e.ErrorMessages...)
		for k, v := range e.Errors {
			parts = append(parts, k+": "+v)
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	return clip(string(raw), 200)
}

type jiraUser struct {
	AccountID    string `json:"accountId"`
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

func (u *jiraUser) label() string {
	if u == nil {
		return ""
	}
	return firstNonEmpty(u.DisplayName, u.Name, u.EmailAddress)
}

// Identities are the forms an allowlist may name a person by.
func (u *jiraUser) Identities() []string {
	if u == nil {
		return nil
	}
	var out []string
	for _, s := range []string{u.EmailAddress, u.AccountID, u.Name, u.Key, u.DisplayName} {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

type jiraStatus struct {
	Name     string `json:"name"`
	Category struct {
		Key string `json:"key"` // new | indeterminate | done
	} `json:"statusCategory"`
}

type jiraComment struct {
	Author  *jiraUser       `json:"author"`
	Body    json.RawMessage `json:"body"`
	Created string          `json:"created"`
}

// JiraIssue is the raw issue shape both API versions share (rich text
// fields differ and are decoded by richText).
type JiraIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
		Status      *jiraStatus     `json:"status"`
		Assignee    *jiraUser       `json:"assignee"`
		Reporter    *jiraUser       `json:"reporter"`
		Creator     *jiraUser       `json:"creator"`
		Labels      []string        `json:"labels"`
		Priority    *struct {
			Name string `json:"name"`
		} `json:"priority"`
		IssueType *struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		Created  string      `json:"created"`
		Updated  string      `json:"updated"`
		Subtasks []JiraIssue `json:"subtasks"`
		Parent   *JiraIssue  `json:"parent"`
		Comment  *struct {
			Comments []jiraComment `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
}

// Reporter is the issue's reporter, falling back to its creator.
func (i JiraIssue) Reporter() *jiraUser {
	if i.Fields.Reporter != nil {
		return i.Fields.Reporter
	}
	return i.Fields.Creator
}

// Description is the issue's description as plain text.
func (i JiraIssue) Description() string { return richText(i.Fields.Description) }

// BrowseURL is the issue's page on the Jira site.
func (j *Jira) BrowseURL(key string) string {
	return strings.TrimRight(j.BaseURL, "/") + "/browse/" + url.PathEscape(key)
}

func (j *Jira) item(i JiraIssue) Item {
	it := Item{Source: "jira", Kind: "issue", ID: i.Key, Title: i.Fields.Summary, URL: j.BrowseURL(i.Key),
		UpdatedAt: i.Fields.Updated, Assignees: []string{}, Labels: glLabels(i.Fields.Labels)}
	if i.Fields.Status != nil {
		it.State, it.StatusType = i.Fields.Status.Name, i.Fields.Status.Category.Key
	}
	if a := i.Fields.Assignee.label(); a != "" {
		it.Assignees = []string{a}
	}
	it.Author = i.Reporter().label()
	if i.Fields.Priority != nil {
		it.Priority = i.Fields.Priority.Name
	}
	if i.Fields.Parent != nil {
		it.Parent = i.Fields.Parent.Key
	}
	return it
}

const jiraListFields = "summary,status,assignee,reporter,creator,updated,created,labels,priority,issuetype,parent"

// Search runs a JQL query. Cloud uses the /search/jql endpoint that
// replaced /search in 2025; Server/Data Center still have /search.
// extraFields adds to the list fields (the trigger asks for "description").
func (j *Jira) Search(ctx context.Context, jql string, max int, extraFields ...string) ([]JiraIssue, error) {
	if max <= 0 || max > 100 {
		max = 50
	}
	fields := strings.Join(append([]string{jiraListFields}, extraFields...), ",")
	q := url.Values{"jql": {jql}, "maxResults": {fmt.Sprint(max)}, "fields": {fields}}
	path := j.api() + "/search?" + q.Encode()
	if j.cloud() {
		path = j.api() + "/search/jql?" + q.Encode()
	}
	var res struct {
		Issues []JiraIssue `json:"issues"`
	}
	if err := j.do(ctx, "GET", path, nil, &res); err != nil {
		return nil, err
	}
	return res.Issues, nil
}

// JQLString quotes a value for JQL.
func JQLString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// ListJQL builds the hub's query from a filter: a project (optional), a
// base JQL the connection configured (optional), then the filter.
func ListJQL(projectKey, base string, f Filter) string {
	var parts []string
	if strings.TrimSpace(base) != "" {
		parts = append(parts, "("+strings.TrimSpace(base)+")")
	} else if projectKey != "" {
		parts = append(parts, "project = "+JQLString(projectKey))
	}
	switch f.State {
	case "", "open":
		parts = append(parts, "statusCategory != Done")
	case "closed":
		parts = append(parts, "statusCategory = Done")
	}
	switch f.Mine {
	case "assigned":
		parts = append(parts, "assignee = currentUser()")
	case "authored":
		parts = append(parts, "reporter = currentUser()")
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		parts = append(parts, "text ~ "+JQLString(s))
	}
	return strings.Join(parts, " AND ") + " ORDER BY updated DESC"
}

// Issues lists issues for the hub.
func (j *Jira) Issues(ctx context.Context, projectKey, baseJQL string, f Filter) ([]Item, error) {
	rows, err := j.Search(ctx, ListJQL(projectKey, baseJQL, f), f.limit())
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(rows))
	for _, i := range rows {
		out = append(out, j.item(i))
	}
	return out, nil
}

// Issue reads one issue with description, comments, sub-tasks and the
// transitions available from its current status.
func (j *Jira) Issue(ctx context.Context, key string) (*IssueDetail, error) {
	var i JiraIssue
	path := j.api() + "/issue/" + url.PathEscape(key) + "?fields=" + url.QueryEscape(jiraListFields+",description,subtasks,comment")
	if err := j.do(ctx, "GET", path, nil, &i); err != nil {
		return nil, err
	}
	d := &IssueDetail{Item: j.item(i), Body: j.markdown(i.Fields.Description), CreatedAt: i.Fields.Created, Editable: true,
		BodyLossy: j.cloud() && ADFLossy(i.Fields.Description),
		Children:  []Item{}, Timeline: []Event{}, Transitions: []Transition{}, BranchName: BranchName(i.Key, i.Fields.Summary)}
	for _, s := range i.Fields.Subtasks {
		d.Children = append(d.Children, j.item(s))
	}
	if i.Fields.Parent != nil {
		p := j.item(*i.Fields.Parent)
		d.ParentItem = &p
	}
	if i.Fields.Comment != nil {
		for _, c := range i.Fields.Comment.Comments {
			d.Timeline = append(d.Timeline, Event{Kind: "comment", Author: c.Author.label(), Body: j.markdown(c.Body), At: c.Created})
		}
	}
	if ts, err := j.Transitions(ctx, key); err == nil {
		d.Transitions = ts
	}
	return d, nil
}

// Transitions lists what the issue can move to from where it is now.
func (j *Jira) Transitions(ctx context.Context, key string) ([]Transition, error) {
	var res struct {
		Transitions []struct {
			ID   string     `json:"id"`
			Name string     `json:"name"`
			To   jiraStatus `json:"to"`
		} `json:"transitions"`
	}
	if err := j.do(ctx, "GET", j.api()+"/issue/"+url.PathEscape(key)+"/transitions", nil, &res); err != nil {
		return nil, err
	}
	out := make([]Transition, 0, len(res.Transitions))
	for _, t := range res.Transitions {
		name := t.Name
		if t.To.Name != "" && !strings.EqualFold(t.To.Name, t.Name) {
			name = t.Name + " → " + t.To.Name
		}
		out = append(out, Transition{ID: t.ID, Name: name, Type: t.To.Category.Key})
	}
	return out, nil
}

// Transition moves an issue through a transition by id.
func (j *Jira) Transition(ctx context.Context, key, id string) error {
	return j.do(ctx, "POST", j.api()+"/issue/"+url.PathEscape(key)+"/transitions",
		map[string]any{"transition": map[string]any{"id": id}}, nil)
}

// TransitionByName finds a transition whose name or destination status is
// name (case-insensitive) and applies it. A missing one is an error, never
// a guess.
func (j *Jira) TransitionByName(ctx context.Context, key, name string) error {
	var res struct {
		Transitions []struct {
			ID   string     `json:"id"`
			Name string     `json:"name"`
			To   jiraStatus `json:"to"`
		} `json:"transitions"`
	}
	if err := j.do(ctx, "GET", j.api()+"/issue/"+url.PathEscape(key)+"/transitions", nil, &res); err != nil {
		return err
	}
	for _, t := range res.Transitions {
		if strings.EqualFold(t.Name, name) || strings.EqualFold(t.To.Name, name) {
			return j.Transition(ctx, key, t.ID)
		}
	}
	return fmt.Errorf("issue %s has no transition to %q from its current status", key, name)
}

// Comment adds a comment written in Markdown: converted to ADF on Cloud and
// to wiki markup on Server/Data Center.
func (j *Jira) Comment(ctx context.Context, key, body string) error {
	return j.do(ctx, "POST", j.api()+"/issue/"+url.PathEscape(key)+"/comment", map[string]any{"body": j.rich(body)}, nil)
}

// SetDescription replaces the issue's description with Markdown, converted
// the same way as a comment.
func (j *Jira) SetDescription(ctx context.Context, key, md string) error {
	return j.do(ctx, "PUT", j.api()+"/issue/"+url.PathEscape(key), map[string]any{"fields": map[string]any{"description": j.rich(md)}}, nil)
}

func (j *Jira) rich(md string) any {
	if j.cloud() {
		return MarkdownToADF(md)
	}
	return MarkdownToWiki(md)
}

// markdown renders a description or comment body for the editor: ADF on
// Cloud, wiki markup on Server.
func (j *Jira) markdown(raw json.RawMessage) string {
	if j.cloud() {
		return richText(raw)
	}
	return WikiToMarkdown(richText(raw))
}

// Myself is the token's own user, for Test connection.
func (j *Jira) Myself(ctx context.Context) (string, error) {
	var u jiraUser
	if err := j.do(ctx, "GET", j.api()+"/myself", nil, &u); err != nil {
		return "", err
	}
	return u.label(), nil
}

// ---- rich text -----------------------------------------------------------

// richText turns a description or comment body into plain text: a v2 string
// passes through; a v3 ADF document is flattened, keeping paragraphs, list
// bullets, headings and code blocks readable.
func richText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var doc adfNode
	if json.Unmarshal(raw, &doc) != nil {
		return ""
	}
	var b strings.Builder
	doc.write(&b, "")
	return strings.TrimSpace(collapseBlankLines(b.String()))
}

type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Marks   []adfMark      `json:"marks"`
	Attrs   map[string]any `json:"attrs"`
	Content []adfNode      `json:"content"`
}

func (n adfNode) write(b *strings.Builder, indent string) {
	switch n.Type {
	case "text":
		b.WriteString(adfMarked(n))
	case "hardBreak":
		b.WriteString("\n" + indent)
	case "mention", "emoji", "status":
		if t, ok := n.Attrs["text"].(string); ok {
			b.WriteString(t)
		} else if t, ok := n.Attrs["shortName"].(string); ok {
			b.WriteString(t)
		}
	case "inlineCard", "blockCard":
		if u, ok := n.Attrs["url"].(string); ok {
			b.WriteString(u)
		}
	case "heading":
		level := 1
		if l, ok := n.Attrs["level"].(float64); ok {
			level = int(l)
		}
		b.WriteString(strings.Repeat("#", level) + " ")
		n.children(b, indent)
		b.WriteString("\n\n")
	case "paragraph":
		n.children(b, indent)
		b.WriteString("\n\n")
	case "bulletList", "orderedList":
		for i, item := range n.Content {
			mark := "- "
			if n.Type == "orderedList" {
				mark = fmt.Sprintf("%d. ", i+1)
			}
			b.WriteString(indent + mark)
			var inner strings.Builder
			item.children(&inner, indent+"  ")
			b.WriteString(strings.ReplaceAll(strings.TrimSpace(inner.String()), "\n\n", "\n") + "\n")
		}
		b.WriteString("\n")
	case "codeBlock":
		b.WriteString("```\n")
		n.children(b, indent)
		b.WriteString("\n```\n\n")
	case "blockquote":
		var inner strings.Builder
		n.children(&inner, indent)
		for _, l := range strings.Split(strings.TrimSpace(inner.String()), "\n") {
			b.WriteString("> " + l + "\n")
		}
		b.WriteString("\n")
	case "rule":
		b.WriteString("---\n\n")
	default:
		n.children(b, indent)
	}
}

func (n adfNode) children(b *strings.Builder, indent string) {
	for _, c := range n.Content {
		c.write(b, indent)
	}
}

func collapseBlankLines(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}

// textADF wraps plain text as an ADF document, one paragraph per blank-line
// separated block.
func textADF(text string) map[string]any {
	var paras []any
	for _, block := range strings.Split(strings.TrimSpace(text), "\n\n") {
		var content []any
		for i, line := range strings.Split(block, "\n") {
			if i > 0 {
				content = append(content, map[string]any{"type": "hardBreak"})
			}
			if line != "" {
				content = append(content, map[string]any{"type": "text", "text": line})
			}
		}
		paras = append(paras, map[string]any{"type": "paragraph", "content": content})
	}
	return map[string]any{"type": "doc", "version": 1, "content": paras}
}
