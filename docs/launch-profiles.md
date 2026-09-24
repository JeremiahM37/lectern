# Named launch profiles

A launch profile names reusable settings for future interactive sessions: an
agent, optional command and model overrides, an environment object, and an
editable description and workflow briefing. It does
not partition the session database or change a running process. Profile paths
are interpreted on the session's selected target.

In the web UI, choose **Manage launch profiles** — and pick the profile itself —
under **Advanced options** in New session, or from command search. In the
terminal dashboard press **P** (also in All actions). Create, edit and delete
profiles there, then choose one in New session. The selected profile supplies
the agent; the collapsed sheet says which profile is in charge, and an explicit
model field overrides its default model. Failed saves and launches retain the
form draft.

The API supports:

- `GET /api/launch-profiles` to list definitions.
- `POST /api/launch-profiles` to create one.
- `PUT /api/launch-profiles/{id}` to replace its settings.
- `DELETE /api/launch-profiles/{id}` to remove the reusable definition.
- `POST /api/sessions` with `profile_id` to launch with that definition.

A definition contains `name`, `agent`, `command`, `model`, `description`,
`instructions`, and `env_json` (a JSON
object encoded as a string, consistent with project environment settings). Use
the profile's environment for provider URLs and credentials; the selected agent
runner remains the executable that starts the session.
Names are unique without regard to ASCII case. Deleted IDs are never reused,
so a stale selector cannot silently launch a replacement definition.

Environment precedence is agent settings, project settings, selected profile,
then any explicit internal launch overrides. A supplied model overrides the
profile model. Supplying a conflicting agent fails. Omitting a profile preserves
existing launch behavior.

The session captures its resolved launch settings and profile name. Later profile
edits or deletion affect new launches; native forks and resumes retain the
captured settings. Public session responses include only `launch_profile`, the
captured label, rather than profile environment values. The profile configuration
API itself returns its environment, just like the existing agent and project
configuration APIs, and sends `Cache-Control: no-store`.

Global conversation search includes configured Claude/Codex profile histories
before a first tracked session exists, and offers the named settings for forks.
It does not create placeholder tracking records.

Backend evidence includes database migration/reopen, preserved unrelated
settings, deleted-ID protection, profile/default precedence, explicit model and
agent boundaries, captured settings after edit/delete, private session output,
and real local history search from a profile with no tracked session. Real tmux browser tests at 390/1440 pixels and a real PTY test cover management,
selection, failed-save drafts, pending-save Escape handling, actual command/
model/environment execution and continuation after profile deletion. A nested
dialog Escape regression is covered by the browser flow. The c5ac0e2 rollout passed the full Go suite, 180 end-to-end cases, and
installed Codex/Claude plus SSH profile proofs. Server and PC run the same build.

## Workflow profiles

Profiles also carry a short `description` and multiline `instructions`. The
instructions become a labelled opening briefing alongside existing project and
memory context. They do not replace the agent's system instructions, install
plugins or add delegation capabilities. Captured session settings retain the
briefing for resumes and forks even after the reusable profile changes.

The manager offers four editable starters:

| Starter | Use it for | Working style |
| --- | --- | --- |
| Lean Builder | Small fixes and everyday features | One focused implementer, proportionate checks, minimal duplication |
| Reviewed Delivery | Changes across several components | Lead, bounded implementation, independent review when supported |
| Debugging Team | Regressions and elusive failures | Separate evidence gathering, explicit hypotheses, reproduction and verification |
| Research & Plan | Unfamiliar integrations and larger design decisions | Focused researchers, comparison of alternatives, concrete implementation plan |

Choose **Use starter**, adjust the agent and instructions, then **Save profile**.
Choosing a starter creates an unsaved draft; it never overwrites an existing
profile. Starters use Codex by default but inherit the configured model and can
be changed to another installed agent. They contain no provider credentials or
command overrides. Advanced provider settings remain available on the same form.

Roles in a briefing describe the desired workflow. A CLI with delegation can
use real helpers; otherwise it follows the workflow itself and must describe
that honestly. The presets avoid redundant investigations, limit concurrent
helpers, and scale verification to the task. A blank session waits for the user
to supply a task. Research & Plan continues into implementation when the user
has already asked for it, without inventing an approval step.

`GET /api/launch-profile-presets` returns the starter catalog. Saved profiles
accept `description` (up to 2,000 bytes) and `instructions` (up to 16,000 bytes),
without NUL characters. Existing API clients may omit these two fields on PUT
to preserve them; sending an empty string clears the field. Briefing text is not
included in public session responses.

### Design references

These are original Lectern briefings informed by open-source workflows reviewed
on 2026-09-23, not bundled copies of those projects or promises of their tooling:

- [Superpowers](https://github.com/obra/superpowers): bounded implementation,
  review against the request, systematic debugging and verification evidence.
- [Everything Claude Code (ECC)](https://github.com/affaan-m/ECC): distinct
  planner, implementer, reviewer and diagnostic roles.
- [GitHub Spec Kit](https://github.com/github/spec-kit): separate research,
  planning, implementation and verification stages with concrete artifacts.

The starter instructions are in `internal/api/launch_profile_presets.json`.
They are shipped as suggestions, saved as ordinary profiles, and can be edited
without changing the catalog or affecting other users' saved definitions.
