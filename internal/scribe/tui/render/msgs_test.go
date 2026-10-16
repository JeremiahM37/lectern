package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// The read tool's highlighting covers the extensions the spec names.
func TestSpecExtensionsHaveALanguage(t *testing.T) {
	for _, ext := range strings.Fields("ts tsx js jsx py sh go rs json css html yaml md java kt swift c cpp cs rb php lua zig sql graphql") {
		if LangFromPath("file."+ext) == "" {
			t.Errorf("extension %q has no highlight language", ext)
		}
	}
	if n := len(langAliases); n < 50 {
		t.Errorf("only %d language names mapped, want about 50 extensions", n)
	}
}

func TestNewLanguagesHighlightTheirKeywords(t *testing.T) {
	th := theme.Default()
	cases := map[string]string{
		"lua":     "local x = nil -- note",
		"zig":     "pub fn main() void { const x = 1; }",
		"swift":   "let name: String = \"a\" // note",
		"csharp":  "public class Foo { string s; }",
		"php":     "<?php function f($a) { return $a; }",
		"graphql": "query Q { user(id: 1) { name } } # note",
	}
	for lang, code := range cases {
		plain := ansi.Strip(Highlight(code, lang, th))
		if plain != code {
			t.Errorf("%s: highlighting changed the text: %q", lang, plain)
		}
		if Highlight(code, lang, th) == code {
			t.Errorf("%s: no colour applied", lang)
		}
	}
}

// Emphasis, links and code spans render as styled text, not raw markup.
func TestInlineMarkupRendersStyled(t *testing.T) {
	th := theme.Default()
	out := Markdown("A *soft* and **bold** with `code` and [docs](https://example.com/x).", 80, th, false)
	if strings.Contains(out, "*") || strings.Contains(out, "`") || strings.Contains(out, "](") {
		t.Fatalf("raw markup leaked: %q", ansi.Strip(out))
	}
	if !strings.Contains(out, "\x1b[3") {
		t.Errorf("italic not applied: %q", out)
	}
	if !strings.Contains(out, "\x1b[1") {
		t.Errorf("bold not applied: %q", out)
	}
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "docs (https://example.com/x)") {
		t.Errorf("link target not shown after its text: %q", plain)
	}
}
