import assert from "node:assert/strict";
import { test } from "node:test";
import {
  activatePane, activePane, closePane, cycleTab, deserialize, emptyState, equalize, focusDirection, groupOf, groups,
  layoutRects, movePane, nudge, openPane, reopenClosed, reorderPane, serialize, splitGroup, toggleMaximize, unsplit,
  visiblePanes, type PaneRef, type WorkspaceState,
} from "./layout";

const term = (n: number): PaneRef => ({ id: `/terminal/session/${n}`, kind: "terminal", title: `T${n}`, params: { path: `/terminal/session/${n}` } });
const chat = (n: number): PaneRef => ({ id: `chat:session:${n}`, kind: "chat", title: `Chat ${n}`, params: { session: String(n) } });
const open = (...refs: PaneRef[]) => refs.reduce((state, ref) => openPane(state, ref), emptyState());
const shape = (state: WorkspaceState): unknown => {
  const walk = (node: WorkspaceState["root"]): unknown =>
    node.type === "group" ? node.panes.map((id) => id.replace(/^\/terminal\/session\//, "t").replace(/^chat:session:/, "c")) : { [node.dir]: node.children.map(walk) };
  return walk(state.root);
};

test("opening panes adds tabs to the focused group after the visible one", () => {
  let state = open(term(1), term(2));
  state = activatePane(state, term(1).id);
  state = openPane(state, term(3));
  assert.deepEqual(state.panes.map((row) => row.title), ["T1", "T3", "T2"]);
  assert.equal(activePane(state), term(3).id);
  // Opening one that is already open shows it rather than duplicating it.
  state = openPane(state, { ...term(2), title: "Renamed" });
  assert.equal(state.panes.length, 3);
  assert.equal(activePane(state), term(2).id);
  assert.equal(state.panes.find((row) => row.id === term(2).id)?.title, "Renamed");
});

test("dropping a tab on an edge splits; on the centre it joins the group", () => {
  let state = open(term(1), term(2), term(3));
  const first = groups(state.root)[0]!.id;
  state = movePane(state, term(2).id, first, "right");
  assert.deepEqual(shape(state), { row: [["t1", "t3"], ["t2"]] });
  const right = groupOf(state, term(2).id)!.id;
  state = movePane(state, term(3).id, right, "bottom");
  assert.deepEqual(shape(state), { row: [["t1"], { column: [["t2"], ["t3"]] }] });
  // Dropped in the centre of another group, it joins that group and the
  // emptied group disappears.
  state = movePane(state, term(1).id, right, "center");
  assert.deepEqual(shape(state), { column: [["t1", "t2"], ["t3"]] });
  assert.equal(activePane(state), term(1).id);
});

test("any pane type can share a tree with terminals", () => {
  let state = open(term(1), chat(1));
  state = movePane(state, chat(1).id, groups(state.root)[0]!.id, "right");
  assert.deepEqual(shape(state), { row: [["t1"], ["c1"]] });
  assert.deepEqual(visiblePanes(state).sort(), [chat(1).id, term(1).id].sort());
});

test("a same-direction split is flattened rather than nested", () => {
  let state = open(term(1), term(2), term(3));
  const first = groups(state.root)[0]!.id;
  state = movePane(state, term(2).id, first, "right");
  state = movePane(state, term(3).id, groupOf(state, term(2).id)!.id, "right");
  assert.deepEqual(shape(state), { row: [["t1"], ["t2"], ["t3"]] });
  const root = state.root as { sizes: number[] };
  assert.ok(Math.abs(root.sizes.reduce((a, b) => a + b, 0) - 1) < 1e-9);
});

test("split with no drag shows the most recent tab not already on screen", () => {
  let state = open(term(1), term(2), term(3));
  state = activatePane(state, term(1).id);
  state = activatePane(state, term(3).id);
  state = splitGroup(state, "row");
  assert.deepEqual(visiblePanes(state).sort(), [term(1).id, term(3).id].sort());
  // With only one pane there is nothing to split.
  const lone = open(term(1));
  assert.equal(splitGroup(lone, "row"), lone);
});

test("closing the last tab of a group removes the group and keeps focus sensible", () => {
  let state = open(term(1), term(2));
  state = movePane(state, term(2).id, groups(state.root)[0]!.id, "right");
  state = closePane(state, term(2).id);
  assert.deepEqual(shape(state), ["t1"]);
  assert.equal(activePane(state), term(1).id);
  // It can be reopened, and comes back as the visible tab.
  state = reopenClosed(state);
  assert.equal(activePane(state), term(2).id);
  assert.equal(state.panes.length, 2);
});

test("resize keeps every pane on screen and sizes summing to one", () => {
  let state = open(term(1), term(2));
  state = movePane(state, term(2).id, groups(state.root)[0]!.id, "right");
  const split = state.root.type === "split" ? state.root.id : "";
  state = nudge(state, split, 0, 0.9);
  const sizes = (state.root as { sizes: number[] }).sizes;
  assert.ok(sizes[1]! >= 0.08 - 1e-9, String(sizes));
  assert.ok(Math.abs(sizes[0]! + sizes[1]! - 1) < 1e-9);
  state = equalize(state);
  assert.deepEqual((state.root as { sizes: number[] }).sizes, [0.5, 0.5]);
});

test("maximize shows only the focused pane, and needs a split to mean anything", () => {
  let state = open(term(1), term(2));
  assert.equal(toggleMaximize(state).maximized, null);
  state = movePane(state, term(2).id, groups(state.root)[0]!.id, "right");
  state = toggleMaximize(state);
  assert.deepEqual(visiblePanes(state), [term(2).id]);
  state = toggleMaximize(state);
  assert.equal(visiblePanes(state).length, 2);
});

test("directional focus follows the geometry", () => {
  let state = open(term(1), term(2), term(3));
  const first = groups(state.root)[0]!.id;
  state = movePane(state, term(2).id, first, "right");
  state = movePane(state, term(3).id, groupOf(state, term(2).id)!.id, "bottom");
  state = activatePane(state, term(1).id);
  state = focusDirection(state, "right");
  assert.equal(activePane(state), term(2).id);
  state = focusDirection(state, "down");
  assert.equal(activePane(state), term(3).id);
  state = focusDirection(state, "left");
  assert.equal(activePane(state), term(1).id);
  const rects = layoutRects(state.root);
  assert.equal(rects.groups.size, 3);
  assert.equal(rects.handles.length, 2);
});

test("tab order can be rearranged, within the strip and within a group", () => {
  let state = open(term(1), term(2), term(3));
  state = reorderPane(state, term(3).id, 0);
  assert.deepEqual(state.panes.map((row) => row.title), ["T3", "T1", "T2"]);
  state = cycleTab(activatePane(state, term(3).id), 1);
  assert.equal(activePane(state), term(1).id);
});

test("a layout survives serialization and restores identically", () => {
  let state = open(term(1), term(2), chat(3));
  state = movePane(state, term(2).id, groups(state.root)[0]!.id, "right");
  state = movePane(state, chat(3).id, groupOf(state, term(2).id)!.id, "bottom");
  const restored = deserialize(JSON.parse(JSON.stringify(serialize(state))));
  assert.deepEqual(serialize(restored), serialize(state));
});

test("restore drops what it cannot trust instead of failing", () => {
  assert.deepEqual(deserialize("junk").panes, []);
  assert.deepEqual(deserialize({ v: 2, panes: [] }).panes, []);
  const state = open(term(1), term(2));
  const raw = serialize(movePane(state, term(2).id, groups(state.root)[0]!.id, "right")) as unknown as Record<string, unknown>;
  // A pane type nobody registered is dropped, and its empty group goes too.
  const restored = deserialize(raw, (ref) => (ref.id === term(2).id ? null : ref));
  assert.deepEqual(shape(restored), ["t1"]);
  // A pane the tree forgot is still placed; a duplicate id is kept once.
  const orphan = { ...raw, panes: [...(raw.panes as PaneRef[]), term(9), term(9)] };
  assert.equal(deserialize(orphan).panes.filter((row) => row.id === term(9).id).length, 1);
  assert.ok(groupOf(deserialize(orphan), term(9).id));
  // Hostile trees: repeated ids and absurd nesting do not throw.
  assert.doesNotThrow(() => deserialize({ v: 1, panes: [term(1)], root: { type: "split", id: "a", dir: "row", children: [{ type: "group", id: "a", panes: [] }], sizes: [NaN] } }));
});

test("unsplit brings every pane back into one group", () => {
  let state = open(term(1), term(2), term(3));
  state = splitGroup(state, "row");
  state = splitGroup(state, "column");
  state = unsplit(state);
  assert.equal(groups(state.root).length, 1);
  assert.equal(groups(state.root)[0]!.panes.length, 3);
});
