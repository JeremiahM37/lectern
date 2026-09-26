package sessions

import (
	"encoding/json"
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
