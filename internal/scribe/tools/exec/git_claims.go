package exec

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
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

// claimWarning names the files a mutating git call may touch that other tabs
// have claimed. The reach is the files the call names plus the working tree's
// changed files, which is what commit, restore, reset and stash act on. It
// runs only when another tab holds a claim, so a single-tab session pays
// nothing. The call is not refused: the warning goes first in the result so
// the model can stop and ask. It returns "" when nothing overlaps.
func claimWarning(env *tools.Env, raw json.RawMessage) string {
	if env == nil || env.Claims == nil {
		return ""
	}
	others := env.Claims.OtherClaims()
	if len(others) == 0 {
		return ""
	}
	var a gitArgs
	if json.Unmarshal(raw, &a) != nil {
		return ""
	}
	reach := append([]string{}, a.Files...)
	if changed, err := ChangedFiles(env.Workdir); err == nil {
		reach = append(reach, changed...)
	}
	seen := map[string]bool{}
	var hits []string
	for _, f := range reach {
		abs := f
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(env.Workdir, f)
		}
		abs = filepath.Clean(abs)
		label, ok := others[abs]
		if !ok || seen[abs] {
			continue
		}
		seen[abs] = true
		hits = append(hits, fmt.Sprintf("%s (claimed by %s)", filepath.ToSlash(strings.TrimPrefix(abs, env.Workdir+string(filepath.Separator))), label))
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return fmt.Sprintf("WARNING: other Scribe tabs are editing files this git command may change: %s. Their work could be lost or mixed into this commit. Check with the user before continuing.", strings.Join(hits, "; "))
}
