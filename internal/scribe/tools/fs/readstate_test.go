package fs

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestReadStateMergesTouchingSpans(t *testing.T) {
	st := newReadState()
	const abs, h = "/p/a.txt", "aaaaaaaaaaaa"
	st.note(abs, h, 1, 2, false)
	st.note(abs, h, 4, 5, false)
	// 3 joins the two spans into 1-5.
	st.note(abs, h, 3, 3, false)
	if !st.note(abs, h, 1, 5, false) {
		t.Fatal("1-5 was delivered in pieces; it should now be unchanged")
	}
	if got := st.files[abs].spans; len(got) != 1 || got[0] != [2]int{1, 5} {
		t.Fatalf("spans = %v, want one span 1-5", got)
	}
}

func TestReadStateGapIsNotCovered(t *testing.T) {
	st := newReadState()
	const abs, h = "/p/a.txt", "aaaaaaaaaaaa"
	st.note(abs, h, 1, 2, false)
	st.note(abs, h, 5, 6, false)
	if st.note(abs, h, 1, 6, false) {
		t.Fatal("lines 3-4 were never sent, so 1-6 is not unchanged")
	}
}

func TestReadStateNewHashStartsOver(t *testing.T) {
	st := newReadState()
	const abs = "/p/a.txt"
	st.note(abs, "old", 1, 10, false)
	if st.note(abs, "new", 1, 10, false) {
		t.Fatal("a changed file must not be reported unchanged")
	}
	if !st.note(abs, "new", 1, 10, false) {
		t.Fatal("the second read of the new content should be unchanged")
	}
}

func TestReadStateForceReportsChangedButStillRecords(t *testing.T) {
	st := newReadState()
	const abs, h = "/p/a.txt", "aaaaaaaaaaaa"
	st.note(abs, h, 1, 3, false)
	if st.note(abs, h, 1, 3, true) {
		t.Fatal("force must send the content, not the notice")
	}
	if !st.note(abs, h, 2, 2, false) {
		t.Fatal("force still records what was sent")
	}
}

func TestShortHashIsTwelveHex(t *testing.T) {
	h := shortHash("x")
	if len(h) != 12 {
		t.Fatalf("len = %d", len(h))
	}
	if h != shortHash("x") || h == shortHash("y") {
		t.Fatal("hash must be stable and distinguish text")
	}
}

func TestReadStateIsPerScope(t *testing.T) {
	st := newReadState()
	parent := &tools.Env{Scope: "p#0"}
	sub := &tools.Env{Scope: "s#0"}
	h := shortHash("x")
	st.note(readKey(parent, "/a.go"), h, 1, 5, false)
	if st.note(readKey(sub, "/a.go"), h, 1, 5, false) {
		t.Fatal("a subagent was told it already had a file only its parent read")
	}
	compacted := &tools.Env{Scope: "p#1"}
	if st.note(readKey(compacted, "/a.go"), h, 1, 5, false) {
		t.Fatal("after compaction the read was still reported as unchanged")
	}
	if !st.note(readKey(parent, "/a.go"), h, 1, 5, false) {
		t.Fatal("same scope re-read should be unchanged")
	}
}
