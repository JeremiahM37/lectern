package skills

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"golang.org/x/crypto/ssh"
)

func TestLocalSkillLifecycleAndForeignCollision(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	source := filepath.Join(repo, ".agents", "skills", "lint")
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "sub", ".agents", "skills", "lint")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SKILL.md"), []byte("name: nested\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: lint\ndescription: check\n---\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if out := exec.Command("git", "init", "-q", repo).Run(); out != nil {
		t.Fatal(out)
	}
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", work).Run(); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{RepoPath: filepath.Join(repo, "sub")}
	ex := executor.NewLocal()
	ctx := context.Background()
	xs, err := Discover(ctx, ex, p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	var x Skill
	seenLint := map[string]bool{}
	for _, candidate := range xs {
		if candidate.EntryName == "lint" && strings.HasPrefix(candidate.Source, "repo:") {
			seenLint[candidate.SourcePath] = true
		}
		if candidate.EntryName == "lint" && strings.HasPrefix(candidate.Source, "repo:") {
			x = candidate
		}
	}
	if x.EntryName != "lint" {
		t.Fatalf("discover=%+v", xs)
	}
	if len(seenLint) != 2 {
		t.Fatalf("nested and root repo roots collapsed: %v", seenLint)
	}
	p.RepoPath = repo
	dst, _, err := Materialize(ctx, ex, p, x, "codex", work, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(dst)
	if err != nil || target != source {
		t.Fatalf("link=%q err=%v", target, err)
	}
	if _, _, err := Materialize(ctx, ex, p, x, "codex", work, 8, false); err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("unrecorded existing link was adopted: %v", err)
	}
	exclude, err := os.ReadFile(filepath.Join(work, ".git", "info", "exclude"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(exclude), "# lectern-owned-skill:7:") || !strings.Contains(string(exclude), "/.agents/skills/lint\n") {
		t.Fatalf("exclude=%q", exclude)
	}
	if err := os.WriteFile(filepath.Join(work, "foreign.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	ax := &store.ProjectSkill{SourcePath: source, TargetRel: ".agents/skills/lint", ExcludeMarker: "# lectern-owned-skill:7"}
	if err := Remove(ctx, ex, p, ax, work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "foreign.txt")); err != nil {
		t.Fatal("foreign content removed", err)
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("skill link remains: %v", err)
	}
}

func TestConfiguredSourceDiscovery(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "configured", "skill")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("name: x\n"), 0644)
	p := &store.Project{RepoPath: filepath.Join(root, "missing"), SkillSourcesJSON: store.J([]string{filepath.Dir(src)})}
	xs, err := Discover(context.Background(), executor.NewLocal(), p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, x := range xs {
		if strings.HasPrefix(x.Source, "configured:") {
			found = true
		}
	}
	if !found {
		t.Fatalf("configured skill missing: %+v", xs)
	}
}

func TestReassertPendingForeignSameTargetLinkNeverBecomesOwned(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	source := filepath.Join(root, "source", "review")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("name: review\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(repo, ".agents", "skills", "review")
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	// This is a user's link to the same source. Its target alone must never be
	// treated as proof that Lectern created it.
	if err := os.Symlink(source, dst); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&store.Target{Name: "pending-target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProject(&store.Project{Name: "pending-project", TargetID: target.ID, RepoPath: repo, DefaultAgent: "codex", SkillSourcesJSON: store.J([]string{filepath.Dir(source)})})
	if err != nil {
		t.Fatal(err)
	}
	xs, err := Discover(context.Background(), executor.NewLocal(), p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	var skill Skill
	for _, candidate := range xs {
		if candidate.SourcePath == source {
			skill = candidate
			break
		}
	}
	if skill.ID == "" {
		t.Fatalf("configured source was not discovered: %+v", xs)
	}
	attachment, err := db.InsertProjectSkill(&store.ProjectSkill{ProjectID: p.ID, TargetID: target.ID, Agent: "codex", SkillID: skill.ID, SourceID: skill.Source, SourcePath: skill.SourcePath, EntryName: skill.EntryName, TargetRel: ".agents/skills/review"})
	if err != nil {
		t.Fatal(err)
	}
	ex := executor.NewLocal()
	firstErr := Reassert(context.Background(), ex, db, p, "codex", repo)
	if firstErr == nil || !strings.Contains(firstErr.Error(), "destination already exists") {
		t.Fatalf("foreign collision result: %v", firstErr)
	}
	mats, err := db.Materializations(attachment.ID)
	if err != nil || len(mats) != 1 || mats[0].State != "pending" {
		t.Fatalf("first collision state: mats=%+v err=%v", mats, err)
	}
	if err := Reassert(context.Background(), ex, db, p, "codex", repo); err == nil {
		t.Fatal("retry adopted foreign same-target link")
	}
	mats, err = db.Materializations(attachment.ID)
	if err != nil || len(mats) != 1 || mats[0].State != "pending" {
		t.Fatalf("retry collision state: mats=%+v err=%v", mats, err)
	}
	if err := Clean(context.Background(), ex, db, p, repo); err == nil {
		t.Fatal("cleanup silently removed pending foreign link")
	}
	if _, err := os.Lstat(dst); err != nil {
		t.Fatalf("foreign link was removed: %v", err)
	}
}

func TestNativeSkillStaysInRepositoryAndLinksIntoChild(t *testing.T) {
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			repo := filepath.Join(root, "repo")
			native := filepath.Join(repo, map[string]string{"claude": ".claude", "codex": ".agents"}[agent], "skills", "native")
			if err := os.MkdirAll(native, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(native, "SKILL.md"), []byte("name: native\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
				t.Fatal(err)
			}
			if err := exec.Command("git", "-C", repo, "add", "-f", map[string]string{"claude": ".claude/skills/native", "codex": ".agents/skills/native"}[agent]).Run(); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern native", "-c", "user.email=lectern@example.invalid", "commit", "-qm", "native skill").CombinedOutput(); err != nil {
				t.Fatalf("native commit: %v %s", err, out)
			}
			db, err := store.Open(filepath.Join(root, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			target, err := db.InsertTarget(&store.Target{Name: "native-" + agent, Kind: "local"})
			if err != nil {
				t.Fatal(err)
			}
			p, err := db.InsertProject(&store.Project{Name: "native-" + agent, TargetID: target.ID, RepoPath: repo, DefaultAgent: agent})
			if err != nil {
				t.Fatal(err)
			}
			ex := executor.NewLocal()
			xs, err := Discover(context.Background(), ex, p, agent)
			if err != nil {
				t.Fatal(err)
			}
			var skill Skill
			for _, candidate := range xs {
				if candidate.SourcePath == native {
					skill = candidate
				}
			}
			if skill.ID == "" {
				t.Fatalf("native skill not discovered: %+v", xs)
			}
			attachment, err := db.InsertProjectSkill(&store.ProjectSkill{ProjectID: p.ID, TargetID: target.ID, Agent: agent, SkillID: skill.ID, SourceID: skill.Source, SourcePath: skill.SourcePath, EntryName: skill.EntryName, TargetRel: filepath.ToSlash(filepath.Join(map[string]string{"claude": ".claude", "codex": ".agents"}[agent], "skills", "native"))})
			if err != nil {
				t.Fatal(err)
			}
			if err := Reassert(context.Background(), ex, db, p, agent, repo); err != nil {
				t.Fatal(err)
			}
			mats, err := db.Materializations(attachment.ID)
			if err != nil || len(mats) != 1 || mats[0].State != "preexisting" {
				t.Fatalf("native state: mats=%+v err=%v", mats, err)
			}
			child := filepath.Join(root, "child")
			if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "--detach", child, "HEAD").CombinedOutput(); err != nil {
				t.Fatalf("native child worktree: %v %s", err, out)
			}
			t.Cleanup(func() { _ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", child).Run() })
			if _, err := os.Stat(filepath.Join(child, map[string]string{"claude": ".claude", "codex": ".agents"}[agent], "skills", "native", "SKILL.md")); err != nil {
				t.Fatal(err)
			}
			if err := Reassert(context.Background(), ex, db, p, agent, child); err != nil {
				t.Fatal(err)
			}
			childLink := filepath.Join(child, map[string]string{"claude": ".claude", "codex": ".agents"}[agent], "skills", "native")
			if info, err := os.Stat(childLink); err != nil || !info.IsDir() {
				t.Fatalf("child native destination missing: info=%v err=%v", info, err)
			}
			if err := os.WriteFile(filepath.Join(childLink, "SKILL.md"), []byte("name: native\nuser edit\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := Reassert(context.Background(), ex, db, p, agent, child); err != nil {
				t.Fatal(err)
			}
			childBody, err := os.ReadFile(filepath.Join(childLink, "SKILL.md"))
			if err != nil || string(childBody) != "name: native\nuser edit\n" {
				t.Fatalf("child native content changed: %q err=%v", childBody, err)
			}
			if err := Clean(context.Background(), ex, db, p, child); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(childLink); err != nil {
				t.Fatalf("native child destination was removed: %v", err)
			}
			if _, err := os.Stat(native); err != nil {
				t.Fatalf("native repository skill changed: %v", err)
			}
		})
	}
}

func TestMaterializeRejectsSymlinkedSkillParent(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	source := filepath.Join(root, "source", "safe")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("name: safe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(work, ".agents")); err != nil {
		t.Fatal(err)
	}
	_, _, err := Materialize(context.Background(), executor.NewLocal(), &store.Project{RepoPath: work}, Skill{Source: "configured:safe", SourcePath: source, EntryName: "safe"}, "codex", work, 1, false)
	if err == nil {
		t.Fatal("materialization followed a symlinked skill parent")
	}
	if _, err := os.Stat(filepath.Join(outside, "skills")); !os.IsNotExist(err) {
		t.Fatalf("outside path was modified: %v", err)
	}
}

func TestNativeSkillForeignUntrackedDestinationIsPreserved(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	foreignRepo := filepath.Join(root, "foreign")
	native := filepath.Join(repo, ".agents", "skills", "native")
	foreign := filepath.Join(foreignRepo, ".agents", "skills", "native")
	for _, dir := range []string{native, foreign} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("foreign content\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{repo, foreignRepo} {
		if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
			t.Fatal(err)
		}
	}
	if err := exec.Command("git", "-C", repo, "add", ".").Run(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern native", "-c", "user.email=lectern@example.invalid", "commit", "-qm", "native").CombinedOutput(); err != nil {
		t.Fatalf("native commit: %v %s", err, out)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&store.Target{Name: "foreign-native-target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProject(&store.Project{Name: "foreign-native-project", TargetID: target.ID, RepoPath: repo, DefaultAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	xs, err := Discover(context.Background(), executor.NewLocal(), p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	var skill Skill
	for _, candidate := range xs {
		if candidate.SourcePath == native {
			skill = candidate
		}
	}
	if skill.ID == "" {
		t.Fatalf("native skill not discovered: %+v", xs)
	}
	attachment, err := db.InsertProjectSkill(&store.ProjectSkill{ProjectID: p.ID, TargetID: target.ID, Agent: "codex", SkillID: skill.ID, SourceID: skill.Source, SourcePath: native, EntryName: "native", TargetRel: ".agents/skills/native"})
	if err != nil {
		t.Fatal(err)
	}
	err = Reassert(context.Background(), executor.NewLocal(), db, p, "codex", foreignRepo)
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("foreign native destination result: %v", err)
	}
	mats, err := db.Materializations(attachment.ID)
	if err != nil || len(mats) != 1 || mats[0].State != "pending" {
		t.Fatalf("foreign destination state: mats=%+v err=%v", mats, err)
	}
	if body, err := os.ReadFile(filepath.Join(foreign, "SKILL.md")); err != nil || string(body) != "foreign content\n" {
		t.Fatalf("foreign native content changed: %q err=%v", body, err)
	}
}

func TestNativeSkillNestedDestinationCannotBorrowTrackedSource(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	native := filepath.Join(repo, ".agents", "skills", "native")
	nested := filepath.Join(repo, "nested", ".agents", "skills", "native")
	if err := os.MkdirAll(native, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(native, "SKILL.md"), []byte("tracked source\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "SKILL.md"), []byte("nested foreign\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", "-f", ".agents/skills/native/SKILL.md").Run(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern nested", "-c", "user.email=lectern@example.invalid", "commit", "-qm", "native").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&store.Target{Name: "nested-target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProject(&store.Project{Name: "nested-project", TargetID: target.ID, RepoPath: repo, DefaultAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	xs, err := Discover(context.Background(), executor.NewLocal(), p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	var skill Skill
	for _, candidate := range xs {
		if candidate.SourcePath == native {
			skill = candidate
		}
	}
	attachment, err := db.InsertProjectSkill(&store.ProjectSkill{ProjectID: p.ID, TargetID: target.ID, Agent: "codex", SkillID: skill.ID, SourceID: skill.Source, SourcePath: native, EntryName: "native", TargetRel: ".agents/skills/native"})
	if err != nil {
		t.Fatal(err)
	}
	err = Reassert(context.Background(), executor.NewLocal(), db, p, "codex", filepath.Join(repo, "nested"))
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("nested foreign destination result: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(nested, "SKILL.md")); err != nil || string(body) != "nested foreign\n" {
		t.Fatalf("nested foreign content changed: %q err=%v", body, err)
	}
	mats, err := db.Materializations(attachment.ID)
	if err != nil || len(mats) != 1 || mats[0].State != "pending" {
		t.Fatalf("nested destination state: mats=%+v err=%v", mats, err)
	}
}

func TestSkillDBErrorsAreReturned(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.InsertTarget(&store.Target{Name: "db-error-target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProject(&store.Project{Name: "db-error-project", TargetID: target.ID, RepoPath: repo, DefaultAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Reassert(context.Background(), executor.NewLocal(), db, p, "codex", repo); err == nil {
		t.Fatal("Reassert swallowed a closed database error")
	}
	if err := Clean(context.Background(), executor.NewLocal(), db, p, repo); err == nil {
		t.Fatal("Clean swallowed a closed database error")
	}
}

func TestSiblingWorktreesRetainIndependentExcludeMarkers(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	source := filepath.Join(root, "source", "shared")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("name: shared\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "seed"), []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", repo, "add", "seed").Run(); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "-c", "user.name=Lectern siblings", "-c", "user.email=lectern@example.invalid", "commit", "-qm", "seed").CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	db, err := store.Open(filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	target, err := db.InsertTarget(&store.Target{Name: "siblings-target", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.InsertProject(&store.Project{Name: "siblings-project", TargetID: target.ID, RepoPath: repo, DefaultAgent: "codex", SkillSourcesJSON: store.J([]string{filepath.Dir(source)})})
	if err != nil {
		t.Fatal(err)
	}
	ex := executor.NewLocal()
	xs, err := Discover(context.Background(), ex, p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	var skill Skill
	for _, candidate := range xs {
		if candidate.SourcePath == source {
			skill = candidate
		}
	}
	attachment, err := db.InsertProjectSkill(&store.ProjectSkill{ProjectID: p.ID, TargetID: target.ID, Agent: "codex", SkillID: skill.ID, SourceID: skill.Source, SourcePath: source, EntryName: "shared", TargetRel: ".agents/skills/shared"})
	if err != nil {
		t.Fatal(err)
	}
	w1, w2 := filepath.Join(root, "w1"), filepath.Join(root, "w2")
	for _, work := range []string{w1, w2} {
		if out, err := exec.Command("git", "-C", repo, "worktree", "add", "-q", "--detach", work, "HEAD").CombinedOutput(); err != nil {
			t.Fatalf("worktree %s: %v %s", work, err, out)
		}
	}
	t.Cleanup(func() {
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", w1).Run()
		_ = exec.Command("git", "-C", repo, "worktree", "remove", "--force", w2).Run()
	})
	for _, work := range []string{w1, w2} {
		if err := Reassert(context.Background(), ex, db, p, "codex", work); err != nil {
			t.Fatal(err)
		}
	}
	info, err := exec.Command("git", "-C", w1, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		t.Fatal(err)
	}
	excludePath := strings.TrimSpace(string(info))
	if !filepath.IsAbs(excludePath) {
		excludePath = filepath.Join(w1, excludePath)
	}
	before, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	m1, m2 := Marker(attachment.ID, w1), Marker(attachment.ID, w2)
	if !strings.Contains(string(before), m1) || !strings.Contains(string(before), m2) {
		t.Fatalf("sibling markers missing: %q", before)
	}
	if err := Clean(context.Background(), ex, db, p, w1); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(after), m1) || !strings.Contains(string(after), m2) {
		t.Fatalf("sibling cleanup removed wrong marker: %q", after)
	}
}

func TestSSHSkillLifecycleUsesRealTransport(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.MarshalPrivateKey(private, "skill-test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(key), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) { return nil, nil }}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, e := ln.Accept()
		if e != nil {
			return
		}
		conn, chans, reqs, e := ssh.NewServerConn(raw, cfg)
		if e != nil {
			return
		}
		defer conn.Close()
		go ssh.DiscardRequests(reqs)
		for nc := range chans {
			ch, reqs, e := nc.Accept()
			if e != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range reqs {
					if req.Type != "exec" {
						_ = req.Reply(false, nil)
						continue
					}
					var p struct{ Command string }
					ssh.Unmarshal(req.Payload, &p)
					_ = req.Reply(true, nil)
					c := exec.Command("bash", "-c", p.Command)
					c.Stdin = ch
					c.Stdout = ch
					c.Stderr = ch.Stderr()
					rc := uint32(0)
					if c.Run() != nil {
						rc = 1
					}
					_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ RC uint32 }{rc}))
					return
				}
			}()
		}
	}()
	ex := executor.NewSSH("127.0.0.1", "test", ln.Addr().(*net.TCPAddr).Port, keyPath, "")
	defer func() { _ = ex.Close(); <-done }()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	src := filepath.Join(root, "src", "remote")
	work := filepath.Join(root, "work")
	for _, d := range []string{filepath.Join(src), work} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", repo).Run(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("name: remote\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "-q", work).Run(); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{RepoPath: repo, SkillSourcesJSON: store.J([]string{filepath.Dir(src)})}
	xs, err := Discover(context.Background(), ex, p, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(xs) == 0 {
		t.Fatal("remote skill not discovered")
	}
	if _, _, err := Materialize(context.Background(), ex, p, xs[0], "codex", work, 11, false); err != nil {
		t.Fatal(err)
	}
}
