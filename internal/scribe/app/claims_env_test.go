package app

import "testing"

// The tools see other tabs' claims through Env.Claims, and never their own.
func TestEnvCarriesOtherTabsClaims(t *testing.T) {
	a := &App{claimOwner: "me", claimLabel: "tab one"}
	fileClaims.claim("/repo/mine.go", "me", "tab one")
	fileClaims.claim("/repo/theirs.go", "other", "tab two")
	t.Cleanup(func() { fileClaims.release("me"); fileClaims.release("other") })
	got := a.env().Claims.OtherClaims()
	if got["/repo/theirs.go"] != "tab two" || len(got) != 1 {
		t.Fatalf("other claims = %v", got)
	}
}
