package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
)

// Post-turn memory extraction (spec 06 §10) is opt-in and off by default:
// set SCRIBE_MEMORY_EXTRACT=1. After a turn that finished cleanly, the model
// reads the exchange once more and saves the durable facts it finds, through
// the same store and dedupe the memory tool uses. It runs in the background,
// so the turn does not wait for it.

const extractTimeout = 60 * time.Second

// extractPrompt asks for durable memories only. The answer must be a JSON
// array, so parsing needs no guessing.
const extractPrompt = `You extract durable memories from one exchange between a person and a coding agent.
Save only what will still matter in a later session: decisions with their rationale, conventions, gotchas with the symptom and the fix, and stated preferences.
Skip restated code, temporary state, speculation and anything the person did not say or agree to.
Reply with only a JSON array of objects {"category": "decision|convention|bug|preference|fact|todo", "title": "short headline", "body": "the detail"}. Reply [] when nothing qualifies.`

// extractOn reports whether post-turn extraction is switched on.
func extractOn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("SCRIBE_MEMORY_EXTRACT")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// extractedMemory is one item of the model's answer.
type extractedMemory struct {
	Category string `json:"category"`
	Title    string `json:"title"`
	Body     string `json:"body"`
}

// parseExtracted reads the model's answer. It tolerates a code fence around
// the array, and returns nothing when the answer is not an array.
func parseExtracted(s string) []extractedMemory {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	start, end := strings.Index(s, "["), strings.LastIndex(s, "]")
	if start < 0 || end < start {
		return nil
	}
	var out []extractedMemory
	if json.Unmarshal([]byte(s[start:end+1]), &out) != nil {
		return nil
	}
	return out
}

// extractMemories runs the extraction for the turn that just finished. It is
// a no-op unless the feature is on, the turn finished, and memory is open.
func (a *App) extractMemories(prompt string, stats agent.TurnStats) {
	if !extractOn() || a.Memory == nil || a.opts.Ephemeral || stats.Error != "" || stats.Aborted {
		return
	}
	answer := lastAssistantText(a.Agent.Messages())
	if strings.TrimSpace(prompt) == "" || answer == "" {
		return
	}
	model := a.Agent.CurrentModel()
	if model.Provider == nil {
		return
	}
	stores := a.Memory
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
		defer cancel()
		req := llm.Request{
			Model:    model.ID,
			System:   extractPrompt,
			Messages: []llm.Message{llm.UserText("Person:\n" + prompt + "\n\nAgent:\n" + answer)},
		}
		text, err := agent.Collect(ctx, model.Provider, req)
		if err != nil {
			return
		}
		db := stores.Project
		if db == nil {
			db = stores.Global
		}
		if db == nil || stores.Policy.WriteScope == "none" {
			return
		}
		for _, m := range parseExtracted(text) {
			if strings.TrimSpace(m.Title) == "" {
				continue
			}
			_, _, _ = db.Add(memory.Record{Category: m.Category, Title: m.Title, Body: m.Body, Source: "agent"})
		}
	}()
}

// lastAssistantText is the text of the newest assistant message, or "".
func lastAssistantText(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleAssistant {
			return strings.TrimSpace(msgs[i].Text())
		}
	}
	return ""
}
