package delegation

import (
	"fmt"
	"strings"
)

// LeadLabel marks a task as orchestrated: its attempt runs the lead, not a
// worker. The scheduler reads it to attach the Lectern MCP server, and the
// board shows it as a chip.
const LeadLabel = "orchestrated"

// MCPServerName is the name the injected server is registered under for the
// lead. The guide and the tools refer to it by this name.
const MCPServerName = "lectern"

// IsLead reports whether a task with these labels is an orchestrated one.
func IsLead(labels []string) bool {
	for _, l := range labels {
		if l == LeadLabel {
			return true
		}
	}
	return false
}

// LeadMCP is the MCP server entry the scheduler adds to an orchestrated
// attempt so the lead can call delegate_build without the operator having
// registered anything: Lectern's own binary in MCP mode, pointed at the
// server that launched the lead. The auth token is passed only when the
// server has one; an open server needs none.
//
// Codex times a tool call out after 60 seconds by default, and a build wait
// is one call of up to 55 minutes, so the Codex entry carries the timeout.
// Claude Code reads MCP_TOOL_TIMEOUT from the environment instead
// (LeadEnv).
func LeadMCP(agent, executable, baseURL, authToken string) map[string]any {
	env := map[string]any{"LECTERN_API": baseURL}
	if authToken != "" {
		env["LECTERN_AUTH_TOKEN"] = authToken
	}
	entry := map[string]any{"command": executable, "args": []any{"mcp"}, "env": env}
	if agent == "codex" {
		// The map is shaped like decoded JSON (numbers are float64) because
		// that is what the Codex -c encoder accepts.
		entry["tool_timeout_sec"] = float64(LeadToolTimeoutSeconds)
	}
	return map[string]any{MCPServerName: entry}
}

// LeadToolTimeoutSeconds bounds one MCP call from the lead. delegate_build
// waits up to 55 minutes inside a single call.
const LeadToolTimeoutSeconds = 3600

// LeadEnv is the environment an orchestrated attempt gets on top of the
// project's. Claude Code's MCP tool timeout is an environment variable in
// milliseconds.
func LeadEnv(agent string) map[string]string {
	if agent == "claude" {
		return map[string]string{"MCP_TOOL_TIMEOUT": fmt.Sprintf("%d", LeadToolTimeoutSeconds*1000)}
	}
	return nil
}

// LeadPrompt is the whole instruction an orchestrated task's lead receives:
// the operator's description of the work, framed by the delegated-build
// workflow it must run. It names the project so delegate_build never has to
// guess it from a worktree path, and it tells the lead to integrate into its
// own checkout, so the finished work is the task's own diff and is reviewed
// on the board like any other task.
func LeadPrompt(project, description string, correctionCycles int) string {
	if correctionCycles < 0 {
		correctionCycles = 0
	}
	guide := strings.ReplaceAll(strings.TrimSpace(leadGuide), "{project}", project)
	guide = strings.ReplaceAll(guide, "{cycles}", fmt.Sprintf("%d", correctionCycles))
	return guide + "\n\n---\n\nREQUEST:\n\n" + strings.TrimSpace(description) + "\n"
}

const leadGuide = `
You are the LEAD of an orchestrated build in Lectern for the project "{project}".
Your current working directory is your own checkout of it (a task worktree on a
fresh branch). You plan the work and review the result; a cheaper worker agent
does the implementation as a separate Lectern task in its own worktree. The
tools are on the "lectern" MCP server: delegate_build, wait_build, task_diff,
request_changes and accept_build. If a "lectern-delegate" skill is available,
its references apply; this prompt is sufficient without it.

This request authorizes the whole workflow. Do not ask for confirmation
between phases; make product decisions the repository cannot answer
conservatively and record them in your report.

Keep for yourself what your judgment is worth paying for: scope, design,
acceptance criteria, material risk and the final review. Hand the volume of
the implementation, and the discovery, testing and debugging it needs, to the
worker. Architecture, auth, tenancy, payments, secrets and production impact
are your decisions; never hand them to the worker under a vague "build it".

1. ORIENT. Read the repository guidance (README, CLAUDE.md, AGENTS.md,
   contributing docs) and the code the request touches. If the request is a
   one-line change or a typo, do it yourself and skip to step 6.

2. DESIGN. Write down, briefly: objective, non-goals, evidence from the
   repository, interfaces and contracts, failure behaviour, risks, acceptance
   criteria, and the verification commands that prove them.

3. BRIEF. One brief is one coherent end-to-end bundle: exact contracts, a
   bounded file scope, testable outcomes and the verification commands by
   name. Do not write the implementation into the brief. Split only at a real
   dependency or independent-acceptance boundary, never for visibility.

4. DELEGATE. Call delegate_build with project "{project}", a short title and
   the brief. It creates the worker task, starts it and waits, then returns the
   worker's report, its branch, diff stats and (when small) the patch under
   "diff". If it returns done:false it timed out; call wait_build with the task
   id. One wait, not polling: do not call task_status for progress, and do not
   edit the repository while the worker owns the bundle. One worker at a time
   unless your design named independent bundles.

5. REVIEW. Read the patch (task_diff when it was too large to inline) and the
   report against the brief, with two lenses: does it do what was specified,
   and would you merge it (correctness, tests, security, no weakened checks,
   no undeclared dependencies). Worker completion means ready for review, not
   accepted. For defects, call request_changes with concrete findings; the
   same worker resumes in the same tree. Allow at most {cycles} correction
   cycle(s); after that, reassess the scope rather than sending more.

6. INTEGRATE. When the build is acceptable, call accept_build with the task id
   and workdir set to the ABSOLUTE path of your current working directory
   (mode "apply"). The worker's changes then land in your checkout,
   uncommitted, as this task's own diff. Run the project's verification
   commands here yourself. Do not commit, push, merge or deploy.

7. REPORT. Finish with ONE final report, nothing after it:

STATUS: ready_for_review | blocked | failed
PLAN: the design in a few lines, and any decision you made for the operator
BUILDS: each delegated task id, its outcome, and how many correction cycles
CHANGED: each path and what changed in it, one line each
VERIFIED: each command, its exit status and the salient result
RISKS: anything the operator should look at first
`
