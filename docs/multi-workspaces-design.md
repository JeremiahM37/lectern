# Multi-repository workspaces — staging design

This branch is not deployed. A workspace owns a root containing up to eight
separately owned Git worktrees. The browser and terminal offer additional
registered repositories, per-repository bases, and a shared branch name when
creating an isolated session. The primary project supplies agent settings;
additional projects do not merge their environment or launch settings.

## Creation and recovery

`worktree.extra_repositories` accepts project IDs and optional bases. All projects
must use the same target. Grouped creation requires a primary project and rejects
a working-directory override. Target-side preflight canonicalizes Git common
directories, rejects duplicates and branch collisions, and resolves bases to
commits. Creation repeats validation under an operation lock.

The root records the entire allocation plan before creating children, fsyncing
progress after each. Each child has its own ownership token, source repository,
base, path, state and error. Public session responses redact every ownership token.
A child process starts behind a pipe gate until its process-group receipt is
written. Inherited locks and process-group checks prevent cleanup while a checkout
continues after its supervisor dies. Linux zombies do not count as writers.
Hooks that deliberately daemonize outside their process group require additional
policy before reusable setup hooks are exposed.

Failed setup retains the session record, files, and allocation receipts. Recovery
checks all repositories before removing any and rechecks each during removal.
Changed, untracked, ignored, or unexpected root files block removal. Active tmux
sessions at the root or within its children also block removal. Partial failures
retain durable progress and all branches. Removing children leaves the owned root
and receipt; it does not recursively delete the workspace.

Metadata writes use a fresh private inode and atomic replacement. Reads reject
symlinks and special files, validate process-group receipts, and check the saved
operation-lock identity. Replaced locks and damaged receipts require inspection.

## Inspect setup progress

Workspace details in the browser include Refresh setup progress. The terminal
session actions include Workspace setup progress. Both read the target's atomic
allocation receipt, including per-repository states and setup errors, even while
checkout holds the operation lock. `GET /api/sessions/{id}/worktree` returns this
redacted snapshot; `?format=text` returns a readable terminal view.

The read validates allocation and lock ownership and never writes the receipt or
session database. It reports the last recorded state, not a guarantee that an
orphaned process is still running. During background creation, the browser refreshes
per-repository progress automatically; the terminal refreshes the selected
session's progress in its preview. Manual inspection remains available afterward.

## Background setup API

An opt-in `POST /api/sessions` with `background:true` and `worktree` returns 202
with a persisted session reservation before target setup. `setup_state` records
`creating`, `ready`, or `failed`; `setup_error` retains the launch failure. The
worker survives the initiating HTTP response or disconnect, with a 30-minute
overall deadline. Nested Git and child-worker deadlines follow that budget.
Existing synchronous callers keep their earlier timeout behavior.

The poller does not interpret a missing terminal as a failed live setup. A
reservation whose controller worker is gone becomes an interrupted failure,
even if its SSH target is unavailable. Allocation files remain for inspection.
Stop, archive and removal refuse an active setup instead of falsely reporting it
stopped while its worker continues. Explicit cancellation and restart recovery
beyond inspection are still pending. Normal browser and terminal isolated-session
creation now use this API. The creation form closes once the reservation is
accepted, the session shows Setting up, and attachment becomes available after
setup finishes. Navigating away or reloading does not abandon setup. Native
conversation-fork creation still uses the synchronous API and needs the same
background treatment separately.

Setup failures remain in the current-session view until archived, with their
error and retained worktree details. They count as needing attention in the
terminal. The regular API remains live-only unless callers request
`include_setup_failures=true`. Failed or incomplete setup cannot open an empty
terminal through the attachment endpoint. Browser tests at390/1440 and a real
terminal PTY hold the second checkout, verify the first repository's ready state,
and then release setup into a usable session; browser tests also reload midway.

The real API lifecycle test returns while a Git hook is paused, observes setup
past the poller's grace period, releases the hook, and verifies both success and
retained failure. Opt-in elapsed-time proofs cross the old nested timeouts:

```sh
LECTERN_SLOW_SETUP_PROOF=1 go test ./internal/api -run TestBackgroundWorkspace -count=1
LECTERN_SLOW_SETUP_PROOF=1 LECTERN_GROUPED_SETUP_PROOF=1 go test ./internal/api -run TestBackgroundWorkspace -count=1
```

## Review and continuation

The agent starts at the shared root. Web review offers a repository selector;
terminal review cycles repositories with Tab. `changes?repository=N` selects a
recorded repository by index, never a supplied filesystem path. File browsing
reaches the child repositories while hiding internal root allocation receipts.
This filtering is a UI boundary, not a restriction on commands in the shell.

Shared continuations resolve the owner's grouped allocation without acquiring
removal ownership. They reject incomplete or removed children. Isolated forks
start each repository from its current committed HEAD, or an explicitly selected
primary base. They exclude uncommitted edits and allocate from stable source
repositories, so removing the parent does not break child cleanup.

Codex forks receive a per-launch directory override. For Claude's native
`--resume {id} --fork-session` template, isolated forks resolve the exact source
transcript on the target, within the captured native profile, and pass its path
to Claude. This is a documented native input, not a copied history seed:
[Claude CLI reference](https://code.claude.com/docs/en/cli-reference).
The resolver checks conversation identity and original workspace and refuses
missing or ambiguous matches. It scans project directories to accommodate custom
or hashed storage names. Custom fork templates remain unchanged. Future launches
retain the original argument template rather than a frozen source path.

The experimental snapshot helper was removed in favor of this native path.
Lectern does not publish duplicate transcripts or manage copied sidecars.

## Evidence and remaining work

Real Git/API/tmux tests cover grouped creation, later checkout failure, supervisor
death, concurrent operations, dirty and partial cleanup, metadata replacement,
source/index preservation, and shared/isolated workspace continuation. Desktop,
phone, and PTY tests cover creation and repository review. A lookup regression
found by the combined suite was fixed by selecting only allocation metadata,
avoiding unrelated legacy session rows with null timestamps.

`e2e/test_grouped_native_fork.py` covers web and terminal search-to-fork workflows,
exact Claude path arguments, two-repository allocation, and unchanged native
profile files. `tests/native_grouped_sessions.py --mode fork|resume` is a manual
installed-Claude/Codex test using private synthetic histories and no model turn.
It checks exact native identity, saved history visibility, captured launch
profiles, and the correct shared or isolated working directory.

Remaining workspace features include adding repositories to existing groups
and transitioning older single-worktree sessions into groups. Fresh isolated creation and isolated native
conversation forks run in the background from both interfaces, with recorded
progress and retained failures. Run the full combined verify suite on
the final source and verify deployment on both server and desktop.

### Concurrent setup and cleanup

Controller lifecycle reservations cover the source repositories and allocated
root on their target. Launches can share a directory, but cleanup refuses while
a launch uses that directory or a descendant. Unrelated directories and other
targets proceed independently; no mutex is held through checkout or SSH.
Existing source paths and allocation parents are resolved on the target so
symlink aliases receive the same protection. Target receipt/operation locks
remain responsible for surviving workers and filesystem validation.

A real Git integration test holds a checkout hook open while removing another
ended allocation on the same target. Reservation tests cover shared launches,
ancestor/descendant conflicts, destination allocation, and symlink aliases.


Isolated conversation forks from saved workspace history and global search use
`background:true` and return HTTP202 after reservation. Shared-directory forks
and API clients that omit the flag retain synchronous HTTP201 behavior. Native
history/profile validation still happens before reservation; the worker retains
the chosen conversation ID and captured launch settings. The browser returns to
Sessions instead of attempting to attach to an unfinished terminal. Terminal
search selects the new reservation and reports setup in its preview. A held
second-repository checkout test covers phone and desktop reloads, the absence of
a premature agent launch, and the exact native history path after release.

Setup reservations persist their private launch configuration before returning
HTTP202. A checkout failure therefore retains its command, declared environment,
profile identity, and permission setting even if reusable settings change later.
The settings do not enter the checkout environment; Git still runs with the
same target environment as before. Regression coverage changes agent settings
both during setup and after failure and checks the retained snapshot.

### Setup cancellation

The worktree runner accepts `cancel` and records a request beside the allocation,
keyed by its private ownership token. Create workers check that record while
waiting for Git. Only the worker owning a live child signals its process group;
requesters never signal a PID read from an old receipt. Grouped children inherit
the cancellation identity, so a child surviving supervisor loss can stop too.
Cancellation leaves checkout files intact. A partial allocation whose ownership
was not finalized remains protected from automatic removal and needs inspection.
Control records reject symlinks, foreign identities, and replacement while a
worker holds their identity. Tests cover held single/grouped hooks, orphaned
children, retained files, and unchanged files behind rejected symlinks.

`POST /api/sessions/{id}/setup/cancel` durably records
`setup_cancel_requested` before contacting the target. The browser card and
terminal action menu expose Cancel setup, with retry when delivery fails. Failed
setup records offer cancellation of a potentially remaining checkout. HTTP202
means the request was recorded, not that the worker has stopped. Files stay in
the retained allocation; existing dirty/unowned cleanup checks still apply.

Cancellation acceptance and the decision to start the agent are serialized. If
cancellation wins, pre-checkout and pre-launch checks prevent further startup,
even when the request disconnects or target delivery fails. Once agent launch
has begun, cancellation refuses and directs the user to End. Migration tests
check the cancellation flag survives reopen; real API and desktop/phone/PTY
flows cover held checkouts.

### Controller restart recovery

Background launches set an ownership marker using tmux's `new-session -e`, so
creation and marking are one target operation. After restart, a reservation whose
checkout was already recorded ready is checked against that marker. A matching
terminal is tracked again; no new agent is launched. A reused name or missing
marker leaves the other terminal untouched and the record available for
inspection. Connection failures keep verification pending rather than marking a
potentially running agent ended. Setup status explains that wait in both UIs.

A checkout not recorded ready cannot yet have launched its agent. Such an
interrupted record retains its allocation and offers cancellation/inspection;
recovery does not blindly restart the checkout. Cancellation of an orphaned
ready allocation first resolves whether its agent exists, preventing a promise
to cancel an agent that has already started.

The restart E2E test stalls the launch client's return immediately after tmux
creation, kills the real controller, and restarts it against the same database.
Phone and desktop attach to the unchanged tmux session; an agent-side counter
proves one launch. Unit integration tests also cover foreign/unmarked/missing
terminals and unavailable targets.


### Validate an interrupted allocation

`POST /api/sessions/{id}/worktree/recover` is exposed as Recover allocation on
failed setup records in both interfaces. It validates repository identity,
branch, and the recorded initial commit before writing a missing ownership
marker. It does not reset files, rerun hooks, restart setup, or launch an agent.
Existing user changes are retained and still prevent cleanup. Grouped validation
checks every child before claiming any missing marker; children never created
are recorded absent, not fabricated. A changed revision, foreign owner, symlink
replacement, active worker, or active terminal makes validation refuse.

Single checkouts now retain their exact initial revision in the target-side
setup record before `git worktree add`. Their operation lock is inherited by a
detached checkout monitor and Git, so loss of the outer supervisor cannot make a
running checkout appear safe to claim. The monitor observes cancellation even
without its supervisor. Missing provenance in older interrupted allocations
requires manual inspection; recovery never guesses a commit from today's HEAD.

Tests cover killed single/grouped supervisors, cancellation, missing owner
recovery, preserved dirty files, clean removal with branches retained, changed
commits, foreign markers and symlink targets. The phone/desktop/PTY cancel flows
continue through validation and guarded cleanup. Setup control errors avoid
including private token-bearing record paths in public error messages.
