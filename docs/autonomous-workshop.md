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
query strings are refused. Workers cannot install packages over the network.
A separate fixed provisioner now supplies verified offline Go module bundles;
failed prerequisites retain their admitted assignments for bounded automatic
recovery while other work proceeds. See `/requirements` for the recorded state.
See isolation documentation for the remaining model-provider trust boundary.

Operational failures retry automatically with persisted 1/5/15-minute backoff,
then retry every 15 minutes in continuous mode (daily mode waits for its next
eligible cycle). Completed
planning/audits are retained and failed workers resume in fresh isolated jobs
with partial files. Unsafe snapshots, corrupt receipts and unknown failure types
stay blocked. Quota uncertainty always blocks launches independently of retries.
Git archive global PAX metadata is ignored as metadata, never extracted as a file.

Continuous cycles wait one minute between useful batches (15 minutes after an
empty plan). Routine builders use an available Luna model or Claude Sonnet;
planning and independent reviews use Astra or Claude Opus. Audited expert tasks
may use a strong builder. Approved artifact receipts remain usable beyond the
90-cycle dashboard history; full cycle records are archived locally and backed
up. Only independently approved builder artifacts can seed an ordinary continuation.
Rejected artifacts have the separate repair path below, without gaining approval.

## Report remediation

Malformed worker reports have their own recovery path. The controller retains
files, records the precise validator error, and schedules up to two correction
attempts (30/60-second backoff), each in a fresh sandbox under the same quota,
role and review rules. The prompt asks for report correction from existing
evidence, not repeated implementation or invented success. After two failed
repairs the cycle is explicitly abandoned with artifacts retained; continuous
mode plans a new cycle without approving the failed work. This does not repair
unsafe snapshots, state corruption or storage failures by relaxing controls.
Legacy paused reports are revalidated after deployment, permitting recovery
without another model call when the validator itself was wrong. Future backlog
ideas need acceptance criteria only when selected for execution.


## Rejected checkpoint repair

`continue_task_id` accepts only approved checkpoints from `/artifacts`.
`repair_task_id` instead selects an owned, completed builder checkpoint with an
explicit rejected **final review**, listed separately at `/repairable`. The two
fields are mutually exclusive. Unreviewed work and rejected major decisions
cannot use this path. The source project and exact reviewer/checkpoint identity
must match; rejection reasons remain available after history rotation.

A new plan and both independent plan audits must approve the repair before the
controller copies its files into a fresh sandbox. Auditors and builders receive
the old rejection as evidence. The repair grants no approval: major decisions
still require both decision reviewers and the repaired result needs a fresh
final review. Rejected work remains excluded from `/artifacts`.

Repairs are capped by the configured revision limit across the entire repair
lineage. Intermediate decision checkpoints and resumed workers retain the same
repair-attempt identity, so they neither reset nor consume the limit twice.
Invalid selected source IDs are caught during planner report validation and use
the existing bounded report-correction path instead of pausing during launch.

A legacy cycle paused specifically because it selected an explicitly rejected
checkpoint as an approved continuation is abandoned with its evidence retained,
then automatically replanned after the normal cooldown. Its old plan is never
silently rewritten into a repair authorization. Disabled mode, uncertain quota,
active workers and corrupt/missing receipts do not take this recovery path.
Publication restrictions and quota reserves are unchanged.

## Planning context and retained backlog

Planners receive a compact backlog discovery index instead of repeatedly carrying
all historical blocker prose. Builders, reviewers and decision auditors receive
the full selected plan and its acceptance criteria, without unrelated backlog
entries. This changes context delivery, not the stored proposals or reports.

`/backlog?view=index` returns explicitly incomplete previews, lineage identifiers
and a content-keyed `details_uri`. Before selecting or rejecting an opportunity
based on a preview, read `/backlog?key=KEY`: it returns the exact full proposal,
including constraints omitted by the preview. This instruction does not establish
eligibility or approval. Current source/ownership checks and independent audits
still apply. Planner rewrites must retain distinct constraints/evidence while
replacing repeated cycle-status narration with the current prerequisite.

The legacy `/backlog` endpoint still returns the full current backlog. Exact-key
lookup searches current, deferred and retained in-memory cycles; an old key may
return404 after history rotates out, even though archived reports remain retained.
It never substitutes another similarly titled opportunity for missing content.

## Completed workspace preservation

Completed workers enter a persisted `exporting` state before their reports are
applied. A UUID-scoped systemd exporter preserves the complete workspace under a
10-minute, 512 MiB, one-CPU limit, using per-job locks and atomic publication.
The original workspace remains intact on every failure. Interrupted exports retry
preservation with backoff; they do not rerun the model or consume report repairs.
Status reads use the latest committed controller snapshot without waiting for
compression. OFF leaves bounded evidence preservation running, retains the pending
assignment, and prevents further agent work. Restart reconciles the existing unit
and published archive. Downloads use the published archive, not a second export.
Other controller I/O can still delay mutations; this removes the archive-duration
lock specifically. Legacy two-minute archive timeouts are recovered only when the
original worker exited successfully and its report passes the unchanged validation.

## Private integration receipts

`GET /api/autonomy/integrations` and the worker's read-only `GET /integrations`
expose append-only records of scoped local integration. A trusted local integrator
or authorized human records one with `POST /api/autonomy/integrations` after local
commit and verification. Include `task_id`, exact `job_id`, `project_id`, full
`revision`, original `report_sha256`, `scope_type` (`adapted` or `partial`), concrete
`integrated_scope`, `remaining_scope` (required for partial work), and private
validation `evidence` references. The server verifies the original report identity,
task/project association and commit reachability from the registered canonical
repository's captured HEAD; caller-supplied repository paths are not accepted.
Repeated identical submissions return the original receipt.

These records attest integration scope; evidence references are not automatically
executed, byte equivalence is not inferred, and later reverts can remove changes.
Current presence remains explicitly unknown until inspected. Historical approval,
rejection, repair limits, unfinished scope and publication restrictions remain
unchanged. `/artifacts` and `/repairable` annotate matching receipts without hiding
rejected or partially integrated work. The worker bridge refuses writes. Record
one after every trusted local integration so planners need not infer completion
from a project name, source revision or stale narrative memory.

## Declining work is audited

An empty `items` array goes through both independent plan auditors. New planner
assignments (report version 3) must include `no_work`: a concrete `reason`, up to
12 uniquely keyed prerequisite `blockers` with evidence, and 1–4 `exploration`
findings with opportunity, decision and evidence references. Blockers may be empty;
a planner must not invent a prerequisite merely to satisfy a schema. The evidence
is a claim to inspect, not mechanically certified research or proof of novelty.

Both auditors may approve a justified no-work decision, which completes the cycle
without ever scheduling a builder. Disagreement requests revisions under the
existing cap; exhaustion completes without a build. OFF/quota gates and repair
lineage rules remain unchanged. In-flight legacy planners can submit their old
schema, but their empty reports still receive both audits, with missing evidence
visible to the auditors. Historical completed cycles are not rewritten.

### Fresh repository discovery

Workers can query `GET /research/search?q=...` on their existing read-only bridge. The controller searches GitHub public repository metadata using a fixed unauthenticated GET endpoint, returns at most ten results, and permits four calls per minute shared across workers. It forwards no worker credentials, headers or bodies and refuses redirects. Queries are limited to 300 bytes. Provider errors, malformed/oversized responses and rate limits are explicit failures, never successful empty searches. `incomplete_results` is preserved.

Results include query, upstream status, retrieval time and a hash of the original response. These are unsigned metadata, not independently verifiable receipts; saved worker copies can be changed, and the hash does not cover the projected result JSON. Auditors should repeat important searches and inspect primary source files through `/research`. This is repository discovery, not comprehensive web/literature search and not evidence of novelty or feasibility. General Grimoire search configuration is unchanged.

`GET /research/issues?q=repo:OWNER/REPO SEARCH TERMS` searches public GitHub issues and pull requests through the same fixed-host, credential-free broker and shared four-call/minute allowance. Results identify issues versus PRs and include titles, state, updated time, comment count and up to 4,000 Unicode characters of body with an explicit truncation flag. Comments and complete discussions are not included; a closed issue or merged-looking PR is not proof that a behavior is fixed. Use this to check existing discussions before proposing upstream work. It grants no ability to post or modify anything.

### Assessing continuation value

Future planner and plan-auditor prompts require a continuation to explain the consumer, UX problem, demonstrable capability or research question; evidence from the prior checkpoint; the unresolved decision; and observations that justify continuing, changing direction or stopping. This uses existing `why`/`acceptance` fields and the two existing audits. It is a model judgment criterion, not a mechanical guarantee of useful work or an extra report-schema requirement.

Synthetic correctness alone does not establish demand or novelty. Additional fixtures should resolve a named uncertainty or protect a concrete consumer. Bounded exploration without demonstrated adoption remains valid when it discriminates worthwhile directions; a negative result can complete the experiment. Missing external inputs/environment capabilities must be explicit and only supported recovery mechanisms can provision them. Workers may still create fixtures and test data within their audited isolated assignment. Existing admissions, repair limits and publication policy are unchanged.

Research document reads buffer at most 2 MiB plus one detection byte before responding. Oversized bodies or upstream read failures return an explicit 502 without partial source bytes; a 200 response preserves the bounded bytes from a transport-complete response. A server ending a close-delimited body without a framing error cannot be distinguished from an intentionally short document; this is not a guarantee of semantic completeness. The existing approved-host, public-address, credential-free GET and no-redirect rules still apply. Small repository fixtures can be fetched from approved raw-source hosts; successful retrieval does not make them representative production data.

### Interrupted worker launch

A runner-owned per-job lock serializes launch and Stop. A durable start-intent marker precedes startup mutations. `launch-state` distinguishes an unused job, a launcher still holding its lock, a running unit, and a consumed terminal launch (including legacy partial assets). The controller reconciles persisted `starting` jobs before calling start again: live launches are retained, while consumed launches pass through the existing evidence export and operational recovery path into a fresh UUID with the same task, admission, audits and repair-attempt identity. Missing units or observation timeouts alone never authorize duplicate starts. Invalid launch receipts fail closed. Stop waits for an in-flight launch and records a marker that prevents a subsequent delayed start.
