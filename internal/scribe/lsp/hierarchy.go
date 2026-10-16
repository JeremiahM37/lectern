package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Hierarchy directions navigate accepts.
const (
	hierIncoming = "incoming_calls"
	hierOutgoing = "outgoing_calls"
	hierSuper    = "supertypes"
	hierSub      = "subtypes"
)

// hierarchyAliases maps the words a model is likely to use to the actions.
var hierarchyAliases = map[string]string{
	"incoming": hierIncoming, "incoming_call": hierIncoming, "callers": hierIncoming, "calls_in": hierIncoming, "call_in": hierIncoming,
	"outgoing": hierOutgoing, "outgoing_call": hierOutgoing, "callees": hierOutgoing, "calls_out": hierOutgoing, "call_out": hierOutgoing,
	"super": hierSuper, "supertype": hierSuper, "super_types": hierSuper, "parents": hierSuper,
	"sub": hierSub, "subtype": hierSub, "sub_types": hierSub, "children": hierSub,
}

// hierarchyItem is a CallHierarchyItem or TypeHierarchyItem. Raw keeps the
// item exactly as the server sent it, because servers read back their own
// data field.
type hierarchyItem struct {
	Name           string          `json:"name"`
	Kind           int             `json:"kind"`
	Detail         string          `json:"detail"`
	URI            string          `json:"uri"`
	Range          Range           `json:"range"`
	SelectionRange Range           `json:"selectionRange"`
	Raw            json.RawMessage `json:"-"`
}

// HierarchyItem is a symbol in a call or type hierarchy.
type HierarchyItem struct {
	Name   string
	Kind   string
	Detail string
	Loc    Location // where the name is
}

// HierarchyEdge is one step of a hierarchy: an item, and for calls the places
// in the source where the call happens.
type HierarchyEdge struct {
	Item  HierarchyItem
	Sites []Location
}

// Hierarchy answers one direction of a call or type hierarchy at p in abs. It
// returns the symbol at p and its related symbols one step away. dir is one of
// incoming_calls, outgoing_calls, supertypes or subtypes. The server is needed;
// there is no text fallback.
func (m *Manager) Hierarchy(ctx context.Context, abs string, p Point, dir string) (HierarchyItem, []HierarchyEdge, error) {
	var target HierarchyItem
	var edges []HierarchyEdge
	typed := dir == hierSuper || dir == hierSub
	prepare := "textDocument/prepareCallHierarchy"
	if typed {
		prepare = "textDocument/prepareTypeHierarchy"
	}
	err := m.run(ctx, abs, func(s *session, sr syncResult) error {
		var raws []json.RawMessage
		if err := s.call(ctx, prepare, textDocumentPosition(sr.uri, pointToPosition(sr.text, p)), &raws); err != nil {
			return err
		}
		items := decodeHierarchyItems(raws)
		if len(items) == 0 {
			return nil
		}
		first := items[0]
		c := &converter{}
		target = c.item(first)
		// Use the first item, which is the one at the position.
		switch dir {
		case hierIncoming:
			var got []struct {
				From       hierarchyItem `json:"from"`
				FromRanges []Range       `json:"fromRanges"`
			}
			if err := s.call(ctx, "callHierarchy/incomingCalls", map[string]any{"item": first.rawValue()}, &got); err != nil {
				return err
			}
			for _, g := range got {
				edges = append(edges, HierarchyEdge{Item: c.item(g.From), Sites: c.sites(g.From.URI, g.FromRanges)})
			}
		case hierOutgoing:
			var got []struct {
				To         hierarchyItem `json:"to"`
				FromRanges []Range       `json:"fromRanges"`
			}
			if err := s.call(ctx, "callHierarchy/outgoingCalls", map[string]any{"item": first.rawValue()}, &got); err != nil {
				return err
			}
			for _, g := range got {
				edges = append(edges, HierarchyEdge{Item: c.item(g.To), Sites: c.sites(first.URI, g.FromRanges)})
			}
		case hierSuper, hierSub:
			method := "typeHierarchy/supertypes"
			if dir == hierSub {
				method = "typeHierarchy/subtypes"
			}
			var got []json.RawMessage
			if err := s.call(ctx, method, map[string]any{"item": first.rawValue()}, &got); err != nil {
				return err
			}
			for _, it := range decodeHierarchyItems(got) {
				edges = append(edges, HierarchyEdge{Item: c.item(it)})
			}
		default:
			return fmt.Errorf("unknown hierarchy direction %q", dir)
		}
		return nil
	})
	if err != nil {
		return HierarchyItem{}, nil, err
	}
	return target, edges, nil
}

func decodeHierarchyItems(raws []json.RawMessage) []hierarchyItem {
	var out []hierarchyItem
	for _, raw := range raws {
		var it hierarchyItem
		if err := json.Unmarshal(raw, &it); err != nil || it.URI == "" {
			continue
		}
		it.Raw = raw
		out = append(out, it)
	}
	return out
}

// rawValue is what a request sends back for this item.
func (h hierarchyItem) rawValue() json.RawMessage {
	if len(h.Raw) > 0 {
		return h.Raw
	}
	b, _ := json.Marshal(h)
	return b
}

func (c *converter) item(h hierarchyItem) HierarchyItem {
	return HierarchyItem{
		Name:   h.Name,
		Kind:   symbolKindName(h.Kind),
		Detail: h.Detail,
		Loc:    c.location(h.URI, h.SelectionRange),
	}
}

func (c *converter) sites(uri string, ranges []Range) []Location {
	out := make([]Location, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, c.location(uri, r))
	}
	return out
}

// hierarchyResult renders one direction of a hierarchy for the model.
func hierarchyResult(m *Manager, dir string, target HierarchyItem, edges []HierarchyEdge) tools.Result {
	where := fmt.Sprintf("%s:%d:%d", m.RelPath(target.Loc.Path), target.Loc.Line, target.Loc.Column)
	head := fmt.Sprintf("%s (%s) at %s", target.Name, target.Kind, where)
	if len(edges) == 0 {
		label := map[string]string{hierIncoming: "callers", hierOutgoing: "callees", hierSuper: "supertypes", hierSub: "subtypes"}[dir]
		return tools.Result{Output: fmt.Sprintf("No %s of %s.", label, head), Summary: "none"}
	}
	var b strings.Builder
	files := map[string]bool{}
	switch dir {
	case hierIncoming:
		fmt.Fprintf(&b, "Callers of %s:\n", head)
	case hierOutgoing:
		fmt.Fprintf(&b, "Calls made by %s:\n", head)
	case hierSuper:
		fmt.Fprintf(&b, "Supertypes of %s:\n", head)
	case hierSub:
		fmt.Fprintf(&b, "Subtypes of %s:\n", head)
	}
	for _, e := range edges {
		loc := fmt.Sprintf("%s:%d:%d", m.RelPath(e.Item.Loc.Path), e.Item.Loc.Line, e.Item.Loc.Column)
		files[m.RelPath(e.Item.Loc.Path)] = true
		fmt.Fprintf(&b, "  %s (%s) %s", e.Item.Name, e.Item.Kind, loc)
		if len(e.Sites) > 0 {
			lines := make([]string, 0, len(e.Sites))
			for _, s := range e.Sites {
				lines = append(lines, fmt.Sprint(s.Line))
			}
			label := "calls at line"
			if dir == hierOutgoing {
				label = "called at line"
			}
			if len(lines) > 1 {
				label += "s"
			}
			fmt.Fprintf(&b, "  %s %s", label, strings.Join(lines, ", "))
		}
		b.WriteString("\n")
	}
	return tools.Result{
		Output:  strings.TrimRight(b.String(), "\n"),
		Summary: fmt.Sprintf("%d %s in %d file(s)", len(edges), dir, len(files)),
	}
}
