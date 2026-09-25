package memory

import (
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

// Item is one delivered memory record, described for the operator rather than
// for the model. Whatever the store calls these, what a person needs in order
// to judge a delivery is: which record it was, where it came from, and enough
// of the text to recognise it. See docs/memory-visibility.md.
type Item struct {
	ID      string `json:"id,omitempty"`
	Source  string `json:"source,omitempty"`
	Title   string `json:"title,omitempty"`
	Snippet string `json:"snippet,omitempty"`
}

// Delivery is one recorded context injection, as the API reports it. It mirrors
// a memory_deliveries row plus the parsed items, so a client never has to know
// the row stores its items as JSON.
type Delivery struct {
	ID        int64   `json:"id"`
	SessionID *int64  `json:"session_id"`
	TaskID    *int64  `json:"task_id"`
	AttemptID *int64  `json:"attempt_id"`
	At        float64 `json:"at"`
	Mode      string  `json:"mode"`
	Bytes     int     `json:"bytes"`
	Items     []Item  `json:"items"`
}

// snippetLimit bounds how much of one item is kept in the delivery log. The
// log answers "was this the right memory", not "what did it say" — the item's
// own source is a click away.
const snippetLimit = 300

// Size is how many bytes of context this delivery actually handed over. The
// budget in Automatic is a byte bound, so this is the honest number to record.
func (r ContextResult) Size() int { return len(r.Context) }

// ItemsJSON renders the delivered items for the delivery log's items_json
// column. It never fails: an item that will not marshal would be a bug in a
// plain struct of strings, and losing the rest of the record over it would be
// worse than losing that one entry.
func (r ContextResult) ItemsJSON() string {
	if len(r.Items) == 0 {
		// json.Marshal renders a nil slice as "null"; the column and every
		// reader expect a list, so an item-less delivery says so explicitly.
		return "[]"
	}
	raw, err := json.Marshal(r.Items)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

// ParseItems reads an items_json column back. It tolerates the empty and the
// malformed alike: a bad blob degrades the Memory section, it does not take the
// session or task view down with it.
func ParseItems(raw string) []Item {
	out := []Item{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil || out == nil {
		return []Item{}
	}
	return out
}

// normalizeItems turns whatever the store sent into the log's item shape.
//
// Grimoire is not the only thing that can answer this endpoint and its `items`
// field is not something lectern controls, so three shapes are accepted: a list
// of objects (the expected one), an object keyed by record id, and nothing at
// all — in which case the delivery's own keys are all the log can honestly
// say. Unknown fields inside an item are ignored.
func normalizeItems(raw json.RawMessage, keys []string) []Item {
	items := itemList(raw)
	if len(items) == 0 {
		items = itemMap(raw)
	}
	if len(items) == 0 {
		return itemsFromKeys(keys)
	}
	out := items[:0]
	seen := map[string]bool{}
	for _, item := range items {
		if item.ID != "" {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
		}
		if item.ID == "" && item.Source == "" && item.Title == "" && item.Snippet == "" {
			continue
		}
		out = append(out, item)
	}
	return out
}

func itemList(raw json.RawMessage) []Item {
	var rows []map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil
	}
	out := make([]Item, 0, len(rows))
	for _, row := range rows {
		out = append(out, itemFrom(row, ""))
	}
	return out
}

// itemMap accepts {"<id>": {...}} as well as a plain list — a store that keys
// its items by id is still describing the same delivery.
func itemMap(raw json.RawMessage) []Item {
	var rows map[string]map[string]any
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Item, 0, len(ids))
	for _, id := range ids {
		out = append(out, itemFrom(rows[id], id))
	}
	return out
}

func itemFrom(row map[string]any, fallbackID string) Item {
	id := firstString(row, "id", "key", "fingerprint", "memory_id")
	if id == "" {
		id = fallbackID
	}
	return Item{
		ID:      id,
		Source:  firstString(row, "source", "path", "note", "note_path", "origin"),
		Title:   firstString(row, "title", "heading", "topic", "name"),
		Snippet: snippet(firstString(row, "snippet", "text", "chunk", "body", "content")),
	}
}

// itemsFromKeys is the fallback when the store describes its context only by
// the fingerprints it deduplicates on. A key is usually "<path>#<n>"; the path
// half is worth keeping, and the key itself is better than nothing.
func itemsFromKeys(keys []string) []Item {
	out := make([]Item, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		item := Item{ID: key}
		if path, _, found := strings.Cut(key, "#"); found && path != "" {
			item.Source = path
		}
		out = append(out, item)
	}
	return out
}

// snippet clips on a rune boundary, so a stored snippet is always valid UTF-8
// even when the limit lands mid-character.
func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= snippetLimit {
		return s
	}
	cut := snippetLimit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
