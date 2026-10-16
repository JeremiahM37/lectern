package symedit

import (
	"strings"
	"testing"
)

func TestAstEditRefusesOversizedFile(t *testing.T) {
	f := newRefactorFixture(t)
	big := "def f():\n    return 1\n" + strings.Repeat("# filler line to pass one megabyte\n", 40000)
	f.write("big.py", big)
	res := f.run("ast_edit", map[string]any{"path": "big.py", "action": "replace_body", "symbol": "f", "new_code": "    return 2"})
	if !res.IsError || !strings.Contains(res.Output, "over 1 MB") {
		t.Fatalf("expected the size refusal, got %q", res.Output)
	}
}

func TestMissingSymbolListsDefinitions(t *testing.T) {
	f := newRefactorFixture(t)
	f.write("m.py", "def alpha():\n    return 1\n\n\ndef beta():\n    return 2\n")
	res := f.run("ast_edit", map[string]any{"path": "m.py", "action": "delete", "symbol": "gamma"})
	if !res.IsError || !strings.Contains(res.Output, "Defined there: alpha, beta") {
		t.Fatalf("expected the available-symbols list, got %q", res.Output)
	}
}
