package config

import (
	"path"
	"strings"
)

// RouterRule sends a task to a model when the task's text matches it. Match is
// a keyword, or a glob when it has * or ?. An empty Match matches every task,
// so a rule with no Match is a catch-all. ModelID is "provider/model".
type RouterRule struct {
	Match    string `json:"match,omitempty"`
	ModelID  string `json:"model_id"`
	Priority int    `json:"priority,omitempty"`
}

// ResolveRouterRule returns the model for text. Among the rules that match, the
// highest priority wins; on a tie the rule listed first wins. Rules with no
// model id are ignored, so a half-written rule cannot blank the model choice.
// ok is false when no rule matches, and the caller keeps its own slot model.
func ResolveRouterRule(rules []RouterRule, text string) (modelID string, ok bool) {
	best := -1
	for i, r := range rules {
		if strings.TrimSpace(r.ModelID) == "" || !r.matches(text) {
			continue
		}
		if !ok || r.Priority > rules[best].Priority {
			best, ok = i, true
		}
	}
	if !ok {
		return "", false
	}
	return strings.TrimSpace(rules[best].ModelID), true
}

// matches says whether the rule applies to text. A glob is tested against each
// word of the text, and a keyword against the whole text, both case-insensitively.
func (r RouterRule) matches(text string) bool {
	match := strings.ToLower(strings.TrimSpace(r.Match))
	if match == "" {
		return true
	}
	lower := strings.ToLower(text)
	if !strings.ContainsAny(match, "*?") {
		return strings.Contains(lower, match)
	}
	for _, word := range strings.Fields(lower) {
		if ok, err := path.Match(match, word); err == nil && ok {
			return true
		}
	}
	return false
}
