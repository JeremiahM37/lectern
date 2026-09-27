package ciloop

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestParseChecksPlainOutput(t *testing.T) {
	out := "build\tpass\t45s\thttps://github.com/a/b/actions/runs/11/job/21\t\n" +
		"test\tfail\t5m30s\thttps://github.com/a/b/actions/runs/11/job/22\t\n" +
		"lint\tpending\t0\thttps://github.com/a/b/actions/runs/11/job/23\t\n" +
		"deploy\tskipping\t0\t\t\n" +
		"weird\tneutral-ish\t0\t\t\n\n"
	got := parseChecks(out)
	if len(got) != 5 {
		t.Fatalf("want 5 rows, got %+v", got)
	}
	if got[1].Name != "test" || !got[1].failed() || got[1].Link != "https://github.com/a/b/actions/runs/11/job/22" {
		t.Fatalf("bad fail row: %+v", got[1])
	}
	if got[4].Bucket != "pending" {
		t.Fatalf("an unknown bucket must wait, not guess: %+v", got[4])
	}
}

func TestClassifyGHErrors(t *testing.T) {
	auth := classify("gh pr view", 4, "To get started with GitHub CLI, please run:  gh auth login\n")
	if !auth.fatal || !strings.Contains(auth.msg, "not signed in") {
		t.Fatalf("auth: %+v", auth)
	}
	if e := classify("gh pr view", 127, "bash: gh: command not found"); !e.fatal || !strings.Contains(e.msg, "not installed") {
		t.Fatalf("missing gh: %+v", e)
	}
	if e := classify("gh pr view", 1, "GraphQL: Could not resolve to a PullRequest with the number of 9."); !e.fatal {
		t.Fatalf("missing PR must be fatal: %+v", e)
	}
	if e := classify("gh pr view", 1, "error connecting to api.github.com"); e.fatal {
		t.Fatalf("a network blip must be retried: %+v", e)
	}
}

func TestPRURLHelpers(t *testing.T) {
	if !ValidPRURL("https://github.com/a/b/pull/12") || ValidPRURL("https://github.com/a/b/issues/12") {
		t.Fatal("ValidPRURL")
	}
	if got := repoArg("https://github.com/a/b/pull/12"); got != "a/b" {
		t.Fatalf("repoArg github.com: %q", got)
	}
	if got := repoArg("https://ghe.example.com/a/b/pull/12"); got != "ghe.example.com/a/b" {
		t.Fatalf("repoArg enterprise: %q", got)
	}
	out := "a pull request for branch \"lec/x\" into branch \"main\" already exists:\nhttps://github.com/a/b/pull/9\n"
	if got := FindPRURL(out); got != "https://github.com/a/b/pull/9" {
		t.Fatalf("FindPRURL: %q", got)
	}
}

func TestRedactMasksObviousSecrets(t *testing.T) {
	in := strings.Join([]string{
		"token ghp_abcdefghijklmnopqrstuvwxyz0123456789 leaked",
		"Authorization: Bearer abc.def.ghi-some-long-token",
		"export API_KEY=supersecretvalue123",
		`"password": "hunter22hunter"`,
		"clone https://user:s3cretpass@example.com/repo.git",
		"aws AKIA" + "ABCDEFGHIJKLMNOP here",
		"key sk-ant-api03-abcdefghijklmnopqrstuvwxyz",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----",
		"FAIL TestParser (0.01s): expected 3, got 4",
	}, "\n")
	out := Redact(in)
	for _, secret := range []string{"ghp_abcdefghij", "abc.def.ghi-some", "supersecretvalue123",
		"hunter22hunter", "s3cretpass", "AKIA" + "ABCDEFGHIJKLMNOP", "sk-ant-api03", "b3BlbnNzaC1rZXktdjEAAAAA"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q survived redaction:\n%s", secret, out)
		}
	}
	if !strings.Contains(out, "FAIL TestParser (0.01s): expected 3, got 4") {
		t.Errorf("an ordinary failure line must survive:\n%s", out)
	}
}

func TestTrimJobLogBoundsAndCleans(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		b.WriteString("test\tRun go test\t2026-09-26T10:00:00.1234567Z \x1b[31mline ")
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\x1b[0m\n")
	}
	b.WriteString("test\tRun go test\t2026-09-26T10:00:01.0000000Z --- FAIL: TestThing\n")
	b.WriteString("test\tRun go test\t2026-09-26T10:00:01.0000000Z ##[error]Process completed with exit code 1.\n")
	out := trimJobLog(b.String())
	if len(out) > jobTailBytes+64 {
		t.Fatalf("tail is %d bytes, over the %d bound", len(out), jobTailBytes)
	}
	if strings.Count(out, "\n") > jobTailLines+1 {
		t.Fatalf("tail has %d lines, over the %d bound", strings.Count(out, "\n"), jobTailLines)
	}
	if !strings.Contains(out, "Run go test | --- FAIL: TestThing") || !strings.HasSuffix(out, "exit code 1.") {
		t.Fatalf("the end of the log must survive, cleaned:\n%s", out[len(out)-300:])
	}
	if strings.Contains(out, "\x1b[") || strings.Contains(out, "2026-09-26T") {
		t.Fatalf("colour codes and timestamps must be stripped")
	}
	if !strings.HasPrefix(out, "… (earlier output trimmed)") {
		t.Fatalf("a cut log must say so")
	}
}

// Real `gh run view --log-failed` output: each step's first line carries a
// UTF-8 byte order mark in front of its timestamp.
func TestCleanLogStripsEachStepsByteOrderMark(t *testing.T) {
	raw := "test\tSet up job\t\ufeff2026-09-26T18:04:11.1528437Z Current runner version: '2.328.0'\n" +
		"test\tSet up job\t2026-09-26T18:04:11.1552213Z ##[group]Operating System\n" +
		"test\tSet up job\t2026-09-26T18:04:11.1553010Z Ubuntu\n" +
		"test\tSet up job\t2026-09-26T18:04:11.1554600Z ##[endgroup]\n" +
		"test\tRun go test ./...\t\ufeff2026-09-26T18:04:20.4406327Z ##[group]Run go test ./...\n" +
		"test\tRun go test ./...\t2026-09-26T18:04:20.4407118Z \x1b[36;1mgo test ./...\x1b[0m\n" +
		"test\tRun go test ./...\t2026-09-26T18:04:31.9730158Z --- FAIL: TestThing (0.00s)\n" +
		"test\tRun go test ./...\t2026-09-26T18:04:31.9771480Z ##[error]Process completed with exit code 1.\n"
	got := cleanLog(raw)
	want := "Set up job | Current runner version: '2.328.0'\n" +
		"Set up job | Ubuntu\n" +
		"Run go test ./... | go test ./...\n" +
		"Run go test ./... | --- FAIL: TestThing (0.00s)\n" +
		"Run go test ./... | ##[error]Process completed with exit code 1."
	if got != want {
		t.Fatalf("cleanLog:\n%q\nwant\n%q", got, want)
	}
}

func TestLabels(t *testing.T) {
	cases := []struct {
		state    string
		attempts int
		want     string
	}{
		{StatePending, 0, "CI running"},
		{StateFailing, 2, "CI failing — attempt 2/3"},
		{StatePending, 1, "CI running — attempt 1/3"},
		{StatePassed, 0, "CI passed"},
		{StatePassed, 2, "CI passed after 2 fixes"},
		{StateCapped, 3, "CI failing — gave up after 3/3"},
		{StateClosed, 1, "PR closed"},
	}
	for _, c := range cases {
		got := Label(&store.CIWatch{State: c.state, Attempts: c.attempts, MaxAttempts: 3})
		if got != c.want {
			t.Errorf("%s/%d: got %q want %q", c.state, c.attempts, got, c.want)
		}
	}
}
