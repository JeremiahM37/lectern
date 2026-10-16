package exec

import (
	"encoding/json"
	"testing"
)

func TestMutatesRepo(t *testing.T) {
	cases := []struct {
		tool, args string
		want       bool
	}{
		{"git", `{"action":"commit"}`, true},
		{"git", `{"action":"cherry-pick"}`, true},
		{"git", `{"action":"branch","sub_action":"switch"}`, true},
		{"git", `{"action":"branch","sub_action":"list"}`, false},
		{"git", `{"action":"status"}`, false},
		{"shell", `{"command":"git commit -m x"}`, true},
		{"shell", `{"command":"ls -la"}`, false},
		{"edit_file", `{"path":"a.go"}`, false},
	}
	for _, c := range cases {
		if got := MutatesRepo(c.tool, json.RawMessage(c.args)); got != c.want {
			t.Errorf("MutatesRepo(%s, %s) = %v, want %v", c.tool, c.args, got, c.want)
		}
	}
}
