package sessions

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Default session names (re-audit N7): two sessions both called "claude"
// could not be told apart, and people ended the wrong one. A session nobody
// named is called after its project (or its agent when it has none) plus a
// short topic from its first message — "myapp — fix the login page" — and a
// name already used by a live session gets "#2", "#3".

const topicWords = 6
const topicMax = 40

var topicNoise = regexp.MustCompile(`[\x00-\x1f]+`)

// Topic is the first few words of a prompt, short enough for a card title.
func Topic(prompt string) string {
	prompt = strings.TrimSpace(topicNoise.ReplaceAllString(prompt, " "))
	// The first line says what it is about; the rest is detail.
	if i := strings.IndexAny(prompt, "\n."); i > 0 {
		prompt = prompt[:i]
	}
	words := strings.Fields(prompt)
	if len(words) > topicWords {
		words = words[:topicWords]
	}
	topic := strings.Join(words, " ")
	if runes := []rune(topic); len(runes) > topicMax {
		topic = strings.TrimRightFunc(string(runes[:topicMax]), unicode.IsSpace) + "…"
	}
	return strings.TrimRight(topic, ",;:-—")
}

// NameBase is what a session is called before it has a topic: its project,
// or its agent.
func NameBase(projectName, agent string) string {
	if strings.TrimSpace(projectName) != "" {
		return strings.TrimSpace(projectName)
	}
	if agent == "" {
		return "claude"
	}
	return agent
}

// WithTopic is base plus a topic, or base alone when there is none.
func WithTopic(base, prompt string) string {
	if topic := Topic(prompt); topic != "" {
		return base + " — " + topic
	}
	return base
}

var numbered = regexp.MustCompile(` #\d+$`)

// IsDefaultName reports whether name is still the untouched default for
// base: base itself, or base with a "#n" suffix. A renamed session never is.
func IsDefaultName(name, base string) bool {
	return numbered.ReplaceAllString(name, "") == base
}

// UniqueName adds " #2", " #3"… to name while a live session other than
// self already has it.
func UniqueName(name string, live []*store.Session, self int64) string {
	taken := map[string]bool{}
	for _, s := range live {
		if s.ID != self && s.EndedAt == nil && s.ArchivedAt == nil {
			taken[s.Name] = true
		}
	}
	if !taken[name] {
		return name
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s #%d", name, n)
		if !taken[candidate] {
			return candidate
		}
	}
}
