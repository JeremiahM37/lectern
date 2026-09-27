package trackers

import (
	"encoding/json"
	"strings"
	"testing"
)

const sampleMD = "## Plan\n\nRotate the **staging** key, see [runbook](https://x.test/rb) and `vault put`.\nThen ~~wait~~ *restart*.\n\n- rotate\n- update\n  - vault\n  - env\n- restart\n\n1. one\n2. two\n\n> keep the old key\n> for a day\n\n```sh\nmake rotate\n```\n\n---\n\nDone."

func TestMarkdownADFRoundTrip(t *testing.T) {
	doc := MarkdownToADF(sampleMD)
	raw, _ := json.Marshal(doc)
	if ADFLossy(raw) {
		t.Fatal("our own document reads as lossy")
	}
	back := richText(raw)
	want := "## Plan\n\nRotate the **staging** key, see [runbook](https://x.test/rb) and `vault put`.\nThen ~~wait~~ *restart*.\n\n- rotate\n- update\n  - vault\n  - env\n- restart\n\n1. one\n2. two\n\n> keep the old key\n> for a day\n\n```\nmake rotate\n```\n\n---\n\nDone."
	if back != want {
		t.Fatalf("round trip:\n%q\nwant\n%q", back, want)
	}
	// the ADF has the real structure, not text with asterisks in it
	s := string(raw)
	for _, frag := range []string{`"type":"strong"`, `"href":"https://x.test/rb"`, `"type":"orderedList"`, `"language":"sh"`, `"type":"rule"`, `"type":"blockquote"`} {
		if !strings.Contains(s, frag) {
			t.Errorf("ADF lacks %s: %s", frag, s)
		}
	}
	if empty, _ := json.Marshal(MarkdownToADF("")); !strings.Contains(string(empty), `"paragraph"`) {
		t.Fatalf("empty doc = %s", empty)
	}
}

func TestADFLossy(t *testing.T) {
	table := `{"type":"doc","version":1,"content":[{"type":"table","content":[]}]}`
	colour := `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"textColor","attrs":{"color":"#f00"}}]}]}]}`
	plain := `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"x","marks":[{"type":"strong"}]}]}]}`
	if !ADFLossy(json.RawMessage(table)) || !ADFLossy(json.RawMessage(colour)) || ADFLossy(json.RawMessage(plain)) {
		t.Fatal("lossy detection")
	}
	if ADFLossy(json.RawMessage(`"a v2 string"`)) {
		t.Fatal("a wiki string is not ADF")
	}
}

func TestMarkdownWikiBothWays(t *testing.T) {
	wiki := MarkdownToWiki(sampleMD)
	for _, frag := range []string{"h2. Plan", "*staging*", "[runbook|https://x.test/rb]", "{{vault put}}", "-wait-", "_restart_",
		"* rotate", "** vault", "# one", "{code:sh}\nmake rotate\n{code}", "{quote}", "----"} {
		if !strings.Contains(wiki, frag) {
			t.Errorf("wiki lacks %q:\n%s", frag, wiki)
		}
	}
	md := WikiToMarkdown("h3. Steps\n\nUse *bold* and _it_ and {{code}} and [site|https://s.test].\n* a\n** b\n# c\nbq. quoted\n{code:go}\nfmt.Println()\n{code}")
	for _, frag := range []string{"### Steps", "**bold**", "*it*", "`code`", "[site](https://s.test)", "- a", "  - b", "1. c", "> quoted", "```go\nfmt.Println()\n```"} {
		if !strings.Contains(md, frag) {
			t.Errorf("markdown lacks %q:\n%s", frag, md)
		}
	}
	// a wiki round trip keeps the words and the structure
	again := MarkdownToWiki(WikiToMarkdown(wiki))
	if !strings.Contains(again, "** vault") || !strings.Contains(again, "*staging*") {
		t.Fatalf("wiki round trip:\n%s", again)
	}
}

func TestInlineDoesNotEatPlainText(t *testing.T) {
	spans := parseInline("snake_case_name and 2 * 3 * 4", nil)
	if len(spans) != 1 || spans[0].text != "snake_case_name and 2 * 3 * 4" {
		t.Fatalf("spans = %+v", spans)
	}
}
