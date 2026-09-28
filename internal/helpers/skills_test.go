//go:build unix

package helpers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSkillsParity(t *testing.T) {
	script := pythonConst(t, "skills/skills.go", "script")
	req := func(kv ...any) []string {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i].(string)] = kv[i+1]
		}
		raw, _ := json.Marshal(m)
		return []string{string(raw)}
	}
	skill := func(dir, front string) {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(dir+"/SKILL.md", []byte(front), 0o644)
	}
	build := func(t *testing.T, r string) {
		gitFixture(t, r+"/repo", map[string]string{
			".claude/skills/alpha/SKILL.md":    "---\nname: Alpha Skill\ndescription:  does a \n---\n",
			".agents/skills/gamma/SKILL.md":    "NAME:\nDescription: g\r\nbody",
			"sub/.claude/skills/deep/SKILL.md": "name: deep",
		})
		skill(r+"/home/.claude/skills/beta", "description: b name: Beta\n")
		skill(r+"/home/.agents/skills/delta", "x")
		skill(r+"/home/.gemini/skills/eps", "name: e")
		skill(r+"/cfg/opencode/skill/zeta", "name: z")
		skill(r+"/extra/one", "name: one")
		os.MkdirAll(r+"/home/.claude/skills/not-a-skill", 0o755)
		os.WriteFile(r+"/home/.claude/skills/bad/SKILL.md", nil, 0o644)
		skill(r+"/home/.claude/skills/latin", "name: caf\xe9\n")
		gitFixture(t, r+"/wt", map[string]string{"README": "x"})
		gitIn(t, r+"/repo", "worktree", "add", "-q", r+"/linked")
	}
	spec := func(r string) runSpec {
		return runSpec{dir: r, env: []string{"HOME=" + r + "/home", "XDG_CONFIG_HOME=" + r + "/cfg", "GIT_CONFIG_GLOBAL=/dev/null"},
			unset: []string{"CLAUDE_CONFIG_DIR"}}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, r string)
		steps func(r string) [][]string
	}{
		{name: "discover", steps: func(r string) [][]string {
			return [][]string{
				req("op", "discover", "repo", r+"/repo/sub", "agent", "claude"),
				req("op", "discover", "repo", r+"/repo", "agent", "codex", "configured", []string{r + "/extra", r + "/missing"}),
				req("op", "discover", "agent", "gemini"),
				req("op", "discover", "agent", "opencode", "repo", r+"/repo"),
				req("op", "discover", "agent", "qwen", "repo", r+"/nowhere"),
			}
		}},
		{name: "discover undecodable", setup: func(t *testing.T, r string) {
			skill(r+"/home/.claude/skills/zz", "name: \xff")
		}, steps: func(r string) [][]string { return [][]string{req("op", "discover")} }},
		{name: "materialize and remove", steps: func(r string) [][]string {
			src, dst := r+"/home/.claude/skills/beta", r+"/wt/.claude/skills/beta"
			return [][]string{
				req("op", "materialize", "source", src, "target", dst, "base", r+"/wt", "owned", true, "source_kind", "claude-user"),
				req("op", "materialize", "source", src, "target", dst, "base", r+"/wt", "owned", true),
				req("op", "materialize", "source", src, "target", dst, "base", r+"/wt", "owned", false),
				req("op", "materialize", "source", src, "target", src, "base", r+"/home"),
				req("op", "materialize", "source", r+"/home/.claude/skills/not-a-skill", "target", r+"/wt/.claude/skills/x", "base", r+"/wt"),
				req("op", "remove", "source", src, "target", dst, "base", r+"/wt", "owned", false),
				req("op", "remove", "source", r+"/extra/one", "target", dst, "base", r+"/wt", "owned", true),
				req("op", "remove", "source", src, "target", dst, "base", r+"/wt", "owned", true),
				req("op", "remove", "source", src, "target", dst, "base", r+"/wt", "owned", true),
				req("op", "remove", "source", src, "target", r+"/wt/none/x/y", "base", r+"/wt", "owned", true),
			}
		}},
		{name: "tracked native skill", steps: func(r string) [][]string {
			return [][]string{req("op", "materialize", "source", r+"/repo/.claude/skills/alpha", "target", r+"/linked/.claude/skills/alpha",
				"base", r+"/linked", "source_kind", "repo:"+r+"/repo")}
		}},
		{name: "refusals", setup: func(t *testing.T, r string) {
			os.MkdirAll(r+"/outside", 0o755)
			os.Symlink(r+"/outside", r+"/wt/linkdir")
			os.Symlink(r+"/wt", r+"/wtlink")
			os.WriteFile(r+"/wt/plain", nil, 0o644)
		}, steps: func(r string) [][]string {
			src := r + "/home/.claude/skills/beta"
			return [][]string{
				req("op", "materialize", "source", src, "target", r+"/wt/linkdir/beta", "base", r+"/wt"),
				req("op", "materialize", "source", src, "target", r+"/elsewhere/beta", "base", r+"/wt"),
				req("op", "materialize", "source", src, "target", r+"/wt/x/beta", "base", r+"/wtlink"),
				req("op", "materialize", "source", src, "target", r+"/wt/plain", "base", r+"/wt"),
				req("op", "remove", "source", src, "target", r+"/wt/plain", "base", r+"/wt", "owned", true),
				req("op", "frobnicate"),
				req("op", "materialize", "target", "x"),
				{"[]"},
			}
		}},
		{name: "exclude", steps: func(r string) [][]string {
			return [][]string{
				req("op", "exclude", "repo", r+"/wt", "marker", "# lectern-skill:1", "line", ".claude/skills/a*b[1]"),
				req("op", "exclude", "repo", r+"/wt", "marker", "# lectern-skill:1", "line", ".claude/skills/a*b[1]"),
				req("op", "exclude", "repo", r+"/wt", "marker", "# lectern-skill:2", "line", `x\y`),
				req("op", "unexclude", "repo", r+"/wt", "marker", "# lectern-skill:1", "line", ".claude/skills/a*b[1]"),
				req("op", "exclude", "repo", r+"/linked", "marker", "# m", "line", "l"),
				req("op", "exclude", "repo", r+"/home", "marker", "# m", "line", "l"),
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			twinSteps(t, script, "skills", func(root, s string) string {
				sum := sha256.Sum256([]byte(pyRealpath(root + "/extra")))
				return strings.ReplaceAll(s, hex.EncodeToString(sum[:])[:12], "$HASH")
			}, func(t *testing.T, root string) (runSpec, [][]string) {
				build(t, root)
				if c.setup != nil {
					c.setup(t, root)
				}
				return spec(root), c.steps(root)
			})
		})
	}
}
