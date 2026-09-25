# Autonomous workshop

An opt-in continuous workshop, enabled by default in configuration but initially
OFF. Open `/autonomy.html` (or its compact home-dashboard embed). Only the
authenticated owner can enable, start or stop it; a local agent cannot grant
itself autonomy. All writes to live services, publishing, purchases and outside
messages remain outside this experiment.

The morning strategic review runs at the first planning checkpoint after 08:00
America/Denver, without interrupting an active worker. It searches Grimoire,
checks Lectern projects, active tasks, saved artifacts and prior cycle records,
then refreshes a persistent, ranked backlog. The backlog favors ambitious,
high-value projects with strong leads, affordable next steps, novelty and clear
reasons. It may include long-running research contributions and homelab fixes;
the planner chooses work that can make useful progress within the current
budget. A cycle is a bounded interval, not a cap on the project's scope. Later
cycles continue from retained files, reports and explicit checkpoints.

Workers must request a checkpoint before major architecture, direction, resource
or experiment decisions. The controller blocks the next builder step until
two independent decision reviews complete. A decision request pauses for
separate reviewers to assess the proposal and its evidence; reviewers do not
see each other's verdict. Auditors also independently evaluate plans, with both
required to approve and at most two revision rounds. Strong leads and affordable
execution are preferred over spending a cycle on weak or speculative work.
Selected work runs in an isolated builder, followed by an independent reviewer
inspecting a copy of its actual files. Rejected or failed work remains available,
never silently deployed. One role runs at a time. The daily record can contain
multiple numbered cycles and carries backlog and decision audit history forward.

## Budget and stop behavior

The controller reads the existing normalized Claude/Codex usage collector.
While work is active the controller polls every 30 seconds; the usage collector
refreshes healthy provider snapshots every 120 seconds and preserves failure/429
backoff. New work can use either provider with healthy allowance; a running job
always checks its own provider. All advertised account/model windows apply,
including scoped limits.
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
workspace. Failures use persisted retry backoff and then wait for the next eligible cycle,
preventing tight failure loops. The morning strategic review continues to
re-rank work using accumulated artifacts and cycle history.

## Evidence and recovery

Lectern stores each role as a task receipt and its normalized output as events;
the deterministic controller owns its execution. General task dispatch,
takeover and integration endpoints cannot run those receipts outside the
sandbox. Daily records retain cycle numbers, the ranked backlog, decision requests and audits, proposals, verdicts, report evidence and job IDs.
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
to test the experiment. Subsequent commissioning completed a real five-role cycle; long-term output
quality still requires observation. Continuous scheduling defaults to enabled in configuration; upgrading preserves
the owner’s existing ON/OFF choice.

A real Codex worker also passed a minimal file-write/report smoke test. This
exercised its companion execution binary inside the same sandbox. Claude uses
proxy-side DNS (`CLAUDE_CODE_PROXY_RESOLVES_HOSTS=1`); generation was not tested while below reserve. A later real Luna worker passed
the same isolated file-writing and report test. A crash between runner metadata creation and unit start
uses persisted operational retry backoff and resumes with a fresh job.

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

Operational failures retry automatically with persisted 1/5/15-minute backoff,
then wait for the next eligible cycle after three retries in one day. Completed
planning/audits are retained and failed workers resume in fresh isolated jobs
with partial files. Unsafe snapshots, corrupt receipts and unknown failure types
stay blocked. Quota uncertainty always blocks launches independently of retries.
Git archive global PAX metadata is ignored as metadata, never extracted as a file.

Continuous cycles wait one minute between useful batches (15 minutes after an
empty plan). Routine builders use an available Luna model or Claude Sonnet;
planning and independent reviews use Astra or Claude Opus. Audited expert tasks
may use a strong builder. Approved artifact receipts remain usable beyond the
90-cycle dashboard history; full cycle records are archived locally and backed
up. Only independently approved builder artifacts can seed another cycle.
