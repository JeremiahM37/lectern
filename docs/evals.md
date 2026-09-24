# Agent tests ("evals")

Evals are repeatable test suites for agents: a set of **cases** (a prompt plus
a way to check the result), run against one or more **variants** (agent,
model, permission mode) for however many **repeats** you want, so you can
answer "does model X actually do better than model Y on tasks like this" with
a matrix instead of a vibe.

A run is `cases × variants × repeats`. Every cell is dispatched as an ordinary
task attempt in its own worktree — the same Best-of-N machinery that powers
`POST /api/tasks/{id}/dispatch`'s `variants` array — so an eval cell gets a
real agent run, a real diff, and a real check, not a simulation. Eval-generated
tasks carry the label `eval` and `created_by: "eval"` so they don't clutter the
normal board; open them from the Agent tests page (More → Agent tests) instead.

## Defining a suite

### In the UI

Agent tests → pick a project → **+ Create suite** → add cases. Each case has:

| Field | Meaning |
|---|---|
| `name` | Shown in the matrix header |
| `prompt` | What the agent is asked to do |
| `base_ref` | Branch/ref to start the worktree from (empty = project default) |
| `check_command` | Shell command run in the worktree after the agent finishes; exit 0 = pass. Empty falls back to the **project's** own verify command (`Project.VerifyCmd`); if neither is set, the case passes whenever the agent's own run succeeds |
| `setup_command` | Optional; folded into the prompt as an instruction the agent runs first (there is no host-side pre-step — see Limitations) |
| `timeout_s` | Stored per case; **not yet host-enforced** (see Limitations) |

### From the repo: `.lectern/evals/*.yaml`

Put one or more YAML files under `.lectern/evals/` in the project's repo (read
**on the target**, not the control plane — a remote project's suite files live
on its own machine) and click **Import from repo**. Re-importing replaces any
existing suite of the same name, so editing the file and re-importing is safe.

```yaml
name: Health endpoint suite
description: Checks the agent can add a working health endpoint
cases:
  - name: add health endpoint
    # quote a prompt that contains a colon or braces — plain YAML parses
    # `{"ok": true}` as a flow mapping otherwise
    prompt: 'Add a GET /health endpoint that returns {"ok": true}'
    base_ref: main
    check_command: pytest tests/test_health.py
    timeout_s: 600
  - name: handle missing route gracefully
    prompt: Make unknown routes return a 404 with a JSON body
    check_command: pytest tests/test_404.py
```

Fields match the table above 1:1; `name` and `prompt` are required on every
case, `base_ref`/`check_command`/`setup_command` default to empty and
`timeout_s` defaults to 900.

## Running a suite

Open a suite → **New run** → add variants (agent + model + permission mode,
1–8 of them — the same shape a Best-of-N dispatch's `variants` array takes) →
set **repeats** → **Run suite**. A concurrency setting (`eval_concurrency` in
Settings, default 2) caps how many cells are in flight for a run at once, so a
20-cell run doesn't try to launch 20 agents simultaneously; the scheduler's own
per-target `max_concurrent` still applies on top of that.

A run can be **cancelled** mid-flight: every cell still queued or running is
stopped and marked `error`, and the run reads as finished rather than stuck.

## Reading the results

- **Matrix**: cases (rows) × variants (columns), each cell showing
  `passed/total` across repeats. Click a cell to see that attempt's check
  output and diff.
- **Leaderboard**: per-variant pass rate, mean duration, total cost, mean
  input/output tokens.
- **Compare two runs**: pick two runs of the same suite and see which cases
  regressed or improved (by pass-rate delta).

## API

```
GET/POST      /api/evals/suites            (?project_id= to filter)
POST          /api/evals/suites/import     {project_id}
GET/DELETE    /api/evals/suites/{id}       (GET returns {suite, cases})
POST          /api/evals/suites/{id}/cases
DELETE        /api/evals/cases/{id}
GET/POST      /api/evals/suites/{id}/runs  (POST body: {variants, repeats, notes})
GET           /api/evals/runs/{id}         ({run, suite, cases, variants, results, leaderboard})
POST          /api/evals/runs/{id}/cancel
GET           /api/evals/runs/{id}/compare/{other}
```

## Limitations (known, not yet done)

- `timeout_s` is stored per case but nothing currently kills a cell's agent
  run early because of it — a hung agent hangs the cell until it exits on its
  own (or the run is cancelled by hand).
- `setup_command` is not run as a host-side step before the agent starts; it
  is prepended to the prompt as an instruction ("before doing anything else,
  run this command…"), so it depends on the agent actually following it.
- A cell's check runs via the same task-level auto-verify path Best-of-N
  uses (`internal/scheduler`'s `captureAndFinalize`), with the case's
  `check_command` overriding the project's for that one task
  (`store.Task.CheckCommand`) — it is not a separate sandboxed check step.
