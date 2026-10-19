package agent

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestHasCompactionPart(t *testing.T) {
	if hasCompactionPart(llm.Message{Parts: []llm.Part{{Type: llm.PartText, Text: "hi"}}}) {
		t.Fatal("plain text must not count as compaction")
	}
	if !hasCompactionPart(llm.Message{Parts: []llm.Part{{Type: llm.PartCompaction, Text: "summary"}}}) {
		t.Fatal("a compaction block must be found")
	}
}
