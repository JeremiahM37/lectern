# Delegated builds: measured against the all-lead baseline and the upstream workflow

Taken 2026-09-21/22 on one machine (AIServer, 32 cores), lead model GPT-6
Astra on a ChatGPT subscription, worker DeepSeek Flash through the DeepSeek
API. Every number below was recorded by the harness in
`/mnt/bulk/codex-bench` from Codex's own session records; nothing is
estimated from task counts or durations.

## What was measured

Four real tasks in three repositories, each a bounded feature with a written
spec, a hidden acceptance test the agents never see, and the repository's own
suite:

| Task | Repository | Shape |
|---|---|---|
| `receipt-stats` | action-receipt (Python) | a session method and an MCP tool, plus tests and README |
| `lectern-sessions-cli` | lectern (Go) | one CLI subcommand with table/JSON output, filters, wiring, tests, docs |
| `librarr-source-health` | librarr (Go) | a tracker method, two HTTP routes, OpenAPI entries, tests |
| `lectern-tasks-cli` | lectern (Go) | a three-subcommand CLI group with filters, wiring, tests, docs |

Three arms, two runs each per task (24 runs):

- **All-Astra**: the lead does the task itself. No skill.
- **astra-flash-orchestrator**: the upstream skill, policy block and
  templates verbatim, with one substitution. On a ChatGPT login Codex refuses
  to spawn a non-OpenAI subagent (`The 'deepseek-flash' model is not supported
  when using Codex with a ChatGPT account`), and codex-router's login-free
  mode, which works around that, signs Codex out and maps the native model
  slugs onto external models, so there is no Astra in that mode. The worker
  therefore runs as a separate `codex exec` process on Flash, with the
  upstream WORKER-INSTRUCTIONS as its developer instructions; the lead
  dispatches it with one script call instead of `spawn_agent`. Everything the
  lead reads and does is the upstream workflow.
- **Lectern delegated build**: the same lead model with the `lectern-delegate`
  skill and the `lectern` MCP server; the worker runs as a Lectern task in a
  worktree, on the same Flash model with Lectern's worker preamble.

Grading is the hidden acceptance test plus the repository suite, both run
after the agent finishes. One suite run flaked (a real-process concurrency
test, graded while three runs shared the box) and passed twice on regrade;
the record says so. Token counts are the sessions' cumulative usage, split by
which Codex home the session ran in, so a root that had accidentally been
routed to another model would show up as a stray; none did.

Cost uses the upstream README's own estimator: Astra at $10 / $1 (cached) /
$50 per million input / cached input / output tokens, Flash at DeepSeek's
peak rates ($0.30 / $0.006 / $1.20).

## Results

| Task | Arm | Runs | Accept | Suite | Wall s | Astra input (cached) | Astra out | Flash in / out | Est. $ |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| lectern-sessions-cli | All-Astra | 2 | 2/2 | 2/2 | 170 | 303k (259k) | 4,336 | 0k / 0k | 0.91 |
| lectern-sessions-cli | astra-flash-orchestrator | 2 | 2/2 | 2/2 | 367 | 880k (843k) | 3,760 | 3043k / 40k | 1.48 |
| lectern-sessions-cli | Lectern delegated build | 2 | 2/2 | 2/2 | 353 | 611k (562k) | 2,408 | 3154k / 41k | 1.25 |
| lectern-tasks-cli | All-Astra | 2 | 2/2 | 2/2 | 249 | 450k (406k) | 6,434 | 0k / 0k | 1.17 |
| lectern-tasks-cli | astra-flash-orchestrator | 2 | 2/2 | 2/2 | 362 | 856k (811k) | 3,529 | 2719k / 39k | 1.52 |
| lectern-tasks-cli | Lectern delegated build | 2 | 2/2 | 2/2 | 535 | 990k (898k) | 2,846 | 5673k / 65k | 2.10 |
| librarr-source-health | All-Astra | 2 | 2/2 | 2/2 | 219 | 450k (410k) | 5,248 | 0k / 0k | 1.06 |
| librarr-source-health | astra-flash-orchestrator | 2 | 2/2 | 2/2 | 304 | 613k (570k) | 2,921 | 1951k / 33k | 1.20 |
| librarr-source-health | Lectern delegated build | 2 | 2/2 | 2/2 | 448 | 729k (676k) | 2,796 | 2894k / 41k | 1.43 |
| receipt-stats | All-Astra | 2 | 2/2 | 2/2 | 87 | 151k (127k) | 2,253 | 0k / 0k | 0.48 |
| receipt-stats | astra-flash-orchestrator | 2 | 2/2 | 2/2 | 190 | 444k (417k) | 2,252 | 1230k / 16k | 0.84 |
| receipt-stats | Lectern delegated build | 2 | 2/2 | 2/2 | 182 | 411k (376k) | 1,802 | 1074k / 17k | 0.85 |

| Arm | Tasks | Accept rate | Suite rate | Mean wall s | Mean Astra input (cached) | Mean Astra out | Mean Flash in / out | Mean est. $ |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| All-Astra | 4 | 100% | 100% | 181 | 338k (301k) | 4,568 | 0k / 0k | 0.91 |
| astra-flash-orchestrator | 4 | 100% | 100% | 305 | 698k (660k) | 3,115 | 2236k / 32k | 1.26 |
| Lectern delegated build | 4 | 100% | 100% | 379 | 685k (628k) | 2,463 | 3199k / 41k | 1.41 |

Lectern vs upstream workflow (per-task means over 4 tasks): Astra input -2%, Astra output -21%, Flash input +43%, wall +24%, est. $ +12%.
Lectern vs all-Astra: Astra input +103% (uncached +52%), Astra output -46%, wall +109%.

## What the numbers say

**Quality is equal.** Every run of every arm passed its hidden acceptance
test and its repository's suite: 24 of 24. Delegating the implementation to
Flash lost nothing on these tasks, in either workflow.

**Lectern's transport matches the upstream workflow on the lead's tokens.**
Per-task means over four tasks: Astra input −2%, Astra output −21%. On two of
the four tasks Lectern used less Astra input and cost less; on the other two
it used more, chiefly because a Lectern run on `lectern-tasks-cli` went
through a correction cycle (the worker missed the `no diff` contract, the
lead caught it in review and sent it back once), which is the workflow doing
its job and is paid for in worker tokens and wall time. With two runs per
cell the run-to-run variance of the worker alone is 2× (the upstream arm's
two `lectern-tasks-cli` runs used 3.7M and 1.8M Flash tokens for the same
brief), so a 12% cost difference between the two delegating arms is inside
the noise; the 21% output difference and the 100% quality are not.

**Neither delegating workflow beats the lead working alone at this task
size.** All-Astra was cheaper (est. $0.91 vs $1.26 and $1.41 per task) and
faster (181 s vs 305 s and 379 s). Delegation halved the lead's *output*
tokens (4.6k → 3.1k → 2.5k) but roughly doubled its *input* tokens, because
the lead still reads the repository to write the brief, then reads the diff
and the report to review it, and Codex samples the lead a few more times
while it waits. At $1 per million cached input tokens that input is most of
the cost. The upstream project's headline (98.9% less Astra input per 1,000
lines) was measured on a build of tens of thousands of lines; these tasks
are 120–190 lines of diff. Where the implementation loop is the bulk of the
work, the arithmetic changes; here it is not.

**What this does buy**, and why the feature exists: the lead's output tokens,
its wall-clock attention, and a reviewable unit of work. In the Lectern arm
the lead's tree is untouched until it accepts, the worker's patch is a diff
with a report next to it, and the correction cycle resumes the same worker
in the same tree. That is the part the benchmark cannot price.

## Not measured

- Larger builds, where the upstream numbers come from.
- A Claude Code lead (the tools are agent-agnostic; the runs were Codex).
- More than two runs per cell.
