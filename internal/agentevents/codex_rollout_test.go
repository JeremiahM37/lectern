package agentevents

import (
	"encoding/base64"
	"strings"
	"testing"
)

// codexRolloutFixture is a sanitised excerpt of a real codex 0.155.1 rollout
// file (~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl on this machine — see
// the workstream report). Prompt/instruction text and the session's real cwd
// are stripped; the structural shape (session_meta, turn_context, and the
// event_msg-wrapped token_count records with total_token_usage/
// model_context_window) is byte-for-byte what codex actually writes.
const codexRolloutFixture = `{"timestamp":"2026-09-22T21:23:42.875Z","type":"session_meta","payload":{"session_id":"01a0cb65-56af-7240-91a2-68f1e8df4433","cwd":"/home/admin/example","cli_version":"0.155.1"}}
{"timestamp":"2026-09-22T21:23:43.227Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-6-astra","cwd":"/home/admin/example"}}
{"timestamp":"2026-09-22T21:23:49.482Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":35340,"cached_input_tokens":12032,"output_tokens":136,"total_tokens":35476},"last_token_usage":{"input_tokens":35340,"output_tokens":136,"total_tokens":35476},"model_context_window":258400},"rate_limits":{"primary":{"used_percent":10.0}}}}
{"timestamp":"2026-09-22T21:23:57.763Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80989,"cached_input_tokens":47232,"output_tokens":346,"total_tokens":81335},"last_token_usage":{"input_tokens":45649,"output_tokens":210,"total_tokens":45859},"model_context_window":258400},"rate_limits":{"primary":{"used_percent":10.0}}}}
`

func TestParseCodexRolloutUsageReadsLatestTokenCount(t *testing.T) {
	usage, ok := ParseCodexRolloutUsage([]byte(codexRolloutFixture))
	if !ok {
		t.Fatal("expected a usage reading")
	}
	if usage.Model != "gpt-6-astra" {
		t.Errorf("model = %q", usage.Model)
	}
	// Must take the LAST token_count record (81335 total), not the first.
	if usage.ContextTokens != 81335 {
		t.Errorf("context_tokens = %d, want 81335", usage.ContextTokens)
	}
	if usage.ContextSize != 258400 {
		t.Errorf("context_size = %d, want 258400", usage.ContextSize)
	}
	if usage.InputTokens != 80989 || usage.OutputTokens != 346 {
		t.Errorf("input/output tokens = %d/%d", usage.InputTokens, usage.OutputTokens)
	}
	wantPct := 81335 * 100 / 258400
	if usage.ContextPct != wantPct {
		t.Errorf("context_pct = %d, want %d", usage.ContextPct, wantPct)
	}
}

func TestParseCodexRolloutUsageNoTokenCountIsAbsent(t *testing.T) {
	_, ok := ParseCodexRolloutUsage([]byte(`{"type":"session_meta","payload":{}}` + "\n"))
	if ok {
		t.Fatal("a rollout with no token_count record must report absent, not fabricate one")
	}
}

func TestParseCodexRolloutUsageTruncatedLeadingLineIsSkipped(t *testing.T) {
	// A tail read (see CodexRolloutTailCommand's `tail -c`) can start
	// mid-line; the truncated first line must not abort the whole parse.
	truncated := `{"timestamp":"2026-09` + "\n" + codexRolloutFixture
	usage, ok := ParseCodexRolloutUsage([]byte(truncated))
	if !ok || usage.ContextTokens != 81335 {
		t.Fatalf("truncated leading line broke the parse: %v ok=%v", usage, ok)
	}
}

func TestValidCodexThreadID(t *testing.T) {
	if !ValidCodexThreadID("01a0925a-4173-7b43-addd-68891f230769") {
		t.Error("a real codex thread id must validate")
	}
	for _, bad := range []string{"", "; rm -rf /", "$(whoami)", "../../etc/passwd", "a b"} {
		if ValidCodexThreadID(bad) {
			t.Errorf("must reject %q", bad)
		}
	}
}

func TestCodexRolloutTailCommandQuotesEveryID(t *testing.T) {
	// Every id, valid-looking or not, must survive as shellq.Quote would
	// render it — this only guards the command builder itself; ingest.go's
	// ValidCodexThreadID is what keeps a hostile id out of the DB in the
	// first place (see TestValidCodexThreadID).
	cmd := CodexRolloutTailCommand([]string{"01a0925a-4173-7b43-addd-68891f230769", "'; rm -rf /"})
	// shellq neutralises the hostile id by single-quoting it and escaping any
	// embedded quote; the raw id must never appear unescaped outside quotes.
	if !strings.Contains(cmd, `'\''; rm -rf /'`) {
		t.Errorf("hostile id was not neutralised: %s", cmd)
	}
	if !strings.Contains(cmd, "01a0925a-4173-7b43-addd-68891f230769") {
		t.Errorf("a well-formed id should render unquoted: %s", cmd)
	}
}

func TestCodexRolloutSnapshotRoundtrip(t *testing.T) {
	ids := []string{"aaaa", "bbbb"}
	// Simulate what CodexRolloutTailCommand's shell side would print.
	output := "YWFhYQ==\tok\t" + base64.StdEncoding.EncodeToString([]byte(codexRolloutFixture)) + "\n" +
		"YmJiYg==\tmissing\t\n" +
		CodexRolloutEnd + "\n"
	tails, ok := ParseCodexRolloutSnapshot(output, ids)
	if !ok {
		t.Fatal("expected a complete snapshot")
	}
	if string(tails["aaaa"]) != codexRolloutFixture {
		t.Errorf("aaaa tail mismatch: %q", tails["aaaa"])
	}
	if tails["bbbb"] != nil {
		t.Errorf("missing thread should decode to a nil tail: %v", tails["bbbb"])
	}
}

func TestCodexRolloutSnapshotRejectsIncomplete(t *testing.T) {
	if _, ok := ParseCodexRolloutSnapshot("not the right shape\n", []string{"aaaa"}); ok {
		t.Fatal("a malformed snapshot must not be trusted")
	}
}
