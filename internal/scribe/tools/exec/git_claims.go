package exec

import (
	"encoding/json"
	"strings"
)

// gitRepoActions are the git tool actions that change the repository. The
// same set the dispatch guard refuses inside one tab; the cross-tab guard in
// app/claims.go uses it too. "branch" is mutating only as "switch".
var gitRepoActions = map[string]bool{
	"commit": true, "stash": true, "restore": true, "reset": true,
	"cherry_pick": true, "rebase": true,
}

// MutatesRepo reports whether a call of tool with args changes the working
// tree or history: a git action from gitRepoActions, `branch switch`, or a
// shell command matching mutatingGit. Near-miss spellings ("cherry-pick",
// "Commit") are accepted, since models write them that way.
func MutatesRepo(tool string, args json.RawMessage) bool {
	switch tool {
	case "git":
		var in struct {
			Action    string `json:"action"`
			SubAction string `json:"sub_action"`
		}
		if json.Unmarshal(args, &in) != nil {
			return false
		}
		act := normalizeAction(in.Action)
		if act == "branch" {
			return normalizeAction(in.SubAction) == "switch"
		}
		return gitRepoActions[act]
	case "shell":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &in) != nil {
			return false
		}
		return mutatingGit.MatchString(in.Command)
	}
	return false
}

func normalizeAction(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}
