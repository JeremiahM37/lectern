package replay

import "fmt"

// PreFilter is the cheap first pass over a PR's metadata alone — size cap,
// docs-only, and "is there anything to check at all" — everything the
// import pipeline can decide without a diff fetch. skipReason is empty when
// the PR should proceed to the expensive phase (resolve the parent commit,
// fetch the diff, build the prompt) — see BuildCase.
//
// Filters run in this order because each is cheaper to explain than the
// last: a human scanning a skip list reads "too large" before "docs-only"
// before the more surprising "no runnable check" (which depends on the
// PROJECT's own config, not just this one PR).
func PreFilter(pr PR, projectVerifyCmd string, opts Options) (skipReason, lang string, testFiles []string) {
	changed := ChangedLines(pr.Files)
	if changed > opts.maxChangedLines() {
		return fmt.Sprintf("too large: %d changed lines (cap %d)", changed, opts.maxChangedLines()), "", nil
	}
	if IsDocsOnly(pr.Files) {
		return "docs-only change", "", nil
	}
	lang, testFiles = TestFiles(pr.Files)
	if lang == "" && projectVerifyCmd == "" {
		return "no runnable check: the project has no verify command and this PR touched no recognizable test files",
			"", nil
	}
	return "", lang, testFiles
}

// BuildCase finishes a PR that passed PreFilter into a ready-to-persist
// Case, once the caller has resolved its parent commit and fetched its
// reference diff (the two things that need a git call — see
// internal/api/replay.go). check_command is the project's own check plus,
// when test files were detected, a command that runs exactly those tests;
// when no test files were touched, check_command is left as the project's
// alone (per docs/evals.md, an empty case check_command already falls back
// to the project's own verify command, so "just the project check" needs no
// special-casing here).
func BuildCase(pr PR, projectVerifyCmd, lang string, testFiles []string, parentSHA, diff string) Case {
	testCmd := TestCommand(lang, testFiles, diff)
	checkCmd := testCmd
	if testCmd != "" && projectVerifyCmd != "" {
		checkCmd = projectVerifyCmd + " && " + testCmd
	}
	prompt := RedactLeakedLines(BuildPrompt(pr), diff)
	return Case{
		Name:           fmt.Sprintf("PR #%d: %s", pr.Number, clip(pr.Title, 80)),
		Prompt:         prompt,
		BaseRef:        parentSHA,
		CheckCommand:   checkCmd,
		TimeoutS:       900,
		SourcePRNumber: pr.Number,
		ReferenceDiff:  diff,
		MatchedTests:   testFiles,
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
