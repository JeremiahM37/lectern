// Package replay builds eval cases from a project's own merged-PR history —
// "replay evals" (docs/replay-evals.md): the answer to "which agent/model is
// best for MY repository" ground-truthed against what actually landed, not a
// hand-written prompt. Everything in this package is pure (no DB, no
// network, no target/executor) so it is unit-testable from fixtures; the
// orchestration that fetches PRs and diffs from a real target lives in
// internal/api/replay.go, which calls into this package once the data is in
// hand.
package replay

// FileStat is one file touched by a PR (or, from the git-log fallback, one
// first-parent merge commit standing in for a PR), with line-change counts.
// It is exactly gh's `files` JSON shape; the fallback path fills the same
// shape from `git diff --numstat`.
type FileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// Issue is one issue linked to a PR (gh's closingIssuesReferences) — its
// title and body feed the prompt exactly like the PR's own, since "fixes
// #123" issues are often where the actual problem statement lives.
type Issue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// PR is one merged PR ready to become a replay case, however it was
// fetched: `gh pr list --json ...` (ParseGHPRList) or, when gh is
// unavailable on the target, walking first-parent merge commits
// (ParseMergeLog).
type PR struct {
	Number      int
	Title       string
	Body        string
	MergeSHA    string
	BaseRefName string
	Files       []FileStat
	Issues      []Issue // empty in the git-log fallback — no PR metadata to link from
}

// Options configures candidate filtering. The zero value is usable —
// MaxChangedLines <= 0 means DefaultMaxChangedLines.
type Options struct {
	MaxChangedLines int
}

// DefaultMaxChangedLines is the size cap applied when Options.MaxChangedLines
// is unset: a PR that changed more than this many lines (additions +
// deletions) is skipped as "too large" rather than becoming a case an agent
// has little realistic chance of reproducing from a prompt alone.
const DefaultMaxChangedLines = 400

func (o Options) maxChangedLines() int {
	if o.MaxChangedLines <= 0 {
		return DefaultMaxChangedLines
	}
	return o.MaxChangedLines
}

// Case is a built replay eval case, ready to be persisted as a
// store.EvalCase with IsReplay set.
type Case struct {
	Name           string
	Prompt         string
	BaseRef        string // the merge's first parent — the repo state before the PR landed
	CheckCommand   string
	TimeoutS       int
	SourcePRNumber int
	ReferenceDiff  string
	MatchedTests   []string // test files the PR touched, for display in the preview
}

// Candidate is one PR's outcome of the import pipeline: either an accepted
// Case or a reason it was skipped, so the preview UI can show both in one
// list — see docs/replay-evals.md.
type Candidate struct {
	PR           PR
	Accepted     bool
	SkipReason   string
	Case         *Case
	ChangedLines int
}
