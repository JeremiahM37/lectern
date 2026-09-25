# Replay evals

Replay evals answer "which agent/model is best for **my** repository" by
building an eval suite (see [Agent tests](evals.md) for the base concept)
straight out of the project's own merged-PR history, instead of a
hand-written prompt. Each case replays one real PR: the agent starts from
the commit right before that PR landed, gets the PR's own stated intent as
its prompt, and is graded against the PR's own accepted diff as ground
truth — plus, optionally, whatever tests that PR itself added.

This is the same [Agent tests](evals.md) machinery underneath — a replay
case is a `store.EvalCase` like any other, a replay run is
`cases × variants × repeats` like any other — with three additions: where
the cases come from, what a result is scored against beyond pass/fail, and
an optional judge.

## Importing a suite

Agent tests → pick a project → **+ New replay suite from merged PRs** →
set:

| Field | Meaning |
|---|---|
| PRs to consider | How many of the project's most recent merged PRs to look at (default 20, max 100) |
| Max changed lines | Size cap — a PR that changed more lines than this (additions + deletions) is skipped (default 400) |

**Preview** runs the whole pipeline without persisting anything: every PR
considered shows up either accepted (with a preview of the case it would
become — name, prompt, check command, matched test files) or skipped with a
specific reason. Filters run in this order:

1. **Too large** — more than the changed-line cap.
2. **Docs-only** — every touched file is documentation (markdown/rst/txt,
   anything under a `doc/`/`docs/` directory, or a well-known unversioned
   file like `LICENSE`/`CHANGELOG`/`README` with or without an extension).
3. **No runnable check** — the project has no verify command (`Project.VerifyCmd`)
   *and* the PR touched no recognizable test file. Without one of those two,
   a replay cell would trivially "pass" no matter what the agent did.

Once you're happy with the preview, name the suite and **Create suite** —
every accepted candidate becomes a replay case (`is_replay = true`,
`source_pr_number`, `reference_diff` — the PR's own diff, stored for
scoring). Each import creates a brand-new suite rather than replacing one of
the same name (unlike `.lectern/evals/*.yaml` import) — re-running against a
fast-moving repo is expected to produce a fresh suite with the newest PRs,
not silently overwrite an older one someone may still be comparing runs
against.

### Where the data comes from

Everything runs on the **target**, never the control plane — same rule as
YAML suite import. The pipeline tries `gh pr list --state merged --json
number,title,body,mergeCommit,baseRefName,files,closingIssuesReferences`
first. If `gh` is unavailable (not installed, not logged in, or the repo has
no GitHub remote — anything that makes the command fail), it falls back to
walking `git log --merges --first-parent`, recovering the PR number and
title from GitHub's default merge-commit message shape. The fallback has no
PR metadata beyond the commit history itself: no linked issues, no base
branch name, and (unlike `gh`, which gives file stats for free) it needs one
extra `git diff --numstat` per candidate just to run the cheap filters.
Either way, `base_ref` for the built case is the merge's **first parent** —
resolved via `git rev-parse <merge-sha>^1`, which lands on "the base branch
tip right before this PR" whether the PR was merged, squashed, or rebased.

### Detected test commands

When a PR added or changed test files, the case's `check_command` becomes
the project's own check plus a command that runs *exactly those tests* —
never the whole suite, and never nothing:

| Language | Detected by | Command |
|---|---|---|
| Go | `*_test.go` | `go test <touched packages> -run '^(Name1\|Name2)$'` — exact `Test` func names are read from the reference diff's own added lines (`^\+func Test\w+\(`), so no extra fetch beyond the diff scoring already needs |
| Python | `test_*.py` / `*_test.py` | `pytest <the matched files>` |
| JS/TS | `*.test.{js,ts,jsx,tsx}` / `*.spec.{js,ts,jsx,tsx}` | `npx jest <the matched files>` |

When no test files were touched, `check_command` is left empty — an empty
case `check_command` already falls back to the project's own verify command
(see [Agent tests](evals.md)), so there is nothing to add.

## Leakage: what the agent is told, and what it never sees

A replay case's prompt is built from the PR's own **title + body**, plus any
**linked issue's** title + body (`closingIssuesReferences`) — never the
diff, never any file's content. That alone means there is no code to leak:
the only inputs are text GitHub already associates with "what was this PR
trying to do".

The real risk is that a PR's own description sometimes *contains* a pasted
snippet of the fix — someone showing their work in the PR body. Two
independent passes guard against that reaching the prompt:

1. **`StripSolutionDetails`** removes fenced code blocks (` ``` `) and
   anything shaped like a pasted unified diff (a `diff --git`/`@@`/`---`/`+++`
   line and the `+`/`-`/` ` lines that follow it), fenced or not.
2. **`RedactLeakedLines`** — a second, independent check that runs *after*
   the first and does not trust it: it compares the (already-stripped)
   prompt against the PR's **own accepted diff**, line by line, and redacts
   any exact match of 8 characters or more (shorter lines are too generic to
   count — `}` or `return` alone would false-positive on ordinary prose).
   This catches what the first pass's heuristics might miss — a snippet
   pasted inline, without a fence, not shaped enough to look like a diff.

Neither pass is claimed to be perfect on its own; the point of having two
independent mechanisms is that a gap in one is unlikely to be a gap in both.
`check_command` is not part of this guard — the agent never sees it, so a
test's exact name is not a leak the way a line of the fix itself would be.

If you are importing PRs from a project where descriptions routinely paste
large blocks of the actual fix, treat the leakage guard as a safety net, not
a guarantee: read a few resulting prompts before trusting a suite's results
as a real measure of an agent solving the problem cold.

## Scoring

Every replay cell is graded like any other eval cell first — pass/fail via
`check_command` (or the project's fallback). On top of that, once a cell
reaches a terminal pass/fail, its attempt's diff is compared against the
case's reference diff:

| Score | Meaning |
|---|---|
| `similarity_files` | Jaccard index of the two diffs' touched-file sets — did the attempt change the same files the accepted fix changed |
| `similarity_lines` | Jaccard index of the two diffs' changed-line content sets (added/removed lines, whitespace-trimmed) — a coarse "how much of the actual edit matches", not an AST-aware diff |
| `size_ratio` | attempt-changed-lines ÷ reference-changed-lines. Around 1.0 means "changed about as much as the accepted fix"; well above suggests scope creep, well below suggests a partial fix |

**These are structural, not semantic.** A correct fix with different
variable names, different statement order, or a genuinely different but
equally valid approach can score low on `similarity_lines` while being
completely right — and a fix that copies the reference's shape almost
exactly can still be wrong in a way line-overlap cannot see (e.g. an
off-by-one that happens to touch the same lines). Treat these as *evidence*,
not a verdict, especially at the level of a single cell; they are most
useful aggregated across many cells in the leaderboard, where consistent
high or low overlap for one variant is a real signal even though any one
cell's number is noisy.

### The judge (optional)

Turning on **Run judge** for a run spawns a headless judge over every
replay cell once it finishes — the same judge machinery
[Best-of-N](evals.md) uses (`judge_agent`/`judge_model` settings, a cheap
model by default), shown the case's reference diff next to the attempt's
diff and asked one question: *does the attempt solve the same problem as
the reference, judged by intent and outcome, not exact shape*. Its answer
(`judge_status`, `judge_match`, `judge_reason`) lands back on the same
`eval_results` row once the judge task finishes — asynchronously, so a run
can reach `done` before every cell's judge verdict has landed; refresh the
run view to see them fill in.

The judge is best-effort: an unconfigured or unusable `judge_agent` is
logged and skipped rather than failing the cell's own grading, which has
already committed by that point. A cell's `similarity_*` scores and
pass/fail never depend on the judge running at all.

## Reading the results

- **Result view**: a replay cell's detail panel shows its similarity scores,
  judge verdict (if run), and the **reference diff next to the attempt's
  own diff** — the same view a human reviewer would want.
- **Leaderboard**: gains a *Similarity to reference* column (mean
  `similarity_files`/`similarity_lines` across the run's scored cells) and a
  *Matches reference* column (the judge's match rate across cells it
  actually judged) alongside the existing pass rate, duration, cost and
  token columns. Both are averaged only over cells that actually carry a
  score, so a suite mixing ordinary and replay cases — or a run still in
  flight — is never diluted by cells with nothing to average.

## API

```
POST /api/evals/replay/preview   {project_id, n, max_changed_lines}
                                  -> {source: "gh"|"git-log", candidates: [...]}
POST /api/evals/replay/suites    {project_id, n, max_changed_lines, name}
                                  -> {suite, source, candidates: [...]}
```

`candidates[]` entries: `{pr_number, title, accepted, skip_reason,
changed_lines, case?: {name, prompt, base_ref, check_command,
matched_tests}}`. A run against a replay suite takes the same
`POST /api/evals/suites/{id}/runs` body as any suite, plus an optional
`with_judge: true`.
