package icons

import (
	"testing"
)

func TestIconSetSwitch(t *testing.T) {
	t.Cleanup(func() { SetNerd(false) })
	SetNerd(false)
	if got := Icon("pencil"); got != "✎" {
		t.Errorf("ASCII pencil = %q", got)
	}
	if got := Icon("ghost"); got != "◆" {
		t.Errorf("ASCII ghost = %q", got)
	}
	SetNerd(true)
	if got := Icon("pencil"); got != "\U0000F044" {
		t.Errorf("nerd pencil = %U", []rune(got))
	}
	if got := Icon("clear"); got != "\U000F01B4" {
		t.Errorf("nerd clear = %U, want the single codepoint U+F01B4", []rune(got))
	}
	if !NerdEnabled() {
		t.Error("NerdEnabled false after SetNerd(true)")
	}
}

func TestMissingNameReturnsName(t *testing.T) {
	t.Cleanup(func() { SetNerd(false) })
	for _, on := range []bool{false, true} {
		SetNerd(on)
		if got := Icon("definitely-not-an-icon"); got != "definitely-not-an-icon" {
			t.Errorf("nerd=%v missing icon = %q", on, got)
		}
	}
}

func TestEveryEntryHasASCIIFallback(t *testing.T) {
	for name, e := range table {
		if e.ascii == "" {
			t.Errorf("icon %q has no ASCII fallback", name)
		}
		if e.nerd == 0 {
			t.Errorf("icon %q has no nerd glyph", name)
		}
	}
}

func TestNerdFallsBackToASCIIWhenNoNerdGlyph(t *testing.T) {
	t.Cleanup(func() { SetNerd(false) })
	SetNerd(true)
	// every entry has a nerd glyph, so the fallback path is exercised with an
	// injected entry
	table["zz-test-ascii-only"] = entry{ascii: "Z"}
	t.Cleanup(func() { delete(table, "zz-test-ascii-only") })
	if got := Icon("zz-test-ascii-only"); got != "Z" {
		t.Errorf("fallback = %q, want Z", got)
	}
}

func TestToolIconAndProviderFallbacks(t *testing.T) {
	t.Cleanup(func() { SetNerd(false) })
	SetNerd(false)
	if got := ToolIcon("some_unknown_tool"); got != "⚒" {
		t.Errorf("unknown tool icon = %q, want the wrench", got)
	}
	if got := ProviderIcon("anthropic"); got != "A" {
		t.Errorf("anthropic ascii = %q", got)
	}
	if got := ProviderIcon("nope"); got != "●" {
		t.Errorf("unknown provider = %q, want ●", got)
	}
	SetNerd(true)
	if got := ProviderIcon("anthropic"); got == "A" || got == "" {
		t.Errorf("nerd anthropic = %q", got)
	}
	if got := ProviderIcon("groq"); got != "Q" {
		t.Errorf("groq has no nerd glyph, want ASCII fallback, got %q", got)
	}
}

func TestDetectNerdFromTerminalAndFontFile(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Setenv("TERM_PROGRAM", "ghostty")
	t.Setenv("TERMINAL_EMULATOR", "")
	if !DetectNerd() {
		t.Error("ghostty should detect nerd")
	}
	t.Setenv("TERM_PROGRAM", "")
	t.Setenv("TERMINAL_EMULATOR", "JetBrains-JediTerm")
	if !DetectNerd() {
		t.Error("JetBrains terminal should detect nerd")
	}
}
