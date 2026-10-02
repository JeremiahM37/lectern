# Bounded Lectern comparison rubric

This rubric records observable workflow evidence, rather than feature counts or
performance claims. Every cell is `PASS`, `FAIL`, or `UNVERIFIED`; a partial
observation remains unverified.

| Case | Pass condition |
| --- | --- |
| C1 setup | Four named sessions are created through the product surface, reopened, and show a deterministic fake-agent marker. |
| C2 find/group | A named search returns exactly one intended record with its agent, project, target, and repository context. |
| C3 attach/reconnect | Attach, marker/sentinel, scroll, resize, detach, and reattach preserve the same terminal identity and readable output. |
| C4 workspace/diff | A child workspace has a distinct identity, the parent remains unchanged, and the exact tracked-file diff is visible to the user. |
| C5 everyday web | Desktop and phone can find, open, send a sentinel, return, reopen, and recover from a visible error without clipped required controls. |
| C6 setup/mobile | A custom agent retains exact multiline text, repository/target, and selected agent; a setup error retains the draft or gives an actionable error. |

Fixtures use local Git repositories, isolated HOME/XDG/tmux state, and fake
executables. They make no model, provider, paid-auth, or external network
calls. API registration of a project/target is a prerequisite for the wizard;
session creation itself is performed through the tested product surface.

This ledger does not support an overall superiority claim. Unverified cells are
kept open, and product-specific UI paths are described separately.
