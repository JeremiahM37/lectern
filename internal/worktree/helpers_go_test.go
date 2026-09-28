package worktree

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/helpers"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// This test binary doubles as `lectern helper` for the Go path, and
// LECTERN_WORKTREE_GO_HELPERS=1 runs every test here against the Go helpers
// instead of the Python scripts.
func TestMain(m *testing.M) {
	if os.Getenv("LECTERN_HELPER_TEST_EXEC") == "1" {
		args := os.Args[1:]
		if len(args) > 0 && args[0] == "helper" {
			args = args[1:]
		}
		os.Exit(helpers.Main(args))
	}
	if os.Getenv("LECTERN_WORKTREE_GO_HELPERS") == "1" {
		useGoHelpers()
	}
	os.Exit(m.Run())
}

func useGoHelpers() {
	os.Setenv("LECTERN_HELPER_TEST_EXEC", "1")
	helperCommand = func(_ executor.Executor, name string, args []string, _ string) string {
		return helpers.Invocation(os.Args[0], name, args)
	}
}

// withGoHelpers runs a test body with the Go helpers.
func withGoHelpers(t *testing.T, body func(*testing.T)) {
	saved := helperCommand
	t.Cleanup(func() { helperCommand = saved })
	t.Setenv("LECTERN_HELPER_TEST_EXEC", "1")
	helperCommand = func(_ executor.Executor, name string, args []string, _ string) string {
		return helpers.Invocation(os.Args[0], name, args)
	}
	body(t)
}

func TestInteractiveCommandUsesGoHelperWhenTargetHasLectern(t *testing.T) {
	saved := helperCommand
	t.Cleanup(func() { helperCommand = saved })
	helperCommand = helpers.Command
	single := &Interactive{Repo: "/r", Path: "/w/s", Branch: "b", Base: "HEAD", Token: strings.Repeat("a", 32), State: "creating"}
	group := &Interactive{Repo: "/r", Path: "/w/g", Branch: "b", Base: "HEAD", Token: strings.Repeat("b", 32),
		Repositories: []WorkspaceRepository{{Name: "r", Worktree: single}}}
	cases := []struct {
		plan         *Interactive
		action, want string
	}{
		{single, "create", "/opt/lectern helper worktree create "},
		{single, "cancel", "/opt/lectern helper worktree-cancel cancel "},
		{group, "check-create", "/opt/lectern helper worktree-preflight check-create "},
		{group, "remove", "/opt/lectern helper worktree-group remove "},
	}
	for _, c := range cases {
		plain := executor.NewMock(0)
		RunInteractive(context.Background(), plain, c.action, c.plan)
		withLectern := executor.NewMock(0)
		executor.SetTargetEnv(withLectern, executor.TargetEnv{Lectern: "/opt/lectern"})
		RunInteractive(context.Background(), withLectern, c.action, c.plan)
		py, goCmd := plain.CmdLog()[0], withLectern.CmdLog()[0]
		if !strings.HasPrefix(py, "python3 -c ") {
			t.Fatalf("%s without lectern: %q", c.action, py)
		}
		if !strings.HasPrefix(goCmd, c.want) || strings.Contains(goCmd, "python3") {
			t.Fatalf("%s with lectern: %q", c.action, goCmd)
		}
		switch {
		case c.action == "create":
			if !strings.HasSuffix(goCmd, " - 90") {
				t.Fatalf("single timeout: %q", goCmd)
			}
		case c.action == "remove":
			if !strings.HasSuffix(goCmd, " 105") {
				t.Fatalf("group timeout: %q", goCmd)
			}
		}
	}
}

// The real scenarios, run again against the Go helpers.
func TestGoHelperScenarios(t *testing.T) {
	for name, test := range map[string]func(*testing.T){
		"FailedCheckoutHook":           TestFailedCheckoutHookRetainsRecoverableOwnedWorktree,
		"MultiPreflight":               TestMultiWorkspaceTargetPreflightPreservesRepositories,
		"CancelSetup":                  TestCancelSetupStopsOwnedCheckoutAndRetainsFiles,
		"ExtendPreservesDirtyFiles":    TestExtendWorkspacePreservesDirtyFilesAndRejectsStaleCleanup,
		"ExtensionRejectsAlias":        TestExtensionPreflightRejectsAliasWithoutMutatingRoot,
		"ExtensionFailureDiscoverable": TestExtensionFailureRemainsDiscoverableFromOldRecord,
		"ReusedProcessReceipt":         TestMultiWorkspaceIgnoresReusedProcessReceipt,
		"LegacyReceipt":                TestMultiWorkspaceLegacyReceiptGuardsLiveProcessGroups,
		"LockProbeOverlap":             TestMultiWorkspaceLockProbeOverlapRetriesWithoutOverlapping,
		"SupervisorDeath":              TestMultiWorkspaceSupervisorDeathKeepsCheckoutGuarded,
		"OrphanCancellation":           TestMultiWorkspaceCancellationReachesOrphanedCheckout,
		"PartialRecovery":              TestPartialRecoveryValidatesWithoutDiscardingChanges,
		"SingleOrphanRecovery":         TestSingleCheckoutOrphanCanBeCancelledAndRecovered,
		"SetupCommands":                TestWorkspaceSetupCommandsRunInEachCheckout,
		"CancelSetupCommand":           TestCancelWorkspaceSetupCommandRetainsFiles,
		"SetupOutlivesSupervisor":      TestSetupCommandOutlivesSupervisorWithoutRerunning,
		"SetupTimeout":                 TestWorkspaceSetupTimeoutStopsFurtherWrites,
	} {
		t.Run(name, func(t *testing.T) { withGoHelpers(t, test) })
	}
}

// The scenarios that need the isolated runner (they use tmux), on the Go
// helpers.
func TestGoHelperIsolatedScenarios(t *testing.T) {
	testutil.RequireIsolated(t)
	for name, test := range map[string]func(*testing.T){
		"IsolationOwnershipRemoval":  TestInteractiveIsolationOwnershipAndSafeRemoval,
		"MultiCreationFailure":       TestMultiWorkspaceCreationFailureAndCleanup,
		"ExtensionCancellationTerms": TestExtensionCancellationDoesNotStopExistingTerminal,
	} {
		t.Run(name, func(t *testing.T) { withGoHelpers(t, test) })
	}
}
