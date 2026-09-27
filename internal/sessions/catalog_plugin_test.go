package sessions

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// The catalog moved from a Go literal into the bundled lectern.agent-catalog
// plugin. testdata/catalog-golden.json is the literal's output, captured
// before the move: the plugin must produce exactly the same presets, in the
// same order, field for field.
func TestBundledCatalogPluginMatchesTheGoLiteralItReplaced(t *testing.T) {
	want, err := os.ReadFile("testdata/catalog-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	cat := Catalog()
	if len(cat) == 0 {
		t.Fatal("the bundled catalog plugin produced no presets")
	}
	got, _ := json.MarshalIndent(cat, "", "  ")
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Fatalf("catalog drifted from the golden copy:\n%s", firstDiff(string(got), string(want)))
	}
}

// OpenClaude's trust command was derived from claudeTrust in Go; in the
// manifest it is a literal. Keep them from drifting apart.
func TestOpenClaudeTrustStillMatchesClaudeTrust(t *testing.T) {
	p, ok := FindCatalogPreset("openclaude")
	if !ok {
		t.Fatal("no openclaude preset")
	}
	want := strings.NewReplacer("CLAUDE_CONFIG_DIR", "OPENCLAUDE_CONFIG_DIR", ".claude.json", ".openclaude.json").Replace(claudeTrust)
	if p.TrustCommand != want {
		t.Fatal("openclaude trust_command no longer matches claudeTrust; regenerate it in the agent-catalog manifest")
	}
}

func TestCatalogFromRawRefusesUnknownFields(t *testing.T) {
	if _, err := CatalogFromRaw([]json.RawMessage{json.RawMessage(`{"name":"x","command":"x","yolo":true}`)}); err == nil {
		t.Fatal("an unknown preset field was accepted")
	}
}

func firstDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return fmt.Sprintf("line %d: got %s\nwant %s", i+1, al[i], bl[i])
		}
	}
	return "lengths differ"
}
