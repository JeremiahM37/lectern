package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

func TestForceClaimReportsDisplacedHolder(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := newClaimRegistry(func() time.Time { return clock }, claimTTL)
	reg.claim("/w/a.go", "tabA", "Scribe tab A")
	prev, displaced := reg.forceClaim("/w/a.go", "tabB", "Scribe tab B")
	if !displaced || prev.label != "Scribe tab A" {
		t.Fatalf("force should report tab A as displaced, got %+v %v", prev, displaced)
	}
	if _, displaced := reg.forceClaim("/w/a.go", "tabB", "Scribe tab B"); displaced {
		t.Fatal("forcing one's own claim displaces nobody")
	}
}

func TestReleaseOnlyDropsOwnClaims(t *testing.T) {
	reg := newClaimRegistry(time.Now, claimTTL)
	reg.claim("/w/a.go", "tabA", "A")
	if reg.releaseFile("/w/a.go", "tabB") {
		t.Fatal("another tab must not release the claim")
	}
	if !reg.releaseFile("/w/a.go", "tabA") {
		t.Fatal("the owner should release its claim")
	}
}

func TestIdleReleaseWaitsForNewTurnAndDispatch(t *testing.T) {
	reg := newClaimRegistry(time.Now, claimTTL)
	reg.claim("/w/a.go", "tabA", "A")
	gen := reg.turnGeneration("tabA")
	reg.turnStarted("tabA") // a new turn cancels the pending release
	if reg.releaseIfIdle("tabA", gen) {
		t.Fatal("a new turn must keep the claims")
	}
	gen = reg.turnGeneration("tabA")
	reg.dispatchStarted("tabA", "A")
	if reg.releaseIfIdle("tabA", gen) {
		t.Fatal("a running dispatch must keep the claims")
	}
	reg.dispatchEnded("tabA")
	if !reg.releaseIfIdle("tabA", gen) {
		t.Fatal("an idle tab should release its claims")
	}
	if len(reg.snapshot("tabA")) != 0 {
		t.Fatal("claims should be gone after the idle release")
	}
}

func TestClaimsGoStaleAfterFiveMinutes(t *testing.T) {
	clock := time.Unix(1000, 0)
	reg := newClaimRegistry(func() time.Time { return clock }, claimTTL)
	reg.claim("/w/a.go", "tabA", "A")
	clock = clock.Add(5*time.Minute + time.Second)
	if _, held := reg.heldByOther("/w/a.go", "tabB"); held {
		t.Fatal("a claim older than five minutes should be stale")
	}
}

func TestGitBlockedWhileOtherTabDispatches(t *testing.T) {
	reg := newClaimRegistry(time.Now, claimTTL)
	reg.dispatchStarted("tabA", "Scribe session tabA")
	commit := json.RawMessage(`{"action":"commit","message":"x"}`)
	mine := claimHooks{reg: reg, owner: "tabA", workdir: "/w"}
	theirs := claimHooks{reg: reg, owner: "tabB", label: "B", workdir: "/w"}
	if got := theirs.claimBlock("git", commit); !strings.HasPrefix(got, "BLOCKED:") {
		t.Fatalf("tab B's commit should be refused while tab A dispatches, got %q", got)
	}
	if got := mine.claimBlock("git", commit); got != "" {
		t.Fatalf("a tab's own dispatch does not block its own git, got %q", got)
	}
	status := json.RawMessage(`{"action":"status"}`)
	if got := theirs.claimBlock("git", status); got != "" {
		t.Fatalf("read-only git is never refused, got %q", got)
	}
}

func TestContentionWordingForClaimedFile(t *testing.T) {
	reg := newClaimRegistry(time.Now, claimTTL)
	reg.claim("/w/a.go", "tabA", "Scribe session tabA")
	h := claimHooks{reg: reg, owner: "tabB", label: "B", workdir: "/w"}
	got := h.claimBlock("edit_file", json.RawMessage(`{"path":"a.go"}`))
	if !strings.HasPrefix(got, "CONTENTION: File a.go was modified by Scribe session tabA") {
		t.Fatalf("contention wording = %q", got)
	}
}

func TestDispatchSubagentHooksDoNotStartTurnClaims(t *testing.T) {
	reg := newClaimRegistry(time.Now, claimTTL)
	sub := claimHooks{reg: reg, owner: "tabA", label: "A", workdir: "/w", dispatch: true}
	sub.Prompt(nil, "task")
	if _, busy := reg.otherDispatch("tabB"); !busy {
		t.Fatal("a dispatch subagent should count as dispatch activity")
	}
	sub.Stop(nil, agent.TurnStats{})
	if _, busy := reg.otherDispatch("tabB"); busy {
		t.Fatal("the subagent's Stop should end its dispatch activity")
	}
}
