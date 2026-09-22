package config

import (
	"os"
	"testing"
)

func TestLegacyEnvIsReadUnderTheNewName(t *testing.T) {
	t.Setenv("AGENTDECK_TEST_ALIAS", "from-old")
	os.Unsetenv("LECTERN_TEST_ALIAS")
	t.Setenv("AGENTDECK_TEST_KEPT", "old")
	t.Setenv("LECTERN_TEST_KEPT", "new")
	aliased := AliasLegacyEnv()
	if got := os.Getenv("LECTERN_TEST_ALIAS"); got != "from-old" {
		t.Fatalf("LECTERN_TEST_ALIAS = %q", got)
	}
	// A value set under the new name is never overridden by the old one.
	if got := os.Getenv("LECTERN_TEST_KEPT"); got != "new" {
		t.Fatalf("LECTERN_TEST_KEPT = %q", got)
	}
	seen := map[string]bool{}
	for _, n := range aliased {
		seen[n] = true
	}
	if !seen["AGENTDECK_TEST_ALIAS"] || seen["AGENTDECK_TEST_KEPT"] {
		t.Fatalf("aliased = %v", aliased)
	}
}
