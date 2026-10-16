package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/app"
)

// `lectern help code` and `lectern code --help` list every flag.
func TestHelpCodeListsEveryFlag(t *testing.T) {
	for _, args := range [][]string{{"help", "code"}, {"code", "--help"}, {"scribe", "--help"}} {
		var out, errOut bytes.Buffer
		code, handled := helpCommand(args, &out, &errOut)
		if !handled || code != 0 {
			t.Fatalf("%v: handled=%v code=%d err=%q", args, handled, code, errOut.String())
		}
		for _, flag := range []string{
			"--model", "--mode", "--cwd", "--resume", "--continue", "--demo",
			"-p, --print", "--json", "--events", "--chat", "--quiet", "--max-steps",
			"--timeout", "--system", "--include", "--no-repomap", "--diff",
			"--save-session", "--session", "--render", "--no-render",
			"--list-providers", "--list-models", "--set-key",
		} {
			if !strings.Contains(out.String(), flag) {
				t.Errorf("%v: help is missing %s", args, flag)
			}
		}
		if !strings.Contains(out.String(), "lectern code -p") {
			t.Errorf("%v: help has no examples", args)
		}
	}
}

func TestHelpOverviewListsCode(t *testing.T) {
	var out, errOut bytes.Buffer
	helpCommand([]string{"help"}, &out, &errOut)
	if !strings.Contains(out.String(), "code [PROMPT]") || !strings.Contains(out.String(), "Lectern's own coding agent, in this terminal") {
		t.Errorf("overview does not list code:\n%s", out.String())
	}
}

func TestCodeWordsAreReserved(t *testing.T) {
	for _, w := range []string{"code", "scribe"} {
		if !reservedVerbs[w] {
			t.Errorf("%q must be reserved so it is never sent to a server", w)
		}
	}
}

func TestStripHeadlessWords(t *testing.T) {
	got := stripHeadlessWords([]string{"--headless", "-p", "hi there", "--json"})
	if strings.Join(got, "|") != "hi there|--json" {
		t.Errorf("stripHeadlessWords = %q", got)
	}
}

func TestWantsHeadless(t *testing.T) {
	tty := os.Stdin // not a terminal under go test, so both cases below hold
	if !wantsHeadless([]string{"--json", "x"}, tty) {
		t.Error("--json should select headless")
	}
	if !wantsHeadless([]string{"--set-key", "anthropic", "k"}, strings.NewReader("")) {
		t.Error("--set-key should select headless")
	}
	if !wantsHeadless(nil, strings.NewReader("piped")) {
		t.Error("a non-terminal stdin should select headless")
	}
}

func TestCodeHeadlessListProviders(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	var out, errOut bytes.Buffer
	code := codeCommand([]string{"--list-providers"}, strings.NewReader(""), &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "PROVIDER") || !strings.Contains(out.String(), "anthropic") {
		t.Errorf("code %d out %q err %q", code, out.String(), errOut.String())
	}
}

// withTUI installs a fake scribeTUI that records what it was given.
func withTUI(t *testing.T) *struct {
	opts app.Options
	args []string
	ran  bool
} {
	t.Helper()
	rec := &struct {
		opts app.Options
		args []string
		ran  bool
	}{}
	prev := scribeTUI
	scribeTUI = func(_ context.Context, opts app.Options, args []string) int {
		rec.opts, rec.args, rec.ran = opts, args, true
		return 0
	}
	t.Cleanup(func() { scribeTUI = prev })
	return rec
}

func TestCodeTUIPassesFlags(t *testing.T) {
	rec := withTUI(t)
	var errOut bytes.Buffer
	code := codeTUI([]string{"--model", "anthropic/m", "--mode", "plan", "--resume", "abc", "--demo", "fix", "the", "tests"}, &errOut)
	if code != 0 || !rec.ran {
		t.Fatalf("code %d ran %v err %q", code, rec.ran, errOut.String())
	}
	if rec.opts.Model != "anthropic/m" || rec.opts.Mode != "plan" || rec.opts.Resume != "abc" {
		t.Errorf("opts = %+v", rec.opts)
	}
	if strings.Join(rec.args, "|") != "--demo|--prompt|fix the tests" {
		t.Errorf("args = %q", rec.args)
	}
}

func TestCodeTUIResumePicker(t *testing.T) {
	rec := withTUI(t)
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--resume"}, &errOut); code != 0 {
		t.Fatalf("code %d err %q", code, errOut.String())
	}
	if rec.opts.Resume != "" || strings.Join(rec.args, "|") != "--pick" {
		t.Errorf("picker: resume %q args %q", rec.opts.Resume, rec.args)
	}
}

func TestCodeTUIContinueUsesNewestSession(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	rec := withTUI(t)
	var errOut bytes.Buffer
	// No saved session in an empty folder: starts a new one and says so.
	if code := codeTUI([]string{"-c", "--cwd", t.TempDir()}, &errOut); code != 0 {
		t.Fatalf("code %d err %q", code, errOut.String())
	}
	if rec.opts.Resume != "" || !strings.Contains(errOut.String(), "no earlier session") {
		t.Errorf("continue with no sessions: resume %q err %q", rec.opts.Resume, errOut.String())
	}
}

func TestCodeTUIRejectsBadInput(t *testing.T) {
	withTUI(t)
	cases := map[string][]string{
		"unknown mode":      {"--mode", "wizard"},
		"unknown flag":      {"--frobnicate"},
		"resume+continue":   {"--resume", "x", "-c"},
		"model needs value": {"--model"},
	}
	for name, args := range cases {
		var errOut bytes.Buffer
		if code := codeTUI(args, &errOut); code != 2 {
			t.Errorf("%s: exit %d, want 2 (err %q)", name, code, errOut.String())
		}
	}
}

func TestCodeTUIWithoutTUIExplainsItself(t *testing.T) {
	prev := scribeTUI
	scribeTUI = nil
	t.Cleanup(func() { scribeTUI = prev })
	var errOut bytes.Buffer
	if code := codeTUI([]string{"--demo"}, &errOut); code != 1 || !strings.Contains(errOut.String(), "the Scribe TUI is not built yet") {
		t.Errorf("exit %d err %q", code, errOut.String())
	}
}
