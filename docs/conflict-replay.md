# Conflict replay

`tools/conflict_replay.py` measures how often two lines of work that were developed
concurrently actually conflict, using the repository's own merge history as the
ground truth. It is an evaluation tool for conflict prediction. It does not change
the repository and nothing in Lectern runs it.

## What it does

For each two-parent merge on the first-parent history of the checkout, newest
first, it redoes the merge from the merge-base and records:

- whether git reports a textual conflict, and in which paths;
- for merges with no textual conflict, whether each side alone and the merged tree
  pass `go build ./...`, `go vet` and `go test` on the packages touched by either
  side, and so whether the merge is a semantic conflict.

Each row also keeps both side diffs and the file lists, so the same rows can be
scored by plain file overlap and by symbol-level prediction.

## How to run it

```
python3 -I tools/conflict_replay.py --repo PATH --out FILE.jsonl --limit N \
    [--timeout 600] [--since 2026-01-01] [--tmp-root DIR]
```

`--out` should sit outside the repository, because each row holds full diffs.
Builds run in detached temporary worktrees under `--tmp-root` (default
`/mnt/bulk/lectern-research/replay-tmp`). Each is removed when its check ends.
Tests for the tool are in `tools/test_conflict_replay.py` and run with
`python3 -I tools/test_conflict_replay.py`.

## What it does NOT do

- It does not test the whole module. Only packages touched by a side are tested,
  so a break in an untouched package that the merge reaches is missed.
  `go build ./...` still covers the module.
- It does not run tests of packages whose `_test.go` files mention tmux. Those
  tests refuse to run outside the reviewed isolated runner. Such packages are still
  built and vetted, and the exclusion is recorded in each row's `notes`.
- It does not judge merges made by other people or on other branches. It walks only
  the first-parent history of the current checkout.
- It skips merges where either side changes no `.go` file, or where a side diff
  exceeds 3000 changed lines.
- A merge is only tested on the merged tree when both sides passed alone, so
  `merged_ok` is `null` in that case.
