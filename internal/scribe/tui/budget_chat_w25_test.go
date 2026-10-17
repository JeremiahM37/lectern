package tui

import (
	"strings"
	"testing"
)

type chatBudgetSetter struct {
	*FakeBackend
	cb  ChatBudget
	set int
}

func (f *chatBudgetSetter) ChatBudget() ChatBudget { return f.cb }
func (f *chatBudgetSetter) SetChatBudget(n int) error {
	f.set = n
	f.cb.Limit = n
	return nil
}

func TestBudgetChatShowsAndSetsThePerChatBudget(t *testing.T) {
	m, fb := newTestModel(t, 120, 40)
	stub := &chatBudgetSetter{FakeBackend: fb, cb: ChatBudget{Today: 1200}}
	m.cur().be = stub
	if f := flashOf(t, budgetCommand(m, "")); !strings.Contains(f.text, "this chat 1.2k") {
		t.Fatalf("/budget did not name this chat's use: %q", f.text)
	}
	if f := flashOf(t, budgetCommand(m, "chat 500k")); f.kind != "ok" || stub.set != 500000 {
		t.Fatalf("/budget chat 500k: flash %q, set %d", f.text, stub.set)
	}
	if f := flashOf(t, budgetCommand(m, "chat off")); f.kind != "ok" || stub.set != 0 {
		t.Fatalf("/budget chat off: flash %q, set %d", f.text, stub.set)
	}
}
