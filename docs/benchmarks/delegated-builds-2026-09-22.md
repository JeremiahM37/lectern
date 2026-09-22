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

Four arms, two runs each per task (32 runs):

- **All-Astra**: the lead does the task itself. No skill.
- **astra-flash-orchestrator (native subagent)**: the upstream package
  installed by its own installer, unmodified, with codex-router in front of
  Codex (signed in with the ChatGPT account, the DeepSeek Flash route unhidden
  and selected as a subagent), so the worker is Codex's native `spawn_agent`
  child on `deepseek/deepseek-v4.1-flash`, exactly as designed. This is the
  faithful baseline.
- **astra-flash-orchestrator (process worker)**: the same skill, policy and
  templates with the worker as a separate `codex exec` process on Flash, run
  before the native path was working here. Kept for the record; it is the
  same workflow on a heavier transport.
- **Lectern delegated build**: the same lead model with the `lectern-delegate`
  skill and the `lectern` MCP server; the worker runs as a Lectern task in a
  worktree, on the same Flash model with Lectern's worker preamble.

A correction, kept in the open: my first attempt at the native path failed
and I wrote that Codex refuses a non-OpenAI subagent on a ChatGPT login. That
was wrong. The child had been sent to OpenAI because the role named a bare
model id without the router, and codex-router's `certify` probe defers for
its own reasons; with the route unhidden (`picker set … show`) and selected
(`subagents set … on`) the native path works, and the upstream installer's
preflight passes. `bench/delegated-builds/ROUTER-FINDING.md` has the details.

Grading is the hidden acceptance test plus the repository suite, both run
after the agent finishes. One suite run flaked (a real-process concurrency
test, graded while three runs shared the box) and passed twice on regrade;
the record says so. Token counts are the sessions' cumulative usage: for the
native arm the child thread's record is the one whose turns ran on the routed
model; for the other arms the worker ran in its own Codex home. A root that
had been routed to another model would show up as a stray; none did.

Cost uses the upstream README's own estimator: Astra at $10 / $1 (cached) /
$50 per million input / cached input / output tokens, Flash at DeepSeek's
peak rates ($0.30 / $0.006 / $1.20).

## Results

| Task | Arm | Runs | Accept | Suite | Wall s | Astra input (cached) | Astra out | Flash in / out | Est. $ |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|
| lectern-sessions-cli | All-Astra | 2 | 2/2 | 2/2 | 170 | 303k (259k) | 4,336 | 0k / 0k | 0.91 |
| lectern-sessions-cli | astra-flash-orchestrator (process worker) | 2 | 2/2 | 2/2 | 367 | 880k (843k) | 3,760 | 3043k / 40k | 1.48 |
| lectern-sessions-cli | astra-flash-orchestrator (native subagent) | 2 | 2/2 | 2/2 | 487 | 577k (534k) | 2,798 | 5463k / 37k | 1.20 |
| lectern-sessions-cli | Lectern delegated build | 2 | 2/2 | 2/2 | 384 | 807k (758k) | 2,744 | 4357k / 54k | 1.49 |
| lectern-tasks-cli | All-Astra | 2 | 2/2 | 2/2 | 249 | 450k (406k) | 6,434 | 0k / 0k | 1.17 |
| lectern-tasks-cli | astra-flash-orchestrator (process worker) | 2 | 2/2 | 2/2 | 362 | 856k (811k) | 3,529 | 2719k / 39k | 1.52 |
| lectern-tasks-cli | astra-flash-orchestrator (native subagent) | 2 | 2/2 | 2/2 | 403 | 623k (580k) | 2,598 | 5708k / 36k | 1.23 |
| lectern-tasks-cli | Lectern delegated build | 2 | 2/2 | 2/2 | 444 | 592k (550k) | 2,628 | 4133k / 62k | 1.21 |
| librarr-source-health | All-Astra | 2 | 2/2 | 2/2 | 219 | 450k (410k) | 5,248 | 0k / 0k | 1.06 |
| librarr-source-health | astra-flash-orchestrator (process worker) | 2 | 2/2 | 2/2 | 304 | 613k (570k) | 2,921 | 1951k / 33k | 1.20 |
| librarr-source-health | astra-flash-orchestrator (native subagent) | 2 | 2/2 | 2/2 | 304 | 484k (440k) | 2,652 | 4377k / 30k | 1.08 |
| librarr-source-health | Lectern delegated build | 2 | 2/2 | 2/2 | 343 | 608k (565k) | 2,470 | 2237k / 36k | 1.19 |
| receipt-stats | All-Astra | 2 | 2/2 | 2/2 | 87 | 151k (127k) | 2,253 | 0k / 0k | 0.48 |
| receipt-stats | astra-flash-orchestrator (process worker) | 2 | 2/2 | 2/2 | 190 | 444k (417k) | 2,252 | 1230k / 16k | 0.84 |
| receipt-stats | astra-flash-orchestrator (native subagent) | 2 | 2/2 | 2/2 | 156 | 287k (250k) | 1,412 | 2406k / 11k | 0.74 |
| receipt-stats | Lectern delegated build | 2 | 2/2 | 2/2 | 142 | 358k (332k) | 1,698 | 814k / 12k | 0.71 |

| Arm | Tasks | Accept rate | Suite rate | Mean wall s | Mean Astra input (cached) | Mean Astra out | Mean Flash in / out | Mean est. $ |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| All-Astra | 4 | 100% | 100% | 181 | 338k (301k) | 4,568 | 0k / 0k | 0.91 |
| astra-flash-orchestrator (process worker) | 4 | 100% | 100% | 305 | 698k (660k) | 3,115 | 2236k / 32k | 1.26 |
| astra-flash-orchestrator (native subagent) | 4 | 100% | 100% | 337 | 493k (451k) | 2,365 | 4489k / 28k | 1.06 |
| Lectern delegated build | 4 | 100% | 100% | 329 | 591k (551k) | 2,385 | 2885k / 41k | 1.15 |

Lectern vs upstream native workflow (per-task means over 4 tasks): Astra input +20%, Astra output +1%, Flash input -36%, wall -3%, est. $ +8%.
Upstream native vs all-Astra: Astra input +46%, Astra output -48%, wall +86%, est. $ +17%.
Lectern vs all-Astra: Astra input +75% (uncached +6%), Astra output -48%, wall +81%.

Astra input per 1,000 inserted lines (the upstream README's metric): All-Astra 2.83M, astra-flash-orchestrator (process worker) 4.58M, astra-flash-orchestrator (native subagent) 2.33M, Lectern delegated build 2.76M. Upstream's own baseline was 8.56M and its thin phase 0.096M.


## What the numbers say

**Quality is equal.** Every run of every arm passed its hidden acceptance
test and its repository's suite: 32 of 32. Delegating the implementation to
Flash lost nothing on these tasks, on any transport.

**Against the upstream workflow run as designed, Lectern's transport ties on
the lead's output tokens (+1%) and on wall time (−3%), uses 36% fewer Flash
tokens, and uses 20% more of the lead's input tokens, for an estimated cost
8% higher.** Per task it is 2–2: Lectern used less lead input on
`lectern-tasks-cli` and about the same on `receipt-stats`, and more on the
other two. With two runs per cell the worker alone varies 2× run to run, so
the cost difference is inside the noise; the input difference is not, and it
has a cause: an MCP round trip costs a full sample of the lead's context,
while a native child shares the parent's cached prefix and is waited on by a
built-in. Two of those round trips were removed during this work
(`delegate_build` now finds the project from the checkout path and inlines a
small patch), which is what took Lectern from +40% to +20%.

**On the upstream README's own metric, Astra input per 1,000 lines,** the
native workflow used 2.33M here against 2.83M for the lead alone: an 18%
reduction, real but two orders of magnitude from the 98.9% in its README.
Their baseline burned 8.56M per 1,000 lines because that phase included
research, browser and deployment work that produced no code, and their thin
phase's 0.096M per 1,000 lines belongs to a 48,000-line build where one brief
yields thousands of lines. At 120–190-line tasks the lead still reads the
repository to write the brief and reads the diff to review it, so its input
roughly doubles while its output halves; in dollars, the lead alone was
cheapest on every task ($0.91 vs $1.06 native vs $1.15 Lectern) and fastest
(181 s vs 337 s vs 329 s).

**What this does buy**, and why the feature exists: the lead's output tokens
(−48%), its wall-clock attention, and a reviewable unit of work. In the
Lectern arm the lead's tree is untouched until it accepts, the worker's patch
is a diff with a report next to it, the correction cycle resumes the same
worker in the same tree, and none of it depends on Codex or on a router: the
same tools drive a Claude Code lead and any worker with a task definition.

## Not measured

- Larger builds, where the upstream numbers come from.
- A Claude Code lead (the tools are agent-agnostic; the runs were Codex).
- More than two runs per cell.
