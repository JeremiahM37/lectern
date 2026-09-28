import assert from "node:assert/strict";
import { test } from "node:test";
import { ago, boardColumns, itemMark, matches, mergeButtonLabel, mergeState, parseTasksHash, sameRef, tasksHash } from "./logic";
import type { Item, MergeOptions } from "./types";

const item = (over: Partial<Item>): Item => ({
  source: "github", kind: "pr", id: "1", title: "t", url: "", state: "open", assignees: [], labels: [], updated_at: "",
  ...over,
});

const opts = (over: Partial<MergeOptions> = {}): MergeOptions => ({
  methods: ["merge", "squash"], default: "squash", delete_branch_default: true, auto_merge_allowed: true,
  merge_queue: false, can_merge: true, ...over,
});

test("tasks deep links round trip, including a connection and an identifier with a slash-free key", () => {
  const ref = { source: "linear" as const, kind: "issue" as const, id: "ENG-12", connection_id: 4 };
  assert.equal(tasksHash(3, ref), "#issues/3/linear/issue/ENG-12/4");
  assert.deepEqual(parseTasksHash("#issues/3/linear/issue/ENG-12/4"), { project: 3, ref });
  assert.deepEqual(parseTasksHash("#tasks/3/linear/issue/ENG-12/4"), { project: 3, ref });
  assert.deepEqual(parseTasksHash("#tasks/3"), { project: 3 });
  assert.deepEqual(parseTasksHash("#tasks/3/trello/issue/x"), { project: 3 });
  assert.deepEqual(parseTasksHash("#tasks/0"), {});
  assert.deepEqual(parseTasksHash("#task/3"), {});
  assert.ok(sameRef({ source: "github", kind: "pr", id: "5" }, item({ id: "5" })));
  assert.ok(!sameRef({ source: "github", kind: "pr", id: "5", connection_id: 2 }, item({ id: "5" })));
});

test("item marks follow each host's own notation", () => {
  assert.equal(itemMark({ source: "github", kind: "pr", id: "7" }), "#7");
  assert.equal(itemMark({ source: "gitlab", kind: "pr", id: "7" }), "!7");
  assert.equal(itemMark({ source: "gitlab", kind: "issue", id: "7" }), "#7");
  assert.equal(itemMark({ source: "jira", kind: "issue", id: "OPS-7" }), "OPS-7");
});

test("merge button says what will actually happen", () => {
  assert.equal(mergeButtonLabel({ merge: opts() }, "squash", false), "Squash and merge");
  assert.equal(mergeButtonLabel({ merge: opts({ merge_queue: true }) }, "squash", false), "Add to merge queue");
  assert.equal(mergeButtonLabel({ merge: opts() }, "merge", true), "Enable auto-merge");
});

test("merge state blocks what the host would refuse and warns about the rest", () => {
  const base = { state: "open", draft: false, mergeable: "mergeable", merge: opts(), checks: "pass" as const, review: "approved" as const };
  assert.deepEqual(mergeState(base), { block: "", warn: "", autoOnly: false });
  assert.match(mergeState({ ...base, state: "merged" }).block, /merged/);
  assert.match(mergeState({ ...base, draft: true }).block, /Draft/);
  assert.match(mergeState({ ...base, mergeable: "conflicting" }).block, /conflicts/);
  assert.match(mergeState({ ...base, merge: opts({ can_merge: false }) }).block, /cannot merge/);
  const pending = mergeState({ ...base, checks: "pending", review: "review_required" });
  assert.equal(pending.block, "");
  assert.match(pending.warn, /still running.*review is still required/);
  assert.equal(pending.autoOnly, true);
  assert.match(mergeState({ ...base, checks: "fail" }).warn, /failing/);
});

test("board columns keep a Linear team's state order and show empty states", () => {
  const cols = boardColumns(
    [item({ source: "linear", kind: "issue", id: "E-1", state: "Done", status_type: "completed" }),
     item({ source: "linear", kind: "issue", id: "E-2", state: "Todo", status_type: "unstarted" })],
    [{ id: "a", name: "Backlog", type: "backlog" }, { id: "b", name: "Todo", type: "unstarted" }, { id: "c", name: "Done", type: "completed" }],
  );
  assert.deepEqual(cols.map((c) => `${c.title}:${c.items.length}`), ["Backlog:0", "Todo:1", "Done:1"]);
  const jira = boardColumns([
    item({ source: "jira", kind: "issue", id: "O-1", state: "Done", status_type: "done" }),
    item({ source: "jira", kind: "issue", id: "O-2", state: "In Progress", status_type: "indeterminate" }),
    item({ source: "jira", kind: "issue", id: "O-3", state: "To Do", status_type: "new" }),
  ]);
  assert.deepEqual(jira.map((c) => c.title), ["To Do", "In Progress", "Done"]);
  const prs = boardColumns([item({ checks: "fail" }), item({ id: "2", review: "approved" }), item({ id: "3", draft: true })]);
  assert.deepEqual(prs.map((c) => c.title).sort(), ["Approved", "Checks failing", "Draft"]);
});

test("search narrows on id, title, branch, people and labels", () => {
  const it = item({ id: "42", title: "Retry budget", head: "feature/retry", author: "sam", labels: [{ name: "enhancement" }] });
  assert.ok(matches(it, "retry sam"));
  assert.ok(matches(it, "42"));
  assert.ok(matches(it, "ENHANCE"));
  assert.ok(!matches(it, "retry riley"));
});

test("relative times", () => {
  const now = Date.parse("2026-09-27T12:00:00Z");
  assert.equal(ago("2026-09-27T11:59:30Z", now), "just now");
  assert.equal(ago("2026-09-27T09:00:00Z", now), "3h ago");
  assert.equal(ago("2026-09-20T12:00:00Z", now), "7d ago");
  assert.equal(ago("2026-01-01T00:00:00Z", now), "2026-01-01");
  assert.equal(ago("", now), "");
});
