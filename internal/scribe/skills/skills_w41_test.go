package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTaskSkill makes a skill directory with a SKILL.md and returns its Skill.
func writeTaskSkill(t *testing.T, name, description, body string) Skill {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := "---\nname: " + name + "\ndescription: " + description + "\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, SkillFile), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return Skill{Name: name, Description: description, Dir: dir}
}

func TestScoreWeighsNameWordsAboveDescriptionWords(t *testing.T) {
	sk := Skill{Name: "release-notes", Description: "write changelog entries for a version"}
	if got := Score("draft the release notes", sk); got != MatchNameWeight*2 {
		t.Fatalf("name words scored %d, want %d", got, MatchNameWeight*2)
	}
	if got := Score("write changelog entries", sk); got != 3 {
		t.Fatalf("description words scored %d, want 3", got)
	}
	// Stopwords and short words never count.
	if got := Score("the and for with", sk); got != 0 {
		t.Fatalf("stopwords scored %d", got)
	}
}

func TestForTaskMatchesAtThresholdOnly(t *testing.T) {
	weak := writeTaskSkill(t, "lint-fix", "tidy imports", "WEAK-BODY") // one description word
	strong := writeTaskSkill(t, "changelog", "write entries", "STRONG-BODY")
	set := &Set{Skills: []Skill{weak, strong}}

	got := set.ForTask("changelog entry for the release")
	if len(got) != 1 || !strings.Contains(got[0], "Skill changelog:") || !strings.Contains(got[0], "STRONG-BODY") {
		t.Fatalf("ForTask = %q, want only the changelog skill", got)
	}
	if strings.Contains(strings.Join(got, "\n"), "WEAK-BODY") {
		t.Fatal("a skill below the threshold was injected")
	}
	if none := set.ForTask("unrelated gardening"); none != nil {
		t.Fatalf("ForTask on an unrelated task = %q", none)
	}
}

func TestForTaskCapsCountAndLength(t *testing.T) {
	long := strings.Repeat("instruction ", 400) // well over 2000 characters
	var skills []Skill
	for _, n := range []string{"alpha-tool", "beta-tool", "gamma-tool", "delta-tool"} {
		skills = append(skills, writeTaskSkill(t, n, "tool helper", long))
	}
	set := &Set{Skills: skills}
	got := set.ForTask("tool helper work for alpha beta gamma delta")
	if len(got) != MaxTaskSkills {
		t.Fatalf("matched %d skills, want at most %d", len(got), MaxTaskSkills)
	}
	for _, g := range got {
		body := g[strings.Index(g, "\n")+1:]
		if n := len([]rune(body)); n > MaxTaskSkillChars {
			t.Fatalf("skill body is %d runes, want at most %d", n, MaxTaskSkillChars)
		}
	}
}

func TestForTaskIsSafeOnNilAndEmptySets(t *testing.T) {
	var nilSet *Set
	if nilSet.ForTask("anything at all") != nil {
		t.Fatal("nil set matched")
	}
	if (&Set{}).ForTask("anything at all") != nil {
		t.Fatal("empty set matched")
	}
}
