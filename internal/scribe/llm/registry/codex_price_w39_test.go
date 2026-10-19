package registry

import "testing"

// Codex models are paid for by the ChatGPT subscription: no per-token price,
// and not shown as an estimate, but the OpenAI model's window is kept.
func TestCodexModelsCostNothing(t *testing.T) {
	for _, m := range []string{"gpt-5.6-sol", "gpt-6.1-sol", "some-new-model"} {
		info := lookupModel("codex", m)
		if info.InputPerMTok != 0 || info.OutputPerMTok != 0 || info.CacheReadPerMTok != 0 || info.CacheWritePerMTok != 0 {
			t.Errorf("%s: priced %+v", m, info)
		}
		if info.Estimated {
			t.Errorf("%s: marked as an estimate", m)
		}
		if info.ContextWindow == 0 {
			t.Errorf("%s: no context window", m)
		}
	}
}
