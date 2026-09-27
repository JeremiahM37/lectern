package trackers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// DefaultLinearURL is Linear's GraphQL endpoint.
const DefaultLinearURL = "https://api.linear.app/graphql"

// Linear is a Linear workspace reached with an API key. The key never
// leaves the Lectern server: it is read from the store for each request and
// sent only to Linear.
type Linear struct {
	APIKey string
	URL    string // DefaultLinearURL when empty; tests point it at a fake
	HTTP   *http.Client
}

func (l *Linear) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	u := l.URL
	if u == "" {
		u = DefaultLinearURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.APIKey)
	client := l.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return fmt.Errorf("linear rejected the API key (HTTP %d)", resp.StatusCode)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("linear returned HTTP %d: %s", resp.StatusCode, clip(string(raw), 200))
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("linear: %s", strings.Join(msgs, "; "))
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("linear returned HTTP %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

type lnUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Email       string `json:"email"`
}

func (u *lnUser) label() string {
	if u == nil {
		return ""
	}
	return firstNonEmpty(u.DisplayName, u.Name, u.Email)
}

type lnState struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Type  string  `json:"type"`
	Color string  `json:"color"`
	Pos   float64 `json:"position"`
}

type lnIssue struct {
	ID            string   `json:"id"`
	Identifier    string   `json:"identifier"`
	Title         string   `json:"title"`
	Description   string   `json:"description"`
	URL           string   `json:"url"`
	BranchName    string   `json:"branchName"`
	PriorityLabel string   `json:"priorityLabel"`
	CreatedAt     string   `json:"createdAt"`
	UpdatedAt     string   `json:"updatedAt"`
	State         *lnState `json:"state"`
	Assignee      *lnUser  `json:"assignee"`
	Creator       *lnUser  `json:"creator"`
	Team          *struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	} `json:"team"`
	Labels struct {
		Nodes []struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"nodes"`
	} `json:"labels"`
	Parent *struct {
		ID         string `json:"id"`
		Identifier string `json:"identifier"`
		Title      string `json:"title"`
		URL        string `json:"url"`
	} `json:"parent"`
	Children *struct {
		Nodes []lnIssue `json:"nodes"`
	} `json:"children"`
	Comments *struct {
		Nodes []struct {
			ID        string  `json:"id"`
			Body      string  `json:"body"`
			CreatedAt string  `json:"createdAt"`
			User      *lnUser `json:"user"`
		} `json:"nodes"`
	} `json:"comments"`
}

const lnIssueFields = `id identifier title url branchName priorityLabel createdAt updatedAt
  state { id name type color position } assignee { name displayName email } creator { name displayName email }
  team { id key } labels { nodes { name color } } parent { id identifier title url }`

func (i lnIssue) item() Item {
	it := Item{Source: "linear", Kind: "issue", ID: i.Identifier, Title: i.Title, URL: i.URL,
		UpdatedAt: i.UpdatedAt, Priority: i.PriorityLabel, Assignees: []string{}, Labels: []Label{}}
	if i.State != nil {
		it.State, it.StatusType = i.State.Name, i.State.Type
	}
	if a := i.Assignee.label(); a != "" {
		it.Assignees = []string{a}
	}
	it.Author = i.Creator.label()
	for _, l := range i.Labels.Nodes {
		it.Labels = append(it.Labels, Label{Name: l.Name, Color: strings.TrimPrefix(l.Color, "#")})
	}
	if i.Parent != nil {
		it.Parent = i.Parent.Identifier
	}
	return it
}

// Team is a Linear team.
type Team struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Teams lists the teams the key can see.
func (l *Linear) Teams(ctx context.Context) ([]Team, error) {
	var res struct {
		Teams struct {
			Nodes []Team `json:"nodes"`
		} `json:"teams"`
	}
	if err := l.do(ctx, `query { teams(first: 100) { nodes { id key name } } }`, nil, &res); err != nil {
		return nil, err
	}
	return res.Teams.Nodes, nil
}

// States lists a team's workflow states in board order — the columns of the
// board view and the choices of the status menu.
func (l *Linear) States(ctx context.Context, teamKey string) ([]Transition, error) {
	var res struct {
		WorkflowStates struct {
			Nodes []lnState `json:"nodes"`
		} `json:"workflowStates"`
	}
	const q = `query($key: String!) { workflowStates(filter: {team: {key: {eq: $key}}}, first: 100) { nodes { id name type color position } } }`
	if err := l.do(ctx, q, map[string]any{"key": teamKey}, &res); err != nil {
		return nil, err
	}
	states := res.WorkflowStates.Nodes
	typeOrder := map[string]int{"triage": 0, "backlog": 1, "unstarted": 2, "started": 3, "completed": 4, "canceled": 5}
	sort.SliceStable(states, func(i, j int) bool {
		a, b := states[i], states[j]
		if typeOrder[a.Type] != typeOrder[b.Type] {
			return typeOrder[a.Type] < typeOrder[b.Type]
		}
		return a.Pos < b.Pos
	})
	out := make([]Transition, 0, len(states))
	for _, s := range states {
		out = append(out, Transition{ID: s.ID, Name: s.Name, Type: s.Type})
	}
	return out, nil
}

// Issues lists a team's issues, most recently updated first. State "open"
// (the default) hides completed and canceled issues.
func (l *Linear) Issues(ctx context.Context, teamKey string, f Filter) ([]Item, error) {
	filter := map[string]any{}
	if teamKey != "" {
		filter["team"] = map[string]any{"key": map[string]any{"eq": teamKey}}
	}
	switch f.State {
	case "", "open":
		filter["state"] = map[string]any{"type": map[string]any{"nin": []string{"completed", "canceled"}}}
	case "closed":
		filter["state"] = map[string]any{"type": map[string]any{"in": []string{"completed", "canceled"}}}
	}
	switch f.Mine {
	case "assigned":
		filter["assignee"] = map[string]any{"isMe": map[string]any{"eq": true}}
	case "authored":
		filter["creator"] = map[string]any{"isMe": map[string]any{"eq": true}}
	}
	if s := strings.TrimSpace(f.Query); s != "" {
		filter["title"] = map[string]any{"containsIgnoreCase": s}
	}
	var res struct {
		Issues struct {
			Nodes []lnIssue `json:"nodes"`
		} `json:"issues"`
	}
	q := `query($filter: IssueFilter, $first: Int) { issues(filter: $filter, first: $first, orderBy: updatedAt) { nodes { ` + lnIssueFields + ` } } }`
	if err := l.do(ctx, q, map[string]any{"filter": filter, "first": f.limit()}, &res); err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(res.Issues.Nodes))
	for _, i := range res.Issues.Nodes {
		out = append(out, i.item())
	}
	return out, nil
}

// Issue reads one issue — by identifier ("ENG-12") or id — with its
// description, comments, sub-issues and its team's states.
func (l *Linear) Issue(ctx context.Context, id string) (*IssueDetail, error) {
	var res struct {
		Issue *lnIssue `json:"issue"`
	}
	q := `query($id: String!) { issue(id: $id) { ` + lnIssueFields + ` description
	  children(first: 100) { nodes { ` + lnIssueFields + ` } }
	  comments(first: 100) { nodes { id body createdAt user { name displayName email } } } } }`
	if err := l.do(ctx, q, map[string]any{"id": id}, &res); err != nil {
		return nil, err
	}
	if res.Issue == nil {
		return nil, fmt.Errorf("no Linear issue %q", id)
	}
	i := res.Issue
	d := &IssueDetail{Item: i.item(), Body: i.Description, CreatedAt: i.CreatedAt, Children: []Item{},
		Timeline: []Event{}, Transitions: []Transition{}, BranchName: i.BranchName, UID: i.ID, Editable: true}
	if d.BranchName == "" {
		d.BranchName = BranchName(i.Identifier, i.Title)
	}
	if i.Team != nil {
		d.Team = i.Team.Key
		if states, err := l.States(ctx, i.Team.Key); err == nil {
			d.Transitions = states
		}
	}
	if i.Children != nil {
		for _, c := range i.Children.Nodes {
			d.Children = append(d.Children, c.item())
		}
	}
	if i.Parent != nil {
		d.ParentItem = &Item{Source: "linear", Kind: "issue", ID: i.Parent.Identifier, Title: i.Parent.Title, URL: i.Parent.URL}
	}
	if i.Comments != nil {
		for _, c := range i.Comments.Nodes {
			d.Timeline = append(d.Timeline, Event{ID: c.ID, Kind: "comment", Author: c.User.label(), Body: c.Body, At: c.CreatedAt})
		}
		sort.SliceStable(d.Timeline, func(a, b int) bool { return d.Timeline[a].At < d.Timeline[b].At })
	}
	return d, nil
}

// SetState moves an issue to a workflow state by id.
func (l *Linear) SetState(ctx context.Context, issueID, stateID string) error {
	var res struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	const q = `mutation($id: String!, $stateId: String!) { issueUpdate(id: $id, input: {stateId: $stateId}) { success } }`
	if err := l.do(ctx, q, map[string]any{"id": issueID, "stateId": stateID}, &res); err != nil {
		return err
	}
	if !res.IssueUpdate.Success {
		return fmt.Errorf("linear did not update the issue")
	}
	return nil
}

// Comment adds a comment (Markdown) to an issue.
func (l *Linear) Comment(ctx context.Context, issueID, body string) error {
	var res struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	const q = `mutation($id: String!, $body: String!) { commentCreate(input: {issueId: $id, body: $body}) { success } }`
	if err := l.do(ctx, q, map[string]any{"id": issueID, "body": body}, &res); err != nil {
		return err
	}
	if !res.CommentCreate.Success {
		return fmt.Errorf("linear did not add the comment")
	}
	return nil
}

// Viewer is the key's own user, for Test connection.
func (l *Linear) Viewer(ctx context.Context) (string, error) {
	var res struct {
		Viewer lnUser `json:"viewer"`
	}
	if err := l.do(ctx, `query { viewer { name displayName email } }`, nil, &res); err != nil {
		return "", err
	}
	return res.Viewer.label(), nil
}

// SetDescription replaces an issue's description (Linear stores Markdown).
func (l *Linear) SetDescription(ctx context.Context, issueID, md string) error {
	var res struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	const q = `mutation($id: String!, $d: String!) { issueUpdate(id: $id, input: {description: $d}) { success } }`
	if err := l.do(ctx, q, map[string]any{"id": issueID, "d": md}, &res); err != nil {
		return err
	}
	if !res.IssueUpdate.Success {
		return fmt.Errorf("linear did not update the issue")
	}
	return nil
}

// React adds an emoji reaction to an issue (subject "") or a comment.
func (l *Linear) React(ctx context.Context, issueID, commentID, emoji string) error {
	glyph := EmojiGlyph[emoji]
	if glyph == "" {
		return fmt.Errorf("unknown reaction %q", emoji)
	}
	input := map[string]any{"emoji": glyph}
	if commentID != "" {
		input["commentId"] = commentID
	} else {
		input["issueId"] = issueID
	}
	var res struct {
		ReactionCreate struct {
			Success bool `json:"success"`
		} `json:"reactionCreate"`
	}
	const q = `mutation($input: ReactionCreateInput!) { reactionCreate(input: $input) { success } }`
	if err := l.do(ctx, q, map[string]any{"input": input}, &res); err != nil {
		return err
	}
	if !res.ReactionCreate.Success {
		return fmt.Errorf("linear did not add the reaction")
	}
	return nil
}
