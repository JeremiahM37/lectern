package tui

import (
	"strings"
	"testing"
)

// With checkpoints and none browsed, the footer says where the user is and how
// to step back. Without checkpoints the rotating tip stays.
func TestCheckpointStripShowsOnceThereAreCheckpoints(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	if strings.Contains(m.viewFooter(), "latest") {
		t.Fatal("no checkpoints yet, the strip should not show")
	}
	m.cur().cps = []CheckpointInfo{{Turn: 1}, {Turn: 2}}
	foot := m.viewFooter()
	if !strings.Contains(foot, "latest") || !strings.Contains(foot, "2 checkpoints") || !strings.Contains(foot, "Ctrl+B") {
		t.Fatalf("footer = %q, want the checkpoint strip", foot)
	}
}
