package config

import "testing"

func TestResolveRouterRuleKeywordAndGlob(t *testing.T) {
	rules := []RouterRule{
		{Match: "refactor", ModelID: "anthropic/haiku"},
		{Match: "test*", ModelID: "openai/mini"},
	}
	if id, ok := ResolveRouterRule(rules, "Please REFACTOR the parser"); !ok || id != "anthropic/haiku" {
		t.Fatalf("keyword match = %q, %v", id, ok)
	}
	if id, ok := ResolveRouterRule(rules, "write tests for it"); !ok || id != "openai/mini" {
		t.Fatalf("glob match = %q, %v", id, ok)
	}
	if _, ok := ResolveRouterRule(rules, "rename a variable"); ok {
		t.Fatal("no rule should match, so the slot model is kept")
	}
}

func TestResolveRouterRulePriorityThenOrder(t *testing.T) {
	rules := []RouterRule{
		{Match: "api", ModelID: "a/low", Priority: 1},
		{Match: "api", ModelID: "a/high", Priority: 5},
		{Match: "api", ModelID: "a/tie", Priority: 5},
	}
	if id, _ := ResolveRouterRule(rules, "api change"); id != "a/high" {
		t.Fatalf("highest priority should win, got %q", id)
	}
}

func TestResolveRouterRuleCatchAllAndInvalid(t *testing.T) {
	rules := []RouterRule{
		{ModelID: "  "},
		{ModelID: "x/default"},
	}
	if id, ok := ResolveRouterRule(rules, "anything"); !ok || id != "x/default" {
		t.Fatalf("catch-all = %q, %v; an empty model id must be skipped", id, ok)
	}
	if _, ok := ResolveRouterRule(nil, "anything"); ok {
		t.Fatal("no rules must resolve to nothing")
	}
}

func TestMergeRouterRulesProjectReplaces(t *testing.T) {
	base := Config{RouterRules: []RouterRule{{Match: "a", ModelID: "p/a"}}}
	if got := Merge(base, Config{}).RouterRules; len(got) != 1 {
		t.Fatalf("unset project rules must keep the global ones, got %v", got)
	}
	over := Merge(base, Config{RouterRules: []RouterRule{{Match: "b", ModelID: "p/b"}}}).RouterRules
	if len(over) != 1 || over[0].ModelID != "p/b" {
		t.Fatalf("project rules must replace the global list, got %v", over)
	}
}
