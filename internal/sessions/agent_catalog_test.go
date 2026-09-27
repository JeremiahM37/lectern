package sessions

import (
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// TestCatalogPresetsAreWellFormed guards the one property every preset must
// have regardless of how well its CLI's flags were verified: a usable name,
// a command (or an explicit admission in Unverified that even the command is
// a guess), a source citation, and a verification date — so a reviewer can
// tell a researched preset from a stub.
func TestCatalogPresetsAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Catalog() {
		if strings.TrimSpace(p.Name) == "" {
			t.Fatalf("preset %q has no Spec.Name", p.DisplayName)
		}
		if seen[p.Name] {
			t.Errorf("duplicate catalog preset name %q", p.Name)
		}
		seen[p.Name] = true
		if strings.TrimSpace(p.DisplayName) == "" {
			t.Errorf("%s: no display name", p.Name)
		}
		if strings.TrimSpace(p.Source) == "" {
			t.Errorf("%s: no source citation — every preset must say where its flags were read", p.Name)
		}
		if strings.TrimSpace(p.VerifiedAt) == "" {
			t.Errorf("%s: no verified_at date", p.Name)
		}
		commandUnverified := false
		for _, field := range p.Unverified {
			if field == "command" {
				commandUnverified = true
			}
		}
		if strings.TrimSpace(p.Command) == "" && !commandUnverified {
			t.Errorf("%s: empty command must be flagged in Unverified", p.Name)
		}
		if p.Task != nil && p.ACP != nil {
			t.Errorf("%s: task and acp are mutually exclusive non-interactive backends", p.Name)
		}
		// Every preset must survive the exact JSON round trip Settings →
		// Agents and `lectern agent save` both use — a preset that cannot
		// even marshal is useless as a one-click starting point.
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("%s: does not marshal: %v", p.Name, err)
		}
		var back CatalogPreset
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: does not round-trip: %v", p.Name, err)
		}
	}
	if len(seen) < 12 {
		t.Errorf("expected at least a dozen catalog presets, got %d", len(seen))
	}
}

// TestCatalogPresetSchema is the per-entry contract the searchable, grouped
// catalog UI relies on, and proves every preset would pass the same
// validation `PUT /api/agents` applies when the operator saves it.
func TestCatalogPresetSchema(t *testing.T) {
	groups := map[string]bool{}
	for _, g := range CatalogGroups() {
		groups[g] = true
	}
	specFields := map[string]bool{"command": true, "install_hint": true}
	for i := 0; i < reflect.TypeOf(Spec{}).NumField(); i++ {
		tag := strings.Split(reflect.TypeOf(Spec{}).Field(i).Tag.Get("json"), ",")[0]
		specFields[tag] = true
	}
	color := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	version := regexp.MustCompile(`^(docs|\S+ v?\d[\w.\-]*( \(.+\))?)$`)
	for _, p := range Catalog() {
		if !groups[p.Group] {
			t.Errorf("%s: unknown group %q", p.Name, p.Group)
		}
		if strings.TrimSpace(p.Vendor) == "" || strings.TrimSpace(p.Description) == "" ||
			strings.TrimSpace(p.InstallHint) == "" {
			t.Errorf("%s: vendor, description and install hint are required", p.Name)
		}
		if !strings.HasPrefix(p.Homepage, "https://") {
			t.Errorf("%s: homepage must be an https URL, got %q", p.Name, p.Homepage)
		}
		if n := len([]rune(p.Icon.Glyph)); n < 1 || n > 2 || !color.MatchString(p.Icon.Color) {
			t.Errorf("%s: icon needs a 1-2 letter glyph and a #rrggbb color, got %+v", p.Name, p.Icon)
		}
		if !version.MatchString(p.VerifiedBy) {
			t.Errorf("%s: verified_by must be \"docs\" or \"<binary> <version>\", got %q", p.Name, p.VerifiedBy)
		}
		if p.VerifiedBy == "docs" && !strings.Contains(p.Source, "https://") && p.Group != CatalogGroupAdapters {
			t.Errorf("%s: a docs-verified preset must cite a documentation URL", p.Name)
		}
		for _, field := range p.Unverified {
			if !specFields[field] {
				t.Errorf("%s: unverified field %q is not a Spec field", p.Name, field)
			}
		}
		raw, _ := json.Marshal([]Spec{p.Spec})
		if err := ValidateSpecs(string(raw)); err != nil {
			t.Errorf("%s: would be rejected on save: %v", p.Name, err)
		}
		if p.Task == nil && p.ACP == nil && p.Capabilities()[CapTask].Available {
			t.Errorf("%s: claims background tasks without a backend", p.Name)
		}
	}
}

// TestCatalogCoversOrcasAgents keeps the catalog at parity with the agents
// Orca (github.com/stablyai/orca) supports, plus Aider and Gemini which it
// does not. Claude Code and Codex are Lectern built-ins.
func TestCatalogCoversOrcasAgents(t *testing.T) {
	for _, name := range []string{
		"grok", "cursor-agent", "copilot", "muse", "zcode", "opencode", "mimo", "amp",
		"openclaude", "antigravity", "pi", "omp", "hermes", "devin", "goose", "auggie",
		"autohand", "crush", "cline", "codebuff", "command-code", "cn", "droid", "kilo",
		"kimi", "kiro", "vibe", "qwen", "rovodev", "aider", "gemini-acp",
	} {
		if _, ok := FindCatalogPreset(name); !ok {
			t.Errorf("catalog is missing %s", name)
		}
	}
}

func TestPromptArgsAndYoloEnvValidation(t *testing.T) {
	for raw, wantErr := range map[string]bool{
		`[{"name":"a","command":"a","prompt_args":["--prompt","{prompt}"]}]`: false,
		`[{"name":"a","command":"a","prompt_args":["--prompt"]}]`:            true,
		`[{"name":"a","command":"a","prompt_args":["--prompt={prompt}"]}]`:   true,
		`[{"name":"a","command":"a","yolo_env":{"GOOSE_MODE":"auto"}}]`:      false,
		`[{"name":"a","command":"a","yolo_env":{"1BAD":"x"}}]`:               true,
	} {
		if err := ValidateSpecs(raw); (err != nil) != wantErr {
			t.Errorf("%s: err=%v, want error %v", raw, err, wantErr)
		}
	}
	yolo := Spec{Name: "g", Command: "g", YoloEnv: map[string]string{"GOOSE_MODE": "auto"}}
	if !yolo.Capabilities()[CapYolo].Available {
		t.Error("an environment-only yolo switch still offers yolo")
	}
}

// TestCatalogPresetsThatClaimACPAreNotAlsoTask double-checks the ACP/Task
// exclusivity ValidateSpecs enforces at save time also holds for every
// preset before it ever reaches an operator.
func TestCatalogPresetNamesDoNotCollideWithBuiltins(t *testing.T) {
	builtins := map[string]bool{}
	for _, b := range Builtins() {
		builtins[b.Name] = true
	}
	for _, p := range Catalog() {
		if builtins[p.Name] {
			t.Errorf("catalog preset %q collides with a built-in agent name", p.Name)
		}
	}
}

func TestFindCatalogPreset(t *testing.T) {
	if _, ok := FindCatalogPreset("does-not-exist"); ok {
		t.Fatal("found a preset that should not exist")
	}
	p, ok := FindCatalogPreset("aider")
	if !ok || p.Command != "aider" {
		t.Fatalf("got %+v, %v", p, ok)
	}
}

// TestOpenCodeCatalogPresetPrefersACP locks in the "prefer ACP when the CLI
// documents it" rule from the catalog's own doc comment: opencode's own docs
// confirm `opencode acp` speaks ACP over stdio, so the preset must carry an
// ACP backend rather than leaving background work to a guessed Task
// invocation.
func TestOpenCodeCatalogPresetPrefersACP(t *testing.T) {
	p, ok := FindCatalogPreset("opencode")
	if !ok {
		t.Fatal("opencode preset missing")
	}
	if p.ACP == nil {
		t.Fatal("opencode documents ACP support and should prefer it over a guessed Task backend")
	}
	if p.Task != nil {
		t.Fatal("ACP and Task are mutually exclusive")
	}
	// Interactive launch is unaffected by ACP — docs/acp.md is explicit that
	// ACP only ever replaces the Task backend for headless work.
	if p.Command == "" || p.ModelFlag == "" {
		t.Fatal("opencode's own interactive launch fields must still be populated alongside ACP")
	}
}
