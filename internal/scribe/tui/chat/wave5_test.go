package chat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// lineWith returns the index of the first line of out containing sub, or -1.
func lineWith(out, sub string) int {
	for i, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func countUsers(v *View) int {
	n := 0
	for _, r := range v.msgs {
		if r.Role == roleUser {
			n++
		}
	}
	return n
}

func TestPhoneMessageGetsOriginAndNoEcho(t *testing.T) {
	v, c := newView(t, Options{})
	// A local prompt: AddUser, then its turn echoes it back. One row only.
	v.AddUser("local hello", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "local hello", At: at(c)})
	if n := countUsers(v); n != 1 {
		t.Fatalf("local prompt echoed as %d user rows", n)
	}
	finish(v, c, time.Second)

	// A phone message starts a turn this view did not start: it gets its row,
	// with the marker removed and the origin kept.
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "[via telegram] check the logs", At: at(c)})
	finish(v, c, time.Second)
	if n := countUsers(v); n != 2 {
		t.Fatalf("phone prompt has no row: %d user rows", n)
	}
	last := v.msgs[len(v.msgs)-2]
	if last.Origin != "phone" || last.Text != "check the logs" {
		t.Fatalf("phone record = origin %q text %q", last.Origin, last.Text)
	}
	out := plain(v)
	if !strings.Contains(out, "· phone") || strings.Contains(out, "[via telegram]") {
		t.Errorf("phone row not shown cleanly:\n%s", out)
	}
	checkShape(t, v)
}

func TestQueuedLocalPromptsEchoOnceEach(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("first", 0, at(c), false)
	v.AddUser("second", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "first", At: at(c)})
	finish(v, c, time.Second)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "second", At: at(c)})
	finish(v, c, time.Second)
	if n := countUsers(v); n != 2 {
		t.Fatalf("queued prompts shown %d times, want 2", n)
	}
}

func TestPhoneSteeringKeepsOrigin(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("[via telegram] also rename it", 0, at(c), true)
	rec := v.msgs[len(v.msgs)-1]
	if !rec.Steering || rec.Origin != "phone" || rec.Text != "also rename it" {
		t.Fatalf("steering record = %+v", rec)
	}
}

func TestWheelAccelerates(t *testing.T) {
	v, c := newView(t, Options{})
	for i := 0; i < 120; i++ {
		v.AddUser(fmt.Sprintf("line %d", i), 0, at(c), false)
	}
	v.View()
	wheelUp := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	delta := func() int {
		before := v.top
		v.HandleMouse(wheelUp)
		return before - v.top
	}
	// A slow wheel scrolls the base step each tick.
	for i := 0; i < 3; i++ {
		c.advance(500 * time.Millisecond)
		if d := delta(); d != wheelBase {
			t.Fatalf("slow tick scrolled %d, want %d", d, wheelBase)
		}
	}
	// A quick burst speeds up tick by tick, up to the cap.
	var last int
	for i := 0; i < 6; i++ {
		c.advance(10 * time.Millisecond)
		last = delta()
	}
	if last != wheelMax {
		t.Fatalf("burst settled at %d lines a tick, want %d", last, wheelMax)
	}
	// Waiting lets it settle back to the base step.
	c.advance(2 * time.Second)
	if d := delta(); d != wheelBase {
		t.Fatalf("after a pause the tick scrolled %d, want %d", d, wheelBase)
	}
}

func TestEditDiffShowsBothLineNumbers(t *testing.T) {
	v, c := newView(t, Options{})
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n"
	tool(v, "e1", "edit_file", map[string]any{"path": "a.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "ok", Diff: diff}, time.Second, c)
	v.ToggleExpand()
	out := plain(v)
	// Removed line 2 has an old number only; added line 2 a new number only.
	if !strings.Contains(out, "2   - two") {
		t.Errorf("removed line lacks its old number:\n%s", out)
	}
	if !strings.Contains(out, "  2 + TWO") {
		t.Errorf("added line lacks its new number:\n%s", out)
	}
	if !strings.Contains(out, "1 1   one") && !strings.Contains(out, "1 1  one") {
		t.Errorf("context line lacks both numbers:\n%s", out)
	}
}

func TestSideBySideDiffPutsOldAndNewOnOneRow(t *testing.T) {
	v, c := newView(t, Options{})
	v.SetDiffView(DiffSideBySide, false)
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,1 @@\n-two\n+TWO\n"
	tool(v, "e1", "edit_file", map[string]any{"path": "a.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "ok", Diff: diff}, time.Second, c)
	v.ToggleExpand()
	found := false
	for _, l := range strings.Split(plain(v), "\n") {
		if strings.Contains(l, "two") && strings.Contains(l, "TWO") {
			found = true
		}
	}
	if !found {
		t.Errorf("side-by-side diff did not pair the lines:\n%s", plain(v))
	}
}

func TestCompactEditIsOneLineUntilExpanded(t *testing.T) {
	v, c := newView(t, Options{CollapseDiffs: true})
	diff := "--- a/a.go\n+++ b/a.go\n@@ -1,1 +1,2 @@\n-two\n+TWO\n+three\n"
	tool(v, "e1", "edit_file", map[string]any{"path": "a.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "ok", Diff: diff}, time.Second, c)
	out := plain(v)
	if !strings.Contains(out, "a.go +2 -1") {
		t.Errorf("compact edit line missing:\n%s", out)
	}
	if strings.Contains(out, "TWO") {
		t.Errorf("compact edit shows its diff body:\n%s", out)
	}
	v.ToggleExpand()
	if !strings.Contains(plain(v), "TWO") {
		t.Errorf("expanding a compact edit did not show the diff")
	}
}

func TestStripLineNumbers(t *testing.T) {
	bare, nums := stripLineNumbers([]string{"     1  package x", "     2  ", "     3  func f()"})
	if bare[0] != "package x" || bare[1] != "" || bare[2] != "func f()" {
		t.Errorf("bare = %q", bare)
	}
	if nums[0] != 1 || nums[1] != 2 || nums[2] != 3 {
		t.Errorf("nums = %v", nums)
	}
	// Fewer than half the lines numbered: the text is left alone.
	bare, nums = stripLineNumbers([]string{"abc", "d", "     1  x"})
	if bare[2] != "     1  x" || nums[2] != 0 {
		t.Errorf("mostly unnumbered output was stripped: %q %v", bare, nums)
	}
}

func TestReadDetailShowsFileLineNumbers(t *testing.T) {
	v, c := newView(t, Options{})
	raw := "     1  package x\n     2  func f() {}\n"
	tool(v, "r1", "read", map[string]any{"path": "x.go"}, c)
	result(v, "r1", "read", tools.Result{Output: raw}, time.Second, c)
	v.ToggleExpand()
	out := plain(v)
	if strings.Contains(out, "     1  package") {
		t.Errorf("read detail still shows the raw gutter:\n%s", out)
	}
	if !strings.Contains(out, "package x") || !strings.Contains(out, "func f()") {
		t.Errorf("read detail lost its text:\n%s", out)
	}
}

func TestLargeReadIsPlain(t *testing.T) {
	v, c := newView(t, Options{})
	big := strings.Repeat("x = 1\n", 12000) // over readPlainLimit
	tool(v, "r1", "read", map[string]any{"path": "big.go"}, c)
	result(v, "r1", "read", tools.Result{Output: big}, time.Second, c)
	v.ToggleExpand()
	if !strings.Contains(plain(v), "x = 1") {
		t.Errorf("large read shows nothing")
	}
	if got := v.readDetail(v.calls["r1"], "big.go"); len(got) != readDetailRows {
		t.Errorf("large read rows = %d, want %d", len(got), readDetailRows)
	}
}

func TestSearchBatchCoversSymbolTools(t *testing.T) {
	v, c := newView(t, Options{})
	tool(v, "s1", "find_symbol", map[string]any{"name": "Parse"}, c)
	tool(v, "s2", "symbol_refs", map[string]any{"symbol": "Parse"}, c)
	tool(v, "s3", "grep", map[string]any{"pattern": "Parse"}, c)
	for _, id := range []string{"s1", "s2", "s3"} {
		result(v, id, "x", tools.Result{Output: "ok"}, time.Second, c)
	}
	if !strings.Contains(plain(v), "3 searches") {
		t.Errorf("symbol lookups did not batch with grep:\n%s", plain(v))
	}
}

func TestRailFoldClickUnfolds(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("c%d", i)
		tool(v, id, "shell", map[string]any{"command": fmt.Sprintf("echo %d", i)}, c)
		result(v, id, "shell", tools.Result{Output: "ok"}, time.Second, c)
	}
	text(v, "done", c)
	finish(v, c, time.Second)
	out := plain(v)
	if !strings.Contains(out, "+2 completed") || !strings.Contains(out, "(click to expand)") {
		t.Fatalf("rail fold text wrong:\n%s", out)
	}
	row := lineWith(out, "(click to expand)")
	clickCell(v, 0, row)
	if !v.msgs[len(v.msgs)-1].RailOpen && !v.msgs[0].RailOpen {
		t.Fatalf("click did not open the rail fold")
	}
	out = plain(v)
	if !strings.Contains(out, "collapse +2 above") {
		t.Errorf("unfolded rail has no collapse row:\n%s", out)
	}
	row = lineWith(out, "collapse +2 above")
	clickCell(v, 0, row)
	if strings.Contains(plain(v), "collapse +2 above") {
		t.Errorf("second click did not fold the rail again")
	}
}

func TestDispatchSeedsShowPendingRowsUntilReplaced(t *testing.T) {
	v, c := newView(t, Options{})
	tool(v, "d1", "dispatch", map[string]any{"tasks": []any{
		map[string]any{"task": "map the sessions store", "role": "explore"},
		map[string]any{"task": "fix the parser", "role": "code"},
	}}, c)
	out := plain(v)
	for _, want := range []string{"map the sessions store", "fix the parser"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pending seed %q not shown:\n%s", want, out)
		}
	}
	// The first subagent starts under a new id with the same task: its real
	// row replaces the seed rather than adding one.
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d1", At: at(c), Agent: &agent.AgentInfo{
		ID: "a1", Task: "map the sessions store", Role: "explore", Status: "running"}})
	if n := len(v.calls["d1"].Agents); n != 2 {
		t.Fatalf("real agent added a row instead of replacing its seed: %d rows", n)
	}
	if v.calls["d1"].Agents[0].ID != "a1" {
		t.Errorf("real agent is not in the seed's place")
	}
	// When the dispatch ends, seeds no subagent took are gone.
	result(v, "d1", "dispatch", tools.Result{Output: "ok"}, time.Second, c)
	if n := len(v.calls["d1"].Agents); n != 1 {
		t.Errorf("unstarted seed outlived its dispatch: %d rows", n)
	}
}

func TestDispatchWindowFoldsOlderRows(t *testing.T) {
	v, c := newView(t, Options{})
	var tasks []any
	for i := 0; i < 8; i++ {
		tasks = append(tasks, map[string]any{"task": fmt.Sprintf("task number %d", i), "role": "explore"})
	}
	tool(v, "d1", "dispatch", map[string]any{"tasks": tasks}, c)
	out := plain(v)
	if !strings.Contains(out, "+2 earlier") {
		t.Errorf("long dispatch has no window fold:\n%s", out)
	}
	// The dispatch header names the first task too, so count the agent rows.
	if strings.Contains(out, "explore  task number 0") {
		t.Errorf("oldest row still shown inside the window")
	}
	if n := strings.Count(out, "explore  task number"); n != dispatchWindow {
		t.Errorf("window shows %d rows, want %d", n, dispatchWindow)
	}
	if !strings.Contains(out, "explore  task number 7") {
		t.Errorf("newest row missing from the window")
	}
}

func TestSeedDecodeFailureIsHarmless(t *testing.T) {
	c := &Call{ID: "x", Args: json.RawMessage(`{"tasks": "nope"}`)}
	seedAgents(c)
	if len(c.Agents) != 0 {
		t.Errorf("bad args seeded %d rows", len(c.Agents))
	}
}

func planTurn(v *View, c *clock, p *agent.Plan) {
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: p, At: at(c)})
}

func TestPlanBoxFoldsWhenFinishedAndOpensOnClick(t *testing.T) {
	v, c := newView(t, Options{})
	p := &agent.Plan{
		Title: "Ship sessions", Path: ".scribe/plans/ship.md",
		Steps: []agent.PlanStep{
			{ID: "1", Text: "Write the store", Status: "done", Files: []string{"store.go"}},
			{ID: "2", Text: "Wire the tab", Status: "active", Files: []string{"tab.go"}},
		},
		Verification: []string{"go test passes"},
	}
	planTurn(v, c, p)
	out := plain(v)
	if !strings.Contains(out, "╭") || !strings.Contains(out, "Ship sessions") {
		t.Fatalf("plan is not a box:\n%s", out)
	}
	// Every step finished: one folded line with the counts, before any click.
	p.Steps[1].Status = "done"
	v.Apply(agent.Event{Kind: agent.EvPlan, Plan: p, At: at(c)})
	finish(v, c, time.Second)
	out = plain(v)
	if !strings.Contains(out, "Ship sessions (2 files, 2 steps)") || strings.Contains(out, "Verification") {
		t.Fatalf("finished plan did not fold:\n%s", out)
	}
	row := lineWith(out, "Ship sessions (2 files")
	clickCell(v, 0, row)
	out = plain(v)
	if !strings.Contains(out, "Verification") || !strings.Contains(out, "go test passes") || !strings.Contains(out, "Files (2)") {
		t.Errorf("clicked plan did not open its sections:\n%s", out)
	}
}

func TestPlanBoxCountsSkippedAsFinished(t *testing.T) {
	v, c := newView(t, Options{})
	planTurn(v, c, &agent.Plan{Title: "Small", Steps: []agent.PlanStep{
		{ID: "1", Text: "one", Status: "done"},
		{ID: "2", Text: "two", Status: "skipped"},
	}})
	finish(v, c, time.Second)
	if !strings.Contains(plain(v), "Small (0 files, 2 steps)") {
		t.Errorf("skipped step kept the plan open:\n%s", plain(v))
	}
}

func TestTaskBoxHidesThreeSecondsAfterAllDone(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvTasks, At: at(c), Tasks: []agent.Task{
		{ID: "1", Text: "write tests", Status: "done"},
		{ID: "2", Text: "ship it", Status: "in_progress"},
	}})
	text(v, "working", c)
	if !strings.Contains(plain(v), "Tasks") {
		t.Fatalf("open task list hidden")
	}
	v.Apply(agent.Event{Kind: agent.EvTasks, At: at(c), Tasks: []agent.Task{
		{ID: "1", Text: "write tests", Status: "done"},
		{ID: "2", Text: "ship it", Status: "done"},
	}})
	finish(v, c, time.Second)
	c.advance(2 * time.Second)
	if !strings.Contains(plain(v), "2/2") {
		t.Errorf("finished task list hid before 3 s:\n%s", plain(v))
	}
	c.advance(2 * time.Second)
	if strings.Contains(plain(v), "Tasks") {
		t.Errorf("finished task list still shown after 3 s:\n%s", plain(v))
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: uint8(x * 40), G: uint8(y * 90), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAttachedImageShowsHalfBlocks(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("what is this", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 4, 4)}})
	out := plain(v)
	if !strings.Contains(out, "image 1 4×4") {
		t.Errorf("image header missing:\n%s", out)
	}
	if !strings.Contains(out, "▀") {
		t.Errorf("image has no half-block rows:\n%s", out)
	}
	checkShape(t, v)
}

func TestUnreadableImageGetsAFallbackLine(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("look", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: []byte("not a picture")}})
	if !strings.Contains(plain(v), "could not show this image") {
		t.Errorf("unreadable image has no fallback:\n%s", plain(v))
	}
}

func TestAttachImagesNeedsAUserMessage(t *testing.T) {
	v, _ := newView(t, Options{})
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 2, 2)}})
	if len(v.msgs) != 0 {
		t.Errorf("image attached to nothing created a record")
	}
}

func TestRejectedDispatchShowsReason(t *testing.T) {
	v, c := newView(t, Options{})
	tool(v, "d1", "dispatch", map[string]any{"tasks": []any{map[string]any{"task": "rm -rf", "role": "code"}}}, c)
	result(v, "d1", "dispatch", tools.Result{Output: "rejected — the role is not allowed here", IsError: true}, time.Second, c)
	if !strings.Contains(plain(v), "→ rejected — the role is not allowed here") {
		t.Errorf("rejected dispatch has no reason:\n%s", plain(v))
	}
}

func TestImageBytesKeepsOnlyImageParts(t *testing.T) {
	png := pngBytes(t, 2, 2)
	got := imageBytes([]llm.Part{{Type: llm.PartText, Text: "x"}, {Type: llm.PartImage, Data: png}, {Type: llm.PartImage}})
	if len(got) != 1 || !bytes.Equal(got[0], png) {
		t.Errorf("imageBytes = %d images", len(got))
	}
}
