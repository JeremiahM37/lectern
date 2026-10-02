# Adding a repository without replacing the session

The active terminal must stay attached while a new repository is checked out.
Creation's `setup_state` cannot represent this operation: it hides attachment and
assumes no agent exists yet. Keep extension state separate from initial launch.

## Worker contract implemented on the workspace-extension branch

- Plan exactly one new repository, preserving all original allocation tokens,
  names, paths and branch identities. Resolve aliases and reject duplicate Git
  common directories on the target before mutating the root.
- Acquire the existing root operation lock. Validate existing checkout ownership
  and branches, but allow dirty files and active terminals in those checkouts.
- Publish the complete expanded root receipt before starting the new child.
  Existing setup commands never rerun; only the new repository's command runs.
- Give each extension a fresh cancellation token. The initial creation's
  cancellation record cannot poison a later extension.
- Status accepts matching prefixes of repository ownership lists, so an older
  database record can discover an extension after an interrupted response. This
  relaxation is read-only. Cleanup and recovery still require exact ownership
  lists, preventing a stale request from silently deleting an unseen addition.
- Failure and cancellation retain both original files and partial new files.
  The original terminal is outside the checkout process group and stays running.

Real Git/tmux tests cover dirty-file preservation, no repeated setup, canonical
alias rejection with an unchanged receipt, failed-extension discovery from an
old record, stale cleanup rejection, and cancellation with an active terminal.
The browser and terminal dashboards now connect to this worker through durable background operations.

## Durable background operation

Persist a workspace operation independently of initial session setup, including
session ID, captured expanded plan, state, error, cancellation intent and times.
Reserve workspace mutation paths while starting the operation, but do not require
ending sessions that already use the group. Save the planned ownership before
sending the target command, return HTTP 202, and let browser/TUI polling display
progress without disabling Attach or replacing the terminal frame.

On restart, never automatically rerun a checkout or a setup command. Inspect the
root receipt and operation lock. If an old target worker may still arrive after
a transport disconnect, first write cancellation for that operation's unique
token; merely seeing the old root receipt is not proof that no worker will start.
Only after the operation is quiescent should the manager reconcile the saved
allocation. A completed expanded receipt can be adopted; a partial receipt stays
inspectable. A request that never reached allocation keeps the original group.

The UI should select one registered project on the same target and an optional
base, show its setup command, and expose progress/cancellation. The terminal needs
the same named selection and operation feedback. API/integration/PTY/browser tests
must prove the original tmux identity remains usable throughout, including after
server restart and cancellation. Legacy single-worktree conversion remains a
separate operation; it must not relocate a directory underneath a live agent.


Implemented API: POST session `worktree/repositories` reserves an addition and
returns 202; `worktree/operations` lists progress, with ID-scoped cancel/recover
POSTs. Private plans and captured environment are not serialized. A transactional
compare-and-swap reserves the plan, and terminal operations cannot overwrite a
newer operation's allocation. Cancellation intent survives failed delivery.

Browser: Workspace repositories in the session action menu offers a same-target
project selector, optional base, setup command preview, close/reopen progress,
and cancellation/recovery. Adding never replaces the attached terminal frame.
Terminal: Add repository, Repository addition progress, Cancel repository
addition, and Check interrupted addition are named session actions.

Tests exercise real Git/tmux API completion/cancellation, stale reservations and
late completions, phone/desktop browser retry and reopening, a real dashboard PTY
form, and a killed/restarted isolated server during the new repository's setup.
The latter explicitly requests reconciliation after restart; automatic recovery
is wired to the normal session polling pass.
