package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

const reviewBody = "Saved to `/work/.scribe/plans/add-retry.md`\n\n" +
	"# Add retry\n\nRetry the fetch on 5xx.\n\n" +
	"## Steps\n\n" +
	"1. Wrap the fetch (`fetch.go`)\n" +
	"   - modify `Fetch` (func)\n" +
	"2. Test the retry path (`fetch_test.go`, `testdata/flaky.txt`)\n" +
	"3. Document the flag\n\n" +
	"## Verification\n\n- go test ./...\n"

func openPlanReview(t *testing.T) (*Model, chan PromptReply) {
	t.Helper()
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{
			Kind:          "plan_review",
			Question:      "Add retry",
			Body:          reviewBody,
			Options:       agent.PlanOptions(),
			AllowFreeText: true,
		},
		Reply: reply,
	}})
	settle(m)
	return m, reply
}

func TestPlanReviewShowsStepsWithFilesAndOptions(t *testing.T) {
	m, _ := openPlanReview(t)
	s := screen(m)
	for _, want := range []string{
		"≣ Add retry",
		"3 steps",
		"/work/.scribe/plans/add-retry.md",
		"○ Wrap the fetch",
		"○ Test the retry path",
		"○ Document the flag",
		"Symbols (1)",
		"~ Fetch (func)",
		"Verification",
		"✓ go test ./...",
		"1  ⏎ Implement",
		"2  ↻ Clear & Implement",
		"3  ✎ Revise",
		"4  ✗ Cancel",
		"v full plan",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("plan review lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "fetch_test.go") || strings.Contains(s, "modify `Fetch`") {
		t.Fatalf("step list should not repeat files or raw symbol lines:\n%s", s)
	}
}

func TestPlanReviewTwoColumnsAboveFiveSteps(t *testing.T) {
	body := "Saved to `/w/p.md`\n\n# Big\n\n## Steps\n\n" +
		"1. one\n2. two\n3. three\n4. four\n5. five\n6. six\n7. seven\n"
	m, _ := newTestModel(t, 100, 40)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Question: &tools.Question{Kind: "plan_review", Question: "Big", Body: body, Options: agent.PlanOptions(), AllowFreeText: true},
		Reply:    reply,
	}})
	settle(m)
	s := screen(m)
	// seven steps: the first four fill the left column, the last three the
	// right, so "one" pairs with "five" on the same row and "four" stands alone
	var pairs, alone string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "○ one") {
			pairs = line
		}
		if strings.Contains(line, "○ four") {
			alone = line
		}
	}
	// each frame row has two edge bars; a paired row has one more, the rule
	if !strings.Contains(pairs, "│ ○ five") || strings.Count(pairs, "│") != 3 || strings.Count(alone, "│") != 2 {
		t.Fatalf("seven steps should fill two columns split at four:\n%s", s)
	}
}

func TestPlanReviewReviseShowsPlaceholder(t *testing.T) {
	m, _ := openPlanReview(t)
	runCmd(m, send(m, "3"))
	if !strings.Contains(screen(m), "what should change...") {
		t.Fatalf("revise row lacks its placeholder:\n%s", screen(m))
	}
}

func TestPlanReviewDigitKeysAnswer(t *testing.T) {
	cases := map[string]string{
		"1": agent.AnswerExecute,
		"2": agent.AnswerClear,
		"4": agent.AnswerCancel,
	}
	for key, want := range cases {
		m, reply := openPlanReview(t)
		runCmd(m, send(m, key))
		select {
		case r := <-reply:
			if r.Answer != want {
				t.Fatalf("key %s answered %q, want %q", key, r.Answer, want)
			}
		default:
			t.Fatalf("key %s sent no reply", key)
		}
	}
}

func TestPlanReviewArrowsAndEnterChooseClear(t *testing.T) {
	m, reply := openPlanReview(t)
	send(m, "down")
	runCmd(m, send(m, "enter"))
	select {
	case r := <-reply:
		if r.Answer != agent.AnswerClear {
			t.Fatalf("down+enter answered %q, want clear", r.Answer)
		}
	default:
		t.Fatal("no reply")
	}
}

func TestPlanReviewReviseTakesFeedback(t *testing.T) {
	m, reply := openPlanReview(t)
	runCmd(m, send(m, "3"))
	if !strings.Contains(screen(m), "›") || !strings.Contains(screen(m), "what should change") {
		t.Fatalf("revise row did not open:\n%s", screen(m))
	}
	typeText(m, "add a jitter")
	runCmd(m, send(m, "enter"))
	select {
	case r := <-reply:
		if r.Answer != agent.AnswerRevise+" add a jitter" {
			t.Fatalf("revise answered %q", r.Answer)
		}
	default:
		t.Fatal("no reply")
	}
}

func TestPlanReviewVTogglesFullMarkdown(t *testing.T) {
	m, _ := openPlanReview(t)
	runCmd(m, send(m, "v"))
	s := screen(m)
	if !strings.Contains(s, "Retry the fetch on 5xx.") || !strings.Contains(s, "v steps") {
		t.Fatalf("full plan lacks its summary or the toggle hint:\n%s", s)
	}
	for i := 0; i < 3 && !strings.Contains(screen(m), "go test ./..."); i++ {
		runCmd(m, send(m, "pgdown"))
	}
	if s = screen(m); !strings.Contains(s, "Verification") || !strings.Contains(s, "go test ./...") {
		t.Fatalf("full plan does not reach the verification section:\n%s", s)
	}
	if strings.Contains(s, "3 steps") {
		t.Fatalf("full plan still shows the step list header:\n%s", s)
	}
	runCmd(m, send(m, "v"))
	if !strings.Contains(screen(m), "3 steps") {
		t.Fatalf("second v did not return to the step list:\n%s", screen(m))
	}
}

func TestPlanApprovalLeavesPlanModeInTheStatusBar(t *testing.T) {
	m, reply := openPlanReview(t)
	m.cur().mode = "plan"
	runCmd(m, send(m, "1"))
	<-reply
	if got := m.cur().mode; got != "default" {
		t.Fatalf("after Implement the tab mode is %q, want default", got)
	}
}

func TestPlanRevisionKeepsPlanMode(t *testing.T) {
	m, reply := openPlanReview(t)
	m.cur().mode = "plan"
	runCmd(m, send(m, "3"))
	runCmd(m, send(m, "x"))
	runCmd(m, send(m, "enter"))
	<-reply
	if got := m.cur().mode; got != "plan" {
		t.Fatalf("revision changed the mode to %q, want plan", got)
	}
}
