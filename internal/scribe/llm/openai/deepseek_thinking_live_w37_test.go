package openai

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// deepSeekLiveKey returns the DeepSeek key from the environment, or from the
// hindsight baseline env file, read in-process only. The value is never logged.
func deepSeekLiveKey() string {
	if k := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); k != "" {
		return k
	}
	b, err := os.ReadFile("/mnt/bulk/grimoire-parity/hindsight-baseline/deepseek.env")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "DEEPSEEK_API_KEY="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// TestLiveW37DeepSeekThinkingToggle sends two tiny requests to the real
// DeepSeek API, one with thinking on and one with it off, and checks that
// neither is rejected and that reasoning text appears only when enabled.
// Guarded by SCRIBE_LIVE_DEEPSEEK=1. Cost: two requests, max_tokens 16.
func TestLiveW37DeepSeekThinkingToggle(t *testing.T) {
	if os.Getenv("SCRIBE_LIVE_DEEPSEEK") != "1" {
		t.Skip("set SCRIBE_LIVE_DEEPSEEK=1 to call the real DeepSeek API")
	}
	key := deepSeekLiveKey()
	if key == "" {
		t.Skip("no DeepSeek key: DEEPSEEK_API_KEY unset and the hindsight env file has none")
	}
	const model = "deepseek-v4-flash"
	p := New(Options{ID: "deepseek", BaseURL: "https://api.deepseek.com/v1", APIKey: key})

	run := func(switchVal string) (text, reason string, errs []error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		ch, err := p.Stream(ctx, llm.Request{
			Model:            model,
			Messages:         []llm.Message{llm.UserText("Say ok")},
			MaxTokens:        16,
			DeepSeekThinking: switchVal,
		})
		if err != nil {
			t.Fatalf("thinking=%q: request rejected before streaming: %v", switchVal, err)
		}
		var tb, rb strings.Builder
		for ev := range ch {
			switch ev.Type {
			case llm.EvText:
				tb.WriteString(ev.Text)
			case llm.EvReasoning:
				rb.WriteString(ev.Text)
			case llm.EvError:
				errs = append(errs, ev.Err)
			}
		}
		return tb.String(), rb.String(), errs
	}

	onText, onReason, onErrs := run("on")
	offText, offReason, offErrs := run("off")

	if len(onErrs) > 0 {
		t.Fatalf("thinking on: API error: %v", onErrs[0])
	}
	if len(offErrs) > 0 {
		t.Fatalf("thinking off: API error: %v", offErrs[0])
	}
	if onReason == "" {
		t.Errorf("thinking on: no reasoning content returned (text=%q)", onText)
	}
	if offReason != "" {
		t.Errorf("thinking off: reasoning content leaked (%d chars)", len(offReason))
	}
	t.Logf("thinking on: text=%d chars, reasoning=%d chars", len(onText), len(onReason))
	t.Logf("thinking off: text=%d chars, reasoning=%d chars", len(offText), len(offReason))
}
