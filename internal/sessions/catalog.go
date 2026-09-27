package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/pluginpkg"
)

// CatalogPreset is a one-click starting point for Settings → Agents: a Spec
// for a popular third-party coding CLI, plus the metadata a reviewer needs to
// trust it. Unlike Builtins(), lectern does not maintain these binaries —
// their flags are only as good as the day someone last checked them, and a
// preset that quietly goes stale is worse than one that says so.
//
// Every preset below says how it was checked in VerifiedBy (the installed
// CLI version whose --help it was read from, or "docs" when the CLI could not
// be installed without an account), cites the page(s) it was read from in
// Source, and lists in Unverified any Spec field that could not be confirmed
// — those fields hold a best guess and the UI presents them as unconfirmed.
// Re-check before trusting a preset whose VerifiedAt has aged.
type CatalogPreset struct {
	Spec
	// DisplayName is UI copy; it need not match the binary name.
	DisplayName string `json:"display_name"`
	// Vendor is who ships the CLI; the catalog search matches it.
	Vendor string `json:"vendor"`
	// Group sections the catalog list so ~30 entries stay scannable.
	Group string `json:"group"`
	// Icon is a monogram badge. Lectern ships no third-party logos: they are
	// trademarks, and fetching favicons would call out to a third party
	// every time Settings opens.
	Icon CatalogIcon `json:"icon"`
	// Homepage is the CLI's own documentation.
	Homepage string `json:"homepage"`
	// Description is one line of UI copy: what the CLI is, and anything a
	// reviewer should know before adding it — especially what it cannot do.
	Description string `json:"description"`
	// InstallHint is copy-pasteable install guidance shown when Installed()
	// is false for the launch host.
	InstallHint string `json:"install_hint"`
	// SessionsHint says where the CLI's own session ids are listed, for an
	// exact resume or fork. Lectern captures native ids itself only for the
	// built-in agents.
	SessionsHint string `json:"sessions_hint,omitempty"`
	// Source is where the flags below were read, so a stale entry can be
	// re-checked against the same page instead of re-discovered from scratch.
	Source string `json:"source"`
	// VerifiedBy is "<binary> <version>" when the flags were checked against
	// that installed CLI's --help (and, for ACP, a real initialize handshake),
	// or "docs" when only the vendor's documentation could be read.
	VerifiedBy string `json:"verified_by"`
	// Unverified lists Spec JSON field names (e.g. "model_flag",
	// "resume_args") that are a best guess rather than a confirmed fact as of
	// VerifiedAt. A field absent from this list, including one left at its
	// zero value, was confirmed to genuinely be unsupported — not merely
	// unresearched.
	Unverified []string `json:"unverified,omitempty"`
	// VerifiedAt is when the entry was last checked (YYYY-MM-DD).
	VerifiedAt string `json:"verified_at"`
}

// CatalogIcon is a one- or two-letter badge on a brand-neutral color.
type CatalogIcon struct {
	Glyph string `json:"glyph"`
	Color string `json:"color"`
}

// Catalog groups, in display order.
const (
	CatalogGroupPopular   = "Popular"
	CatalogGroupVendor    = "Vendor agents"
	CatalogGroupCommunity = "Open source & community"
	CatalogGroupAdapters  = "ACP adapters"
)

// CatalogGroups is the display order of the groups above.
func CatalogGroups() []string {
	return []string{CatalogGroupPopular, CatalogGroupVendor, CatalogGroupCommunity, CatalogGroupAdapters}
}

// Catalog lists the coding CLIs Settings → Agents offers as a one-click
// "Add from catalog" starting point, on top of the "Custom command…" escape
// hatch the editor already has. Adding one here never changes behavior for an
// operator who already saved a custom agent under the same name — ParseSpecs
// only ever reads what is in the "agents" setting; a preset is just what
// pre-fills the editor the first time.
//
// The entries are the bundled lectern.agent-catalog plugin
// (internal/pluginpkg/bundled/agent-catalog/lectern-plugin.yaml): the catalog
// is a plugin contribution like any other, so the plugin path is exercised by
// real content. Installed plugins add their own presets beside these
// (internal/plugins); this function is only the bundled set.
//
// A preset whose CLI speaks the Agent Client Protocol gets an ACP field in
// addition to its interactive launch fields: per docs/acp.md, ACP only ever
// replaces the Task backend for headless work (tasks/routines/best-of-N/evals)
// — interactive sessions stay tmux-based through Command/Args/PromptArg(s)
// exactly like every other agent. ACP is preferred over a Task command
// whenever both exist, because it carries every permission mode and project
// MCP servers itself.
func Catalog() []CatalogPreset {
	pkg, ok := pluginpkg.BundledPackage(CatalogPluginID)
	if !ok {
		return nil
	}
	out, err := CatalogFromRaw(pkg.Manifest.Contributes.Agents)
	if err != nil {
		return nil
	}
	return out
}

// CatalogPluginID is the bundled plugin that carries the catalog.
const CatalogPluginID = "lectern.agent-catalog"

// CatalogFromRaw decodes plugin agent contributions into presets. Unknown
// fields are errors, so a preset cannot carry a flag Lectern would ignore.
func CatalogFromRaw(raw []json.RawMessage) ([]CatalogPreset, error) {
	out := make([]CatalogPreset, 0, len(raw))
	for i, r := range raw {
		dec := json.NewDecoder(bytes.NewReader(r))
		dec.DisallowUnknownFields()
		var p CatalogPreset
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("agents[%d]: %w", i, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// FindCatalogPreset looks up one catalog entry by name for the "Add from
// catalog" flow.
func FindCatalogPreset(name string) (CatalogPreset, bool) {
	for _, p := range Catalog() {
		if p.Name == name {
			return p, true
		}
	}
	return CatalogPreset{}, false
}
