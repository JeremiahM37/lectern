package app

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// cloneFake is a code map stand-in: Impact for the cross-file check and
// CloneHints for the duplication lines.
type cloneFake struct {
	hints map[string][]string
	delay time.Duration
}

func (c *cloneFake) Impact(string) (repomap.Impact, error) { return repomap.Impact{}, nil }

func (c *cloneFake) CloneHints(file string, _ int) []string {
	time.Sleep(c.delay)
	return c.hints[file]
}

func TestCloneBlockNamesDuplicatesOfEditedFile(t *testing.T) {
	dir := t.TempDir()
	p := newPostEditor(dir, nil)
	p.SetDependents(&cloneFake{hints: map[string][]string{
		"pkg/a.go": {"near-duplicate of pkg/b.go (about 90% of its shingles match)"},
	}})
	block := p.cloneBlock([]string{filepath.Join(dir, "pkg", "a.go")})
	if !strings.Contains(block, "Possible duplicates (advisory):") || !strings.Contains(block, "pkg/a.go: near-duplicate of pkg/b.go") {
		t.Fatalf("block = %q", block)
	}
}

func TestCloneBlockIsEmptyWithoutHintsOrMap(t *testing.T) {
	dir := t.TempDir()
	p := newPostEditor(dir, nil)
	if got := p.cloneBlock([]string{filepath.Join(dir, "a.go")}); got != "" {
		t.Fatalf("no map: block = %q", got)
	}
	p.SetDependents(&cloneFake{})
	if got := p.cloneBlock([]string{filepath.Join(dir, "a.go")}); got != "" {
		t.Fatalf("no hints: block = %q", got)
	}
}

func TestCloneBlockDropsASlowAnswer(t *testing.T) {
	dir := t.TempDir()
	p := newPostEditor(dir, nil)
	p.SetDependents(&cloneFake{delay: cloneBudget + 300*time.Millisecond, hints: map[string][]string{
		"a.go": {"would be shown if it were in time"},
	}})
	start := time.Now()
	got := p.cloneBlock([]string{filepath.Join(dir, "a.go")})
	if got != "" {
		t.Fatalf("a slow map must not add a block, got %q", got)
	}
	if time.Since(start) > cloneBudget+200*time.Millisecond {
		t.Fatalf("the edit waited %v, over the %v budget", time.Since(start), cloneBudget)
	}
}
