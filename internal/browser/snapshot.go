package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type axValue struct {
	Value json.RawMessage `json:"value"`
}

func (v *axValue) text() string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(v.Value, &s) == nil {
		return s
	}
	return strings.Trim(string(v.Value), `"`)
}

type axNode struct {
	NodeID     string   `json:"nodeId"`
	Ignored    bool     `json:"ignored"`
	Role       *axValue `json:"role"`
	Name       *axValue `json:"name"`
	Value      *axValue `json:"value"`
	ParentID   string   `json:"parentId"`
	ChildIDs   []string `json:"childIds"`
	BackendID  int64    `json:"backendDOMNodeId"`
	Properties []axProp `json:"properties"`
}

type axProp struct {
	Name  string   `json:"name"`
	Value *axValue `json:"value"`
}

// Snapshot is the page as an agent reads it: an accessibility outline in which
// every element it can act on carries a ref for click and fill.
type Snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Tree  string `json:"tree"`
	Refs  int    `json:"refs"`
}

// roles that are pure structure: their children are shown in their place.
var skipRoles = map[string]bool{"generic": true, "none": true, "presentation": true, "InlineTextBox": true,
	"LineBreak": true, "RootWebArea": false, "group": false}

var actionable = map[string]bool{"button": true, "link": true, "textbox": true, "searchbox": true, "checkbox": true,
	"radio": true, "combobox": true, "listbox": true, "option": true, "menuitem": true, "menuitemcheckbox": true,
	"menuitemradio": true, "tab": true, "switch": true, "slider": true, "spinbutton": true, "treeitem": true,
	"textarea": true, "PopUpButton": true, "image": false, "img": false}

const snapshotLimit = 60000

// Snapshot reads the page's accessibility tree. Refs from any earlier
// snapshot stop working: a ref names an element as it was when read.
func (b *Browser) Snapshot(ctx context.Context) (Snapshot, error) {
	var res struct {
		Nodes []axNode `json:"nodes"`
	}
	if err := b.conn.Call(ctx, b.page, "Accessibility.getFullAXTree", map[string]any{}, &res); err != nil {
		return Snapshot{}, err
	}
	byID := make(map[string]*axNode, len(res.Nodes))
	var root *axNode
	for i := range res.Nodes {
		n := &res.Nodes[i]
		byID[n.NodeID] = n
		if n.ParentID == "" && root == nil {
			root = n
		}
	}
	refs := map[int]int64{}
	next := 0
	var out strings.Builder
	truncated := false
	var walk func(n *axNode, depth int)
	walk = func(n *axNode, depth int) {
		if n == nil || truncated {
			return
		}
		role, name := n.Role.text(), strings.Join(strings.Fields(n.Name.text()), " ")
		shown := !n.Ignored && !skipRoles[role] && role != ""
		if role == "StaticText" {
			// Text a named parent already reads out is noise.
			shown = name != ""
			if p := byID[n.ParentID]; p != nil && strings.Join(strings.Fields(p.Name.text()), " ") == name {
				shown = false
			}
		}
		if role == "RootWebArea" {
			shown = false
		}
		childDepth := depth
		if shown {
			line := strings.Repeat("  ", depth) + "- "
			if role == "StaticText" {
				line += "text: " + clip(name, 300)
			} else {
				line += role
				if name != "" {
					line += fmt.Sprintf(" %q", clip(name, 200))
				}
				for _, p := range n.Properties {
					switch p.Name {
					case "checked", "selected", "expanded", "pressed", "disabled", "level":
						if v := p.Value.text(); v != "" && v != "false" {
							line += fmt.Sprintf(" [%s=%s]", p.Name, v)
						}
					}
				}
				if n.BackendID > 0 && (actionable[role] || role == "heading" || name != "") {
					next++
					refs[next] = n.BackendID
					line += fmt.Sprintf(" [ref=%d]", next)
				}
				if v := n.Value.text(); v != "" && (role == "textbox" || role == "searchbox" || role == "combobox" || role == "spinbutton" || role == "slider") {
					line += ": " + clip(v, 200)
				}
			}
			if out.Len()+len(line) > snapshotLimit {
				out.WriteString(strings.Repeat("  ", depth) + "- … (snapshot truncated; the page is larger)\n")
				truncated = true
				return
			}
			out.WriteString(line + "\n")
			childDepth++
		}
		for _, id := range n.ChildIDs {
			walk(byID[id], childDepth)
		}
	}
	walk(root, 0)
	b.mu.Lock()
	b.refs = refs
	st := State{URL: b.url, Title: b.title}
	b.mu.Unlock()
	tree := out.String()
	if tree == "" {
		tree = "(the page has no readable content)\n"
	}
	return Snapshot{URL: st.URL, Title: st.Title, Tree: tree, Refs: next}, nil
}
