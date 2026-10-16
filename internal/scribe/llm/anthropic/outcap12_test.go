package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// TestDefaultOutputCapClampsKnownModels checks spec 2.1: an unset cap is
// 64000, a known model with a lower documented limit gets that limit, and an
// explicit cap above a known model's limit is lowered to it.
func TestDefaultOutputCapClampsKnownModels(t *testing.T) {
	cases := []struct {
		model string
		req   int
		want  int
	}{
		{"claude-haiku-4-5", 0, 64000},
		{"claude-opus-4-1", 0, 32000},
		{"claude-opus-4-1-20250805", 0, 32000},
		{"some-future-model", 0, 64000},
		{"claude-opus-4-1", 50000, 32000},
		{"claude-haiku-4-5", 1000, 1000},
	}
	for _, c := range cases {
		b, err := buildBody(llm.Request{Model: c.model, MaxTokens: c.req, Messages: []llm.Message{llm.UserText("x")}})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if got.MaxTokens != c.want {
			t.Errorf("%s req=%d: max_tokens %d, want %d", c.model, c.req, got.MaxTokens, c.want)
		}
	}
}
