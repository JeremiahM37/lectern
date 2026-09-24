# Autonomous workshop

An opt-in daily experiment, off by default. Open `/autonomy.html` (or its compact
home-dashboard embed). Only the authenticated owner can enable, start or stop
it; a local agent cannot grant itself autonomy. All writes to live services,
publishing, purchases and outside messages remain outside this experiment.

At 08:00 America/Denver the planner searches Grimoire, checks Lectern projects,
active tasks and previous daily records, then proposes zero to three small,
useful tasks. Existing projects and new research both fit; new research uses the
`autonomous-experiment` project. Two fresh auditors independently evaluate the
plan; neither sees the other's verdict. Both must approve. At most two revision
rounds prevent endless debate. Each item gets an isolated builder and then an
independent reviewer inspecting a copy of its actual files. Rejected or failed
work remains available, never silently deployed. One role runs at a time.

## Budget and stop behavior

The controller reads the existing normalized Claude/Codex usage collector.
While autonomous work is enabled it requests healthy snapshots every 30 seconds;
provider failure/429 backoff is preserved. Both providers must have usable quota
telemetry. All advertised account/model windows apply, including scoped limits.
Codex plans reporting only a weekly window are supported. Claude needs weekly
and session coverage. Missing, stale, invalid or reset-past data pauses work.

The protected reserve is 10%, with a further 5% margin: jobs pause at **15%**
remaining. This is a conservative cutoff, not a provider-enforced reservation.
Usage arrives late and may jump, so no third-party controller can guarantee an
exact remaining percentage. A 60-second supervisor heartbeat independently
stops a worker if the controller disappears. A job also has a 30-minute limit.
There is no paid API fallback or automatic reset-credit purchase.

OFF is persisted before worker cancellation. Only owned UUID service cgroups
are stopped; existing interactive sessions are untouched. Files are retained.
Budget pauses resume with fresh quota in a new process using the saved partial
workspace. Failures need the owner's Start-today retry, preventing automatic
failure loops. Completed days are not replayed, even across service restarts.

## Evidence and recovery

Lectern stores each role as a task receipt and its normalized output as events;
the deterministic controller owns its execution. General task dispatch,
takeover and integration endpoints cannot run those receipts outside the
sandbox. Daily records retain proposals, verdicts, report evidence and job IDs.
Authenticated Download-files links export only a job's workspace. Human
promotion is deliberately separate from autonomous building.

Source repositories are snapshotted at committed HEAD; dirty work and live
sessions are not edited. Source archives over100MiB or with unsafe/link entries
are refused. Build/review workspaces have 2GiB hard capacity. Durable gzip
artifact snapshots are made after completion/stopping, and the homelab backup
includes them. Private auth/runtime assets and duplicate backing images are
excluded from off-box artifact backups. A control-plane database backup keeps
the daily state and task receipts. Retained work uses at most50GiB before the
experiment pauses for archival; at least20GiB host free space is required to
prepare another job. Nothing automatically deletes retained work.

Grimoire is queried through a read-only scoped bridge. Completion writes a
controller-authored checkpoint identifying the daily record; raw model reports
stay in Lectern so credentials cannot accidentally be copied into the
phone-synced notes vault. The read-only `/history` bridge supplies prior results
for the next plan. Workers get no general control-plane API, SSH key, sudo,
host tmux socket or production directory. See [isolation](autonomy-isolation.md)
for the real enforced boundary and residual credential risks.

## Implementation and commissioning

`internal/autonomy` is the pure quota/state policy. `internal/api/autonomy*.go`
owns durable orchestration, task receipts, scoped context and the public-only
network proxy. `tools/autonomy-runner.py` is the root-owned Linux launcher.
The source CLI binaries and existing subscription logins are copied privately;
provider credentials never enter the dashboard or model prompt.

Run the project's isolated verify suite, then the real credential-free runner
selftest, including public HTTPS and blocked private IPv4/IPv6/DNS destinations,
heartbeat termination, artifact preservation, report handling and review copy.
Do not spend a provider's protected quota merely to smoke-test a deployment.

Prior-art review informed the persistence and isolation boundaries:
[LangGraph durable runtime](https://www.langchain.com/blog/runtime-behind-production-deep-agents)
and [OpenHands platform](https://arxiv.org/abs/2407.16741).
Existing Lectern scheduling, task receipts and Grimoire context are reused
instead of adding a second general orchestration framework.

Initial commissioning (2026-09-24): real no-model isolation, public HTTPS,
private IPv4/IPv6/CGNAT/localhost rejection, socket replacement in a live worker,
heartbeat expiry, copy/report/export preservation all passed. Claude's live
Fable weekly allowance was already 1% remaining; no Claude generation was used
to test the experiment. Real model-driven daily output quality therefore still
needs observation after quota resets. The mode ships OFF.

A real Codex worker also passed a minimal file-write/report smoke test. This
exercised its companion execution binary inside the same sandbox. Claude uses
proxy-side DNS (`CLAUDE_CODE_PROXY_RESOLVES_HOSTS=1`); generation was not tested
while below reserve. A crash between runner metadata creation and unit start
pauses safely for inspection; use Start today to retry with a fresh job.

## Publication consent

Autonomous mode and peer approval never authorize a public action. Pushes, PRs,
issues, comments, releases, public uploads, messages and deployments require
Jeremiah’s explicit consent for the specific action. Workers prepare local drafts
and downloadable artifacts only. This release offers no publish button or agent
approval path. A human-directed session may publish only after receiving consent.

The worker proxy now blocks general external connections, including GitHub and
registries. Only exact subscription-inference hosts retain TLS access. Research
uses a credential-free GET broker for approved document hosts; redirects and
query strings are refused. This intentionally limits online dependency installs.
See isolation documentation for the remaining model-provider trust boundary.
