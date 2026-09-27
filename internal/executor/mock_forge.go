package executor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// mockForge is a small scripted GitHub for demo mode and the hermetic
// suites: the repository mock/repo with a handful of pull requests and
// issues, answering the gh calls internal/trackers makes (and the CI loop's
// reads for these PRs). Merges, reviewer and label edits, comments and
// closing change its state, so a test can see an action land.
//
// PR 12 is green and approved; 14 is stacked on 12 and has a failing check;
// 15 conflicts with main; 17 is a draft still running checks. PR 7 is left
// to the older generic gh answers the CI loop tests rely on.
type mockForge struct {
	mu     sync.Mutex
	prs    map[int]*mockPR
	issues map[int]*mockIssue
}

type mockPR struct {
	Number     int
	Title      string
	Body       string
	Head, Base string
	SHA        string
	State      string // OPEN | CLOSED | MERGED
	Draft      bool
	Author     string
	Labels     []string
	Requested  []string
	Reviews    [][2]string // login, state
	Checks     [][3]string // name, conclusion (SUCCESS|FAILURE|"" pending), run/job link
	Conflicts  []string
	Comments   [][2]string // author, body
	AutoMerge  string
	Updated    string
	MergedWith string
}

type mockIssue struct {
	Number   int
	Title    string
	Body     string
	State    string
	Author   string
	Labels   []string
	Comments [][2]string
	Updated  string
}

var mockLabelColors = map[string]string{"enhancement": "a2eeef", "bug": "d73a4a", "docs": "0075ca", "refactor": "cfd3d7", "needs-review": "fbca04"}

func newMockForge() *mockForge {
	run := func(r, j int) string { return fmt.Sprintf("https://github.com/mock/repo/actions/runs/%d/job/%d", r, j) }
	return &mockForge{
		prs: map[int]*mockPR{
			12: {Number: 12, Title: "Add a retry budget to the sync worker", Head: "feature/retry-budget", Base: "main",
				SHA: "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678", State: "OPEN", Author: "sam", Labels: []string{"enhancement"},
				Body:      "Sync retried forever on a flaky upstream. This caps retries per window and backs off.\n\n- adds `RetryBudget`\n- tests for exhaustion and refill",
				Requested: []string{"riley"}, Reviews: [][2]string{{"jo", "APPROVED"}},
				Checks:   [][3]string{{"lint", "SUCCESS", run(101, 201)}, {"unit tests", "SUCCESS", run(101, 202)}},
				Comments: [][2]string{{"jo", "Looks good. The refill test is exactly what I wanted."}, {"sam", "Thanks — merging once riley has a look."}},
				Updated:  "2026-09-26T16:20:00Z"},
			14: {Number: 14, Title: "Stream sync progress to the dashboard", Head: "feature/sync-progress", Base: "feature/retry-budget",
				SHA: "b2c3d4e5f60718293a4b5c6d7e8f901234567891", State: "OPEN", Author: "sam", Labels: []string{"enhancement"},
				Body:     "Builds on #12: reports retry-budget usage while a sync runs.",
				Checks:   [][3]string{{"lint", "SUCCESS", run(102, 203)}, {"unit tests", "FAILURE", run(102, 204)}},
				Comments: [][2]string{{"riley", "CI is red on the progress test."}},
				Updated:  "2026-09-26T18:05:00Z"},
			15: {Number: 15, Title: "Rename the config loader", Head: "refactor/config-loader", Base: "main",
				SHA: "c3d4e5f60718293a4b5c6d7e8f90123456789012", State: "OPEN", Author: "jo", Labels: []string{"refactor"},
				Body: "`load_config` becomes `Config.load`.", Checks: [][3]string{{"lint", "SUCCESS", run(103, 205)}},
				Conflicts: []string{"src/config.py", "README.md"}, Reviews: [][2]string{{"sam", "CHANGES_REQUESTED"}},
				Updated: "2026-09-25T11:40:00Z"},
			17: {Number: 17, Title: "WIP: dark mode for settings", Head: "ui/dark-settings", Base: "main",
				SHA: "d4e5f60718293a4b5c6d7e8f9012345678901234", State: "OPEN", Draft: true, Author: "riley",
				Checks: [][3]string{{"lint", "", run(104, 206)}}, Updated: "2026-09-24T09:15:00Z"},
		},
		issues: map[int]*mockIssue{
			3: {Number: 3, Title: "Sync stalls when the upstream token expires", State: "OPEN", Author: "riley", Labels: []string{"bug"},
				Body:     "After the token rotates, sync sits at 0% until restart.\n\nSteps: rotate the token, start a sync.",
				Comments: [][2]string{{"sam", "Reproduced on main."}}, Updated: "2026-09-26T12:00:00Z"},
			8: {Number: 8, Title: "Document the retry budget", State: "OPEN", Author: "jo", Labels: []string{"docs"},
				Body: "Explain the window and how to tune it.", Updated: "2026-09-23T08:30:00Z"},
		},
	}
}

func (m *Mock) forge() *mockForge {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forgeState == nil {
		m.forgeState = newMockForge()
	}
	return m.forgeState
}

var (
	mockForgeURLRe = regexp.MustCompile(`https://github\.com/mock/repo/pull/(\d+)`)
	mockRunRe      = regexp.MustCompile(`^gh run view (\d+) `)
)

// forgeCommand reports whether cmd is one mockForge answers.
func (f *mockForge) handles(cmd string) bool {
	switch {
	case strings.HasPrefix(cmd, "git -C ") && strings.HasSuffix(cmd, " remote get-url origin"):
		return true
	case strings.Contains(cmd, "--lectern-merge-tree--"):
		return true
	case strings.HasPrefix(cmd, "gh ") && strings.Contains(cmd, " -R mock/repo"):
		if m := mockRunRe.FindStringSubmatch(cmd); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n >= 101 && n <= 110
		}
		return true
	case strings.HasPrefix(cmd, "gh repo view mock/repo"), strings.HasPrefix(cmd, "gh api graphql"):
		return true
	case strings.HasPrefix(cmd, "gh pr view https://github.com/mock/repo/pull/"),
		strings.HasPrefix(cmd, "gh pr checks https://github.com/mock/repo/pull/"):
		m := mockForgeURLRe.FindStringSubmatch(cmd)
		if m == nil {
			return false
		}
		n, _ := strconv.Atoi(m[1])
		f.mu.Lock()
		defer f.mu.Unlock()
		_, ok := f.prs[n]
		return ok
	}
	return false
}

// shellWords splits a command built by trackers' command(): words separated
// by spaces, single-quoted where needed, with '\” for a quote.
func shellWords(cmd string) []string {
	var out []string
	var b strings.Builder
	in, started := false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\'':
			in, started = !in, true
		case c == ' ' && !in:
			if started {
				out = append(out, b.String())
				b.Reset()
				started = false
			}
		case c == '\\' && !in && i+1 < len(cmd):
			i++
			b.WriteByte(cmd[i])
			started = true
		default:
			b.WriteByte(c)
			started = true
		}
	}
	if started {
		out = append(out, b.String())
	}
	return out
}

func flagValue(args []string, name string) (string, bool) {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func mockJSON(v any) Result {
	b, _ := json.Marshal(v)
	return Result{0, string(b), ""}
}

func (f *mockForge) run(cmd string) Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case strings.HasSuffix(cmd, " remote get-url origin"):
		return Result{0, "https://github.com/mock/repo.git\n", ""}
	case strings.Contains(cmd, "--lectern-merge-tree--"):
		for n, p := range f.prs {
			if strings.Contains(cmd, fmt.Sprintf("refs/pull/%d/head", n)) && len(p.Conflicts) > 0 {
				return Result{0, "--lectern-merge-tree--\n4b825dc642cb6eb9a060e54bf8d69288fbee4904\n" + strings.Join(p.Conflicts, "\n") + "\nrc=1\n", ""}
			}
		}
		return Result{0, "--lectern-merge-tree--\n4b825dc642cb6eb9a060e54bf8d69288fbee4904\nrc=0\n", ""}
	case strings.HasPrefix(cmd, "gh api graphql"):
		return Result{0, `{"data":{"repository":{"autoMergeAllowed":true,"mergeQueue":null}}}`, ""}
	case strings.HasPrefix(cmd, "gh pr view https://"):
		n, _ := strconv.Atoi(mockForgeURLRe.FindStringSubmatch(cmd)[1])
		p := f.prs[n]
		return mockJSON(map[string]any{"state": p.State, "headRefOid": p.SHA})
	case strings.HasPrefix(cmd, "gh pr checks https://"):
		n, _ := strconv.Atoi(mockForgeURLRe.FindStringSubmatch(cmd)[1])
		var b strings.Builder
		for _, c := range f.prs[n].Checks {
			bucket := map[string]string{"SUCCESS": "pass", "FAILURE": "fail", "": "pending"}[c[1]]
			fmt.Fprintf(&b, "%s\t%s\t1m\t%s\t\n", c[0], bucket, c[2])
		}
		rc := 0
		if strings.Contains(b.String(), "\tfail\t") {
			rc = 8
		}
		return Result{rc, b.String(), ""}
	}
	args := shellWords(cmd)
	if len(args) < 3 {
		return Result{1, "", "mock gh: unexpected " + cmd}
	}
	switch args[1] + " " + args[2] {
	case "repo view":
		labels := []map[string]string{}
		names := make([]string, 0, len(mockLabelColors))
		for n := range mockLabelColors {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			labels = append(labels, map[string]string{"name": n, "color": mockLabelColors[n]})
		}
		return mockJSON(map[string]any{
			"mergeCommitAllowed": true, "squashMergeAllowed": true, "rebaseMergeAllowed": true,
			"deleteBranchOnMerge": true, "viewerPermission": "ADMIN", "viewerDefaultMergeMethod": "SQUASH",
			"labels": labels,
			"assignableUsers": []map[string]string{{"login": "jo", "name": "Jo Park"}, {"login": "riley", "name": "Riley Chen"},
				{"login": "sam", "name": "Sam Ortiz"}, {"login": "devon", "name": "Devon Hale"}},
		})
	case "label list":
		out := []map[string]string{}
		for n, c := range mockLabelColors {
			out = append(out, map[string]string{"name": n, "color": c})
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["name"] < out[j]["name"] })
		return mockJSON(out)
	case "pr list":
		state, _ := flagValue(args, "--state")
		search, _ := flagValue(args, "--search")
		nums := []int{}
		for n := range f.prs {
			nums = append(nums, n)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(nums)))
		out := []any{}
		for _, n := range nums {
			p := f.prs[n]
			if !mockStateMatch(p.State, state) || !mockSearch(p.Title, search) {
				continue
			}
			out = append(out, f.prJSON(p))
		}
		return mockJSON(out)
	case "issue list":
		state, _ := flagValue(args, "--state")
		search, _ := flagValue(args, "--search")
		nums := []int{}
		for n := range f.issues {
			nums = append(nums, n)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(nums)))
		out := []any{}
		for _, n := range nums {
			i := f.issues[n]
			if !mockStateMatch(i.State, state) || !mockSearch(i.Title, search) {
				continue
			}
			out = append(out, f.issueJSON(i))
		}
		return mockJSON(out)
	case "pr view":
		n, _ := strconv.Atoi(args[3])
		p, ok := f.prs[n]
		if !ok {
			return Result{1, "", "GraphQL: Could not resolve to a PullRequest with the number of " + args[3] + "."}
		}
		return mockJSON(f.prJSON(p))
	case "issue view":
		n, _ := strconv.Atoi(args[3])
		i, ok := f.issues[n]
		if !ok {
			return Result{1, "", "GraphQL: Could not resolve to an issue with the number of " + args[3] + "."}
		}
		return mockJSON(f.issueJSON(i))
	case "pr merge":
		n, _ := strconv.Atoi(args[3])
		p, ok := f.prs[n]
		if !ok || p.State != "OPEN" {
			return Result{1, "", "Pull request is not open"}
		}
		if hasFlag(args, "--disable-auto") {
			p.AutoMerge = ""
			return Result{0, "", ""}
		}
		if sha, ok := flagValue(args, "--match-head-commit"); ok && sha != p.SHA {
			return Result{1, "", "GraphQL: Head branch was modified. Review and try the merge again. (mergePullRequest)"}
		}
		method := "MERGE"
		for flag, m := range map[string]string{"--squash": "SQUASH", "--rebase": "REBASE"} {
			if hasFlag(args, flag) {
				method = m
			}
		}
		if hasFlag(args, "--auto") {
			p.AutoMerge = method
			return Result{0, fmt.Sprintf("✓ Pull request mock/repo#%d will be automatically merged via %s when all requirements are met\n", n, strings.ToLower(method)), ""}
		}
		if len(p.Conflicts) > 0 {
			return Result{1, "", "Pull request mock/repo#" + args[3] + " is not mergeable: the merge commit cannot be cleanly created."}
		}
		p.State, p.MergedWith, p.Updated = "MERGED", method, "2026-09-27T10:00:00Z"
		return Result{0, fmt.Sprintf("✓ Merged pull request mock/repo#%d (%s)\n", n, p.Title), ""}
	case "pr edit", "issue edit":
		n, _ := strconv.Atoi(args[3])
		var labels *[]string
		if args[1] == "pr" {
			p, ok := f.prs[n]
			if !ok {
				return Result{1, "", "no such pull request"}
			}
			labels = &p.Labels
			if v, ok := flagValue(args, "--add-reviewer"); ok {
				p.Requested = mockAdd(p.Requested, strings.Split(v, ","))
			}
			if v, ok := flagValue(args, "--remove-reviewer"); ok {
				p.Requested = mockRemove(p.Requested, strings.Split(v, ","))
			}
		} else {
			i, ok := f.issues[n]
			if !ok {
				return Result{1, "", "no such issue"}
			}
			labels = &i.Labels
		}
		if v, ok := flagValue(args, "--add-label"); ok {
			*labels = mockAdd(*labels, strings.Split(v, ","))
		}
		if v, ok := flagValue(args, "--remove-label"); ok {
			*labels = mockRemove(*labels, strings.Split(v, ","))
		}
		return Result{0, "https://github.com/mock/repo/pull/" + args[3] + "\n", ""}
	case "pr comment", "issue comment":
		n, _ := strconv.Atoi(args[3])
		body, _ := flagValue(args, "--body")
		if p, ok := f.prs[n]; ok && args[1] == "pr" {
			p.Comments = append(p.Comments, [2]string{"you", body})
		} else if i, ok := f.issues[n]; ok {
			i.Comments = append(i.Comments, [2]string{"you", body})
		}
		return Result{0, "https://github.com/mock/repo/issues/" + args[3] + "#issuecomment-1\n", ""}
	case "pr close", "pr reopen", "issue close", "issue reopen":
		n, _ := strconv.Atoi(args[3])
		state := "CLOSED"
		if args[2] == "reopen" {
			state = "OPEN"
		}
		if args[1] == "pr" {
			if p, ok := f.prs[n]; ok {
				p.State = state
			}
		} else if i, ok := f.issues[n]; ok {
			i.State = state
		}
		return Result{0, "", ""}
	case "run view":
		return Result{0, mockJobLog(args[3]), ""}
	}
	return Result{1, "", "mock gh: unsupported " + cmd}
}

func mockStateMatch(state, want string) bool {
	switch want {
	case "", "open":
		return state == "OPEN"
	case "closed":
		return state != "OPEN"
	case "merged":
		return state == "MERGED"
	}
	return true
}

func mockSearch(title, search string) bool {
	for _, w := range strings.Fields(search) {
		if strings.Contains(w, ":") {
			continue
		}
		if !strings.Contains(strings.ToLower(title), strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func mockAdd(list, add []string) []string {
	for _, a := range add {
		if a = strings.TrimSpace(a); a != "" && !contains(list, a) {
			list = append(list, a)
		}
	}
	return list
}

func mockRemove(list, drop []string) []string {
	out := []string{}
	for _, l := range list {
		if !contains(drop, l) {
			out = append(out, l)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, l := range list {
		if l == s {
			return true
		}
	}
	return false
}

func mockLabels(names []string) []map[string]string {
	out := []map[string]string{}
	for _, n := range names {
		out = append(out, map[string]string{"name": n, "color": mockLabelColors[n]})
	}
	return out
}

func (f *mockForge) prJSON(p *mockPR) map[string]any {
	checks := []map[string]any{}
	for _, c := range p.Checks {
		status := "COMPLETED"
		if c[1] == "" {
			status = "IN_PROGRESS"
		}
		checks = append(checks, map[string]any{"__typename": "CheckRun", "name": c[0], "workflowName": "CI",
			"status": status, "conclusion": c[1], "detailsUrl": c[2]})
	}
	requests := []map[string]string{}
	for _, r := range p.Requested {
		requests = append(requests, map[string]string{"__typename": "User", "login": r})
	}
	reviews := []map[string]any{}
	for i, r := range p.Reviews {
		reviews = append(reviews, map[string]any{"author": map[string]string{"login": r[0]}, "state": r[1],
			"body": "", "submittedAt": fmt.Sprintf("2026-09-26T1%d:00:00Z", i+2)})
	}
	comments := []map[string]any{}
	for i, c := range p.Comments {
		comments = append(comments, map[string]any{"author": map[string]string{"login": c[0]}, "body": c[1],
			"createdAt": fmt.Sprintf("2026-09-26T0%d:30:00Z", i+1), "url": fmt.Sprintf("https://github.com/mock/repo/pull/%d#c%d", p.Number, i),
			"reactionGroups": []map[string]any{{"content": "THUMBS_UP", "users": map[string]int{"totalCount": i % 2}}}})
	}
	decision := "REVIEW_REQUIRED"
	for _, r := range p.Reviews {
		if r[1] == "APPROVED" {
			decision = "APPROVED"
		}
		if r[1] == "CHANGES_REQUESTED" {
			decision = "CHANGES_REQUESTED"
		}
	}
	mergeable, mergeState := "MERGEABLE", "CLEAN"
	switch {
	case len(p.Conflicts) > 0:
		mergeable, mergeState = "CONFLICTING", "DIRTY"
	case p.Draft:
		mergeState = "DRAFT"
	}
	var auto any
	if p.AutoMerge != "" {
		auto = map[string]any{"mergeMethod": p.AutoMerge, "enabledBy": map[string]string{"login": "you"}}
	}
	out := map[string]any{
		"number": p.Number, "title": p.Title, "body": p.Body, "url": fmt.Sprintf("https://github.com/mock/repo/pull/%d", p.Number),
		"state": p.State, "isDraft": p.Draft, "author": map[string]string{"login": p.Author},
		"headRefName": p.Head, "headRefOid": p.SHA, "baseRefName": p.Base, "isCrossRepository": false,
		"mergeable": mergeable, "mergeStateStatus": mergeState, "reviewDecision": decision,
		"reviewRequests": requests, "latestReviews": reviews, "reviews": reviews,
		"labels": mockLabels(p.Labels), "assignees": []map[string]string{{"login": p.Author}}, "comments": comments,
		"commits": []map[string]any{{"oid": p.SHA, "messageHeadline": "Implement " + strings.ToLower(p.Title),
			"committedDate": "2026-09-25T20:00:00Z", "authors": []map[string]string{{"login": p.Author}}}},
		"additions": 120 + p.Number, "deletions": 14 + p.Number%5, "changedFiles": 3 + p.Number%4,
		"statusCheckRollup": checks, "autoMergeRequest": auto,
		"reactionGroups": []map[string]any{{"content": "ROCKET", "users": map[string]int{"totalCount": 2}}},
		"createdAt":      "2026-09-24T08:00:00Z", "updatedAt": p.Updated,
	}
	if p.State == "MERGED" {
		out["mergedAt"] = p.Updated
	}
	return out
}

func (f *mockForge) issueJSON(i *mockIssue) map[string]any {
	comments := []map[string]any{}
	for k, c := range i.Comments {
		comments = append(comments, map[string]any{"author": map[string]string{"login": c[0]}, "body": c[1],
			"createdAt": fmt.Sprintf("2026-09-26T0%d:10:00Z", k+1)})
	}
	return map[string]any{
		"number": i.Number, "title": i.Title, "body": i.Body, "url": fmt.Sprintf("https://github.com/mock/repo/issues/%d", i.Number),
		"state": i.State, "author": map[string]string{"login": i.Author}, "labels": mockLabels(i.Labels),
		"assignees": []map[string]string{}, "comments": comments, "reactionGroups": []map[string]any{},
		"createdAt": "2026-09-22T08:00:00Z", "updatedAt": i.Updated,
	}
}

func mockJobLog(run string) string {
	return "unit tests\tRun pytest\t\ufeff2026-09-26T18:04:01.0000000Z ============================= test session starts ==============================\n" +
		"unit tests\tRun pytest\t2026-09-26T18:04:02.0000000Z collected 42 items\n" +
		"unit tests\tRun pytest\t2026-09-26T18:04:09.0000000Z tests/test_progress.py::test_reports_budget FAILED\n" +
		"unit tests\tRun pytest\t2026-09-26T18:04:09.0000000Z E   AssertionError: expected 3 of 5 retries used, got 2 of 5\n" +
		"unit tests\tRun pytest\t2026-09-26T18:04:09.0000000Z ========================= 1 failed, 41 passed in 7.1s =========================\n" +
		"unit tests\tRun pytest\t2026-09-26T18:04:09.0000000Z ##[error]Process completed with exit code 1. (run " + run + ")\n"
}
