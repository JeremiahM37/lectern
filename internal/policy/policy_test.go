package policy

import "testing"

func TestPatternForBashTakesFirstToken(t *testing.T) {
	got := PatternFor("Bash", map[string]any{"command": "pytest -x tests/"})
	if got != (Rule{Tool: "Bash", Prefix: "pytest"}) {
		t.Fatalf("got %+v", got)
	}
	if got := PatternFor("Bash", map[string]any{"command": ""}); got != (Rule{Tool: "Bash"}) {
		t.Fatalf("empty command: %+v", got)
	}
	if got := PatternFor("Edit", map[string]any{"file_path": "a.py"}); got != (Rule{Tool: "Edit"}) {
		t.Fatalf("non-bash: %+v", got)
	}
}

func TestMatchesBashPrefixOnly(t *testing.T) {
	p := Policy{Allow: []Rule{{Tool: "Bash", Prefix: "pytest"}}}
	if !Matches(p, "Bash", map[string]any{"command": "pytest -q"}) {
		t.Error("prefix should match")
	}
	if Matches(p, "Bash", map[string]any{"command": "rm -rf /"}) {
		t.Error("unrelated command must not match")
	}
	// substring is not a token match — this is the class of bug that once denied
	// `terraform version` because it contains "rm "
	if Matches(p, "Bash", map[string]any{"command": "pytest-cov run"}) {
		t.Error("substring must not count as a token match")
	}
	if Matches(p, "Edit", map[string]any{"file_path": "x"}) {
		t.Error("a Bash rule must not grant Edit")
	}
}

func TestMatchesWholeTool(t *testing.T) {
	p := Policy{Allow: []Rule{{Tool: "Edit"}}}
	if !Matches(p, "Edit", map[string]any{"file_path": "x"}) {
		t.Error("tool rule should match")
	}
	if Matches(p, "Write", map[string]any{}) {
		t.Error("Edit rule must not grant Write")
	}
}

func TestEmptyPrefixNeverMatches(t *testing.T) {
	p := Policy{Allow: []Rule{{Tool: "Bash", Prefix: ""}}}
	if Matches(p, "Bash", map[string]any{"command": "anything"}) {
		t.Error("an empty prefix must not become a wildcard")
	}
}

func TestAddRuleDedupes(t *testing.T) {
	p := AddRule(Policy{}, Rule{Tool: "Bash", Prefix: "ls"})
	p = AddRule(p, Rule{Tool: "Bash", Prefix: "ls"})
	if len(p.Allow) != 1 {
		t.Fatalf("expected one rule, got %+v", p.Allow)
	}
}

func TestGlobRules(t *testing.T) {
	p := Policy{Allow: []Rule{{Tool: "Bash", Glob: "git commit *"}}}
	if !Matches(p, "Bash", map[string]any{"command": "git commit -m 'x'"}) {
		t.Error("glob should match")
	}
	if Matches(p, "Bash", map[string]any{"command": "git push origin main"}) {
		t.Error("glob must not over-match")
	}
	if Matches(p, "Bash", map[string]any{"command": "rm -rf / && git commit -m x"}) {
		t.Error("a compound command must not slip through the glob")
	}
}

func TestGlobAndPrefixCombined(t *testing.T) {
	p := Policy{Allow: []Rule{
		{Tool: "Bash", Prefix: "pytest"}, {Tool: "Bash", Glob: "npm run *"}}}
	if !Matches(p, "Bash", map[string]any{"command": "pytest -q"}) {
		t.Error("prefix rule")
	}
	if !Matches(p, "Bash", map[string]any{"command": "npm run build"}) {
		t.Error("glob rule")
	}
	if Matches(p, "Bash", map[string]any{"command": "npm install left-pad"}) {
		t.Error("npm install must not match 'npm run *'")
	}
}

func TestParseTolerates(t *testing.T) {
	if Matches(Parse(""), "Bash", map[string]any{"command": "ls"}) {
		t.Error("no policy means no auto-approval")
	}
	if Matches(Parse("{not json"), "Bash", map[string]any{"command": "ls"}) {
		t.Error("a corrupt policy must fail closed, not open")
	}
}

func TestBroadenableForSession(t *testing.T) {
	cmd := func(c string) map[string]any { return map[string]any{"command": c} }
	for c, want := range map[string]bool{
		"npm test":                 true,
		"go test ./...":            true,
		"sudo systemctl restart x": false,
		"bash -c 'rm -rf /'":       false,
		"env FOO=1 make":           false,
		"xargs rm":                 false,
		"python3 -c 'print(1)'":    false,
		"./scripts/deploy.sh":      false,
		"FOO=bar make":             false,
		"":                         false,
	} {
		if got := BroadenableForSession("Bash", cmd(c)); got != want {
			t.Errorf("Bash %q: got %v, want %v", c, got, want)
		}
	}
	if !BroadenableForSession("Edit", map[string]any{"file_path": "x"}) {
		t.Error("non-Bash tools are scoped by tool name and always broadenable")
	}
}
