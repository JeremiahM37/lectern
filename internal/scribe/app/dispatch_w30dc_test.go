package app

import (
	"path/filepath"
	"testing"
)

// The dispatch planning check reads other tabs' claims through tabClaims. A
// tab must see the files other tabs hold, labelled by tab, and never its own.
func TestW30DCTabClaimsListsOtherTabsOnly(t *testing.T) {
	dir := t.TempDir()
	mine := &App{claimOwner: "w30dc-mine", claimLabel: "Scribe session mine", Workdir: dir}
	theirs := "w30dc-theirs"
	t.Cleanup(func() {
		fileClaims.release(mine.claimOwner)
		fileClaims.release(theirs)
	})

	otherFile := filepath.Join(dir, "pkg", "a.go")
	ownFile := filepath.Join(dir, "pkg", "b.go")
	fileClaims.claim(otherFile, theirs, "Scribe session 99aa00bb")
	fileClaims.claim(ownFile, mine.claimOwner, mine.claimLabel)

	got := tabClaims{mine}.OtherClaims()
	if label, ok := got[otherFile]; !ok || label != "Scribe session 99aa00bb" {
		t.Fatalf("other tab's claim = %q, %v; want its label", label, ok)
	}
	if _, ok := got[ownFile]; ok {
		t.Fatalf("own claim reported as another tab's: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("OtherClaims = %v, want only the other tab's file", got)
	}
}
