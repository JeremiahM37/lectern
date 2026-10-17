package tui

import (
	"strings"
	"testing"
)

// chatBudgetFake is the fake backend plus a per-chat budget, so the /context
// row can be tested with a known spend and limit.
type chatBudgetFake struct {
	Backend
	cb ChatBudget
}

func (f *chatBudgetFake) ChatBudget() ChatBudget { return f.cb }

// SetChatBudget makes the fake a ChatBudgeter; the setter itself is tested in app.
func (f *chatBudgetFake) SetChatBudget(int) error { return nil }

func contextWithChatBudget(t *testing.T, cb ChatBudget) string {
	t.Helper()
	m, fb := newTestModel(t, 120, 40)
	m.cur().be = &chatBudgetFake{Backend: fb, cb: cb}
	return strings.Join(contextLines(m.cur()), "\n")
}

func TestContextShowsThisChatsShareOfTodayAgainstItsLimit(t *testing.T) {
	lines := contextWithChatBudget(t, ChatBudget{Today: 50000, Limit: 200000})
	if !strings.Contains(lines, "This chat      50k of 200k tokens today (25%)") {
		t.Fatalf("Today section lacks the per-chat row:\n%s", lines)
	}
	// The global row is still there, and comes first.
	global := strings.Index(lines, "daily budget")
	chat := strings.Index(lines, "This chat")
	if global < 0 || chat < global {
		t.Fatalf("the per-chat row should follow the global budget row:\n%s", lines)
	}
}

func TestContextPerChatRowWithoutLimitShowsSpendAndKey(t *testing.T) {
	lines := contextWithChatBudget(t, ChatBudget{Today: 50000})
	for _, want := range []string{"This chat", "50k tokens today", "no per-chat budget; set chat_daily_token_budget"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("per-chat row lacks %q:\n%s", want, lines)
		}
	}
}

func TestContextOmitsPerChatRowWhenBackendHasNoChatBudget(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	lines := strings.Join(contextLines(m.cur()), "\n")
	if strings.Contains(lines, "This chat") {
		t.Fatalf("a backend without per-chat budgets showed a row:\n%s", lines)
	}
}

func TestChatBudgetOfFindsOnlyBackendsThatExposeIt(t *testing.T) {
	fb := NewFakeBackend()
	if _, ok := chatBudgetOf(fb); ok {
		t.Fatal("the fake backend has no per-chat budget but was reported as having one")
	}
	cb, ok := chatBudgetOf(&chatBudgetFake{Backend: fb, cb: ChatBudget{Today: 1, Limit: 2}})
	if !ok || cb != (ChatBudget{Today: 1, Limit: 2}) {
		t.Fatalf("chatBudgetOf = %+v, %v", cb, ok)
	}
}

func TestChatBudgetLineRoundsPercentLikeTheGlobalRow(t *testing.T) {
	got := chatBudgetLine(ChatBudget{Today: 1, Limit: 3})
	if !strings.Contains(got, "(33%)") {
		t.Fatalf("line = %q, want a whole-number percent", got)
	}
	got = chatBudgetLine(ChatBudget{Today: 300, Limit: 200})
	if !strings.Contains(got, "(150%)") {
		t.Fatalf("an overspent chat should show its real percent, got %q", got)
	}
}
