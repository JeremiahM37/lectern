package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

// -s and --session are the same as --resume: an id, or the picker alone.
func TestCodeTUISessionFlagIsResumeAlias(t *testing.T) {
	cases := []struct {
		args    []string
		resume  string
		picker  bool
		comment string
	}{
		{[]string{"-s", "7f3c9a21"}, "7f3c9a21", false, "-s with an id"},
		{[]string{"--session", "7f3c9a21"}, "7f3c9a21", false, "--session with an id"},
		{[]string{"--session=7f3c9a21"}, "7f3c9a21", false, "--session=id"},
		{[]string{"-s"}, "", true, "-s alone opens the picker"},
		{[]string{"--resume", "7f3c9a21"}, "7f3c9a21", false, "--resume is unchanged"},
	}
	for _, c := range cases {
		rec := withTUI(t)
		var errOut bytes.Buffer
		if code := codeTUI(c.args, &errOut); code != 0 {
			t.Fatalf("%s: code %d err %q", c.comment, code, errOut.String())
		}
		if rec.opts.Resume != c.resume {
			t.Errorf("%s: resume %q, want %q", c.comment, rec.opts.Resume, c.resume)
		}
		if hasPicker := strings.Contains(strings.Join(rec.args, "|"), "--pick"); hasPicker != c.picker {
			t.Errorf("%s: picker %v, want %v (args %q)", c.comment, hasPicker, c.picker, rec.args)
		}
	}
}

// -s cannot be combined with --continue, the same as --resume.
func TestCodeTUISessionFlagRejectsContinue(t *testing.T) {
	withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"-s", "abc", "-c"}, &errOut); code != 2 {
		t.Fatalf("code %d, want 2; err %q", code, errOut.String())
	}
}

// `lectern code --continue` resumes the newest session in the folder and asks
// the UI to re-run its interrupted turn.
func TestCodeTUIContinueAsksForInterruptedTurn(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	dir := t.TempDir()
	meta := session.Meta{Workdir: dir, Title: "fix the build"}
	if err := session.NewStore(dir).Create(&meta); err != nil {
		t.Fatal(err)
	}
	rec := withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--continue", "--cwd", dir}, &errOut); code != 0 {
		t.Fatalf("code %d err %q", code, errOut.String())
	}
	if rec.opts.Resume != meta.ID {
		t.Fatalf("resume %q, want the newest session %q", rec.opts.Resume, meta.ID)
	}
	if strings.Join(rec.args, "|") != "--continue-turn" {
		t.Fatalf("args %q, want --continue-turn", rec.args)
	}
}

// A plain resume must not re-run anything on its own.
func TestCodeTUIResumeDoesNotContinue(t *testing.T) {
	rec := withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"-s", "abc", "--cwd", t.TempDir()}, &errOut); code != 0 {
		t.Fatalf("code %d err %q", code, errOut.String())
	}
	if strings.Contains(strings.Join(rec.args, "|"), "--continue-turn") {
		t.Fatalf("resume alone re-ran a turn: args %q", rec.args)
	}
}

// --continue only reopens a session the current SCRIBE_HOME recorded. The
// newer session here was made under another home, so the older one wins.
func TestCodeTUIContinueIgnoresOtherScribeHome(t *testing.T) {
	dir := t.TempDir()
	homeA, homeB := t.TempDir(), t.TempDir()
	t.Setenv("SCRIBE_HOME", homeA)
	mineA := session.Meta{Workdir: dir, Title: "made here"}
	if err := session.NewStore(dir).Create(&mineA); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIBE_HOME", homeB)
	other := session.Meta{Workdir: dir, Title: "made under another home"}
	if err := session.NewStore(dir).Create(&other); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIBE_HOME", homeA)
	rec := withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--continue", "--cwd", dir}, &errOut); code != 0 {
		t.Fatalf("code %d err %q", code, errOut.String())
	}
	if rec.opts.Resume != mineA.ID {
		t.Fatalf("resume %q, want %q (the session of this home), not %q", rec.opts.Resume, mineA.ID, other.ID)
	}
}
