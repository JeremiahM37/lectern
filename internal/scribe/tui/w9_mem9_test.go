package tui

import (
	"fmt"
	"testing"
)

func TestStashIsCappedAtFiftyNewestKept(t *testing.T) {
	s := &inputState{}
	for i := 0; i < stashCap+7; i++ {
		s.pushStash(fmt.Sprintf("draft %d", i))
	}
	if len(s.stashes) != stashCap {
		t.Fatalf("stashes = %d, want %d", len(s.stashes), stashCap)
	}
	if s.stashes[0] != "draft 7" || s.stashes[stashCap-1] != fmt.Sprintf("draft %d", stashCap+6) {
		t.Fatalf("kept %q .. %q; the oldest should go first", s.stashes[0], s.stashes[stashCap-1])
	}
}

// emergencyBackend records whether it was asked to save, and can panic.
type emergencyBackend struct {
	Backend
	saved, boom bool
}

func (e *emergencyBackend) EmergencySave() {
	if e.boom {
		panic("disk gone")
	}
	e.saved = true
}

func TestEmergencySaveAllContainsAPanickingBackend(t *testing.T) {
	bad := &emergencyBackend{boom: true}
	good := &emergencyBackend{}
	plain := struct{ Backend }{}
	EmergencySaveAll([]Backend{bad, plain, good})
	if !good.saved {
		t.Fatal("a backend after a panicking one was not saved")
	}
}
