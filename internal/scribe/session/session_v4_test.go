package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestListReportsMessageCountAndSize(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "counted")
	if err := s.AppendMessages(m.ID, []llm.Message{
		llm.UserText("one"), llm.UserText("two"), llm.UserText("three"),
	}); err != nil {
		t.Fatal(err)
	}
	var got Meta
	for _, x := range s.List() {
		if x.ID == m.ID {
			got = x
		}
	}
	if got.Messages != 3 {
		t.Fatalf("Messages = %d, want 3", got.Messages)
	}
	st, err := os.Stat(filepath.Join(s.Dir(m.ID), "messages.jsonl"))
	if err != nil || got.SizeBytes != st.Size() || got.SizeBytes == 0 {
		t.Fatalf("SizeBytes = %d, file %v", got.SizeBytes, err)
	}
	// The counts are for the picker only and must not be persisted.
	saved, _ := s.readMeta(m.ID)
	if saved.Messages != 0 || saved.SizeBytes != 0 {
		t.Fatalf("list stats leaked into meta.json: %+v", saved)
	}
}

func TestCheckpointLineDelta(t *testing.T) {
	e := newCP(t)
	rel := "lines.txt"
	abs := e.write(rel, []byte("a\nb\nc\n"), 0o644)
	e.cp.Begin(1, "edit")
	e.cp.BeforeWrite(abs)
	if err := os.WriteFile(abs, []byte("a\nB\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Open turn: counted against the disk.
	if open := e.cp.List(); len(open) != 1 || open[0].Added != 2 || open[0].Removed != 1 {
		t.Fatalf("open turn delta = +%d -%d, want +2 -1", open[0].Added, open[0].Removed)
	}
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	cps := e.cp.List()
	if len(cps) != 1 || cps[0].Added != 2 || cps[0].Removed != 1 {
		t.Fatalf("closed turn delta = %+v, want +2 -1", cps)
	}
}

func TestCheckpointLineDeltaNewAndBinaryFiles(t *testing.T) {
	e := newCP(t)
	created := filepath.Join(e.root, "new.txt")
	bin := e.write("blob.bin", []byte{0x00, 0x01, 0x02}, 0o644)
	e.cp.Begin(1, "make and overwrite")
	e.cp.BeforeWrite(created)
	e.cp.BeforeWrite(bin)
	if err := os.WriteFile(created, []byte("x\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte{0x00, 0x09}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	cps := e.cp.List()
	// The new text file adds two lines; the binary file counts as zero.
	if len(cps) != 1 || cps[0].Added != 2 || cps[0].Removed != 0 {
		t.Fatalf("delta = +%d -%d, want +2 -0", cps[0].Added, cps[0].Removed)
	}
}

func TestPicksRankByFrecency(t *testing.T) {
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	now := base
	frecencyNow = func() time.Time { return now }
	t.Cleanup(func() { frecencyNow = time.Now })

	// model-a: picked 4 times, last a month ago. model-b: picked twice, today.
	for i := 0; i < 4; i++ {
		if err := RecordPick(PickModel, "model-a"); err != nil {
			t.Fatal(err)
		}
	}
	now = base.Add(30 * 24 * time.Hour)
	if err := RecordPick(PickModel, "model-b"); err != nil {
		t.Fatal(err)
	}
	if err := RecordPick(PickModel, "model-b"); err != nil {
		t.Fatal(err)
	}
	// Other kinds do not mix in.
	if err := RecordPick(PickAgent, "explore"); err != nil {
		t.Fatal(err)
	}

	got, err := TopPicks(PickModel, 0)
	if err != nil {
		t.Fatal(err)
	}
	// model-a: 4/(1+30); model-b: 2/(1+0). model-b wins.
	if len(got) != 2 || got[0] != "model-b" || got[1] != "model-a" {
		t.Fatalf("ranking = %v", got)
	}
	if top1, _ := TopPicks(PickModel, 1); len(top1) != 1 {
		t.Fatalf("n=1 returned %d", len(top1))
	}
	if agents, _ := TopPicks(PickAgent, 0); len(agents) != 1 || agents[0] != "explore" {
		t.Fatalf("agent kind = %v", agents)
	}
	if none, _ := TopPicks(PickFile, 0); len(none) != 0 {
		t.Fatalf("file kind should be empty, got %v", none)
	}
}

func TestPicksCapPerKind(t *testing.T) {
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	frecencyNow = func() time.Time { return now }
	t.Cleanup(func() { frecencyNow = time.Now })
	for i := 0; i < FrecencyCap+3; i++ {
		if err := RecordPick(PickFile, "f"+string(rune('A'+i%26))+string(rune('a'+i/26))); err != nil {
			t.Fatal(err)
		}
	}
	if err := RecordPick(PickCommand, "/help"); err != nil {
		t.Fatal(err)
	}
	files, _ := TopPicks(PickFile, 0)
	if len(files) != FrecencyCap {
		t.Fatalf("files kept %d, want %d", len(files), FrecencyCap)
	}
	if cmds, _ := TopPicks(PickCommand, 0); len(cmds) != 1 {
		t.Fatalf("cap must not touch other kinds, commands = %v", cmds)
	}
}

func TestBlankPickIgnored(t *testing.T) {
	t.Setenv("SCRIBE_HOME", filepath.Join(t.TempDir(), "scribe"))
	if err := RecordPick(PickModel, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := TopPicks(PickModel, 0); len(got) != 0 {
		t.Fatalf("blank key recorded: %v", got)
	}
}
