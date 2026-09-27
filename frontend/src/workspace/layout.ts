// The workspace layout: open panes (terminals, chats, diffs, a browser, a
// file…) arranged as a tree of splits whose leaves are tab groups. Pure data
// and pure functions — every operation returns a new state — so layouts can be
// tested, saved server-side, and restored on another device.
//
// A pane belongs to exactly one group. `panes` is the global tab order (the
// strip across the top); each group lists its members in that same order and
// shows one of them. Splits carry fractional sizes that always sum to 1.

export interface PaneRef {
  id: string;
  kind: string;
  title: string;
  params: Record<string, string>;
}
export interface GroupNode {
  type: "group";
  id: string;
  panes: string[];
  active: string | null;
}
export interface SplitNode {
  type: "split";
  id: string;
  dir: "row" | "column";
  children: LayoutNode[];
  sizes: number[];
}
export type LayoutNode = GroupNode | SplitNode;
export type Edge = "center" | "left" | "right" | "top" | "bottom";
export interface WorkspaceState {
  panes: PaneRef[];
  root: LayoutNode;
  focus: string;
  maximized: string | null;
  // Pane ids, most recently shown first.
  mru: string[];
  // Recently closed panes, newest first, for "reopen closed tab".
  closed: PaneRef[];
}

const MIN_SIZE = 0.08;
const MAX_PANES = 40;
let counter = 0;
export function newId(prefix: string) {
  counter = (counter + 1) % 1e6;
  return prefix + Date.now().toString(36).slice(-4) + counter.toString(36) + Math.random().toString(36).slice(2, 5);
}

export function emptyState(): WorkspaceState {
  const id = newId("g");
  return { panes: [], root: { type: "group", id, panes: [], active: null }, focus: id, maximized: null, mru: [], closed: [] };
}

// ---- reading the tree ----------------------------------------------------------

export function groups(node: LayoutNode): GroupNode[] {
  return node.type === "group" ? [node] : node.children.flatMap(groups);
}
export function findGroup(state: WorkspaceState, id: string) {
  return groups(state.root).find((group) => group.id === id);
}
export function groupOf(state: WorkspaceState, paneId: string) {
  return groups(state.root).find((group) => group.panes.includes(paneId));
}
export function pane(state: WorkspaceState, id: string | null | undefined) {
  return id ? state.panes.find((row) => row.id === id) : undefined;
}
export function focusedGroup(state: WorkspaceState): GroupNode {
  return findGroup(state, state.focus) || groups(state.root)[0]!;
}
// The pane the person is looking at: the focused group's visible one.
export function activePane(state: WorkspaceState): string | null {
  return focusedGroup(state).active;
}
// Every pane showing right now, one per group.
export function visiblePanes(state: WorkspaceState): string[] {
  const shown = state.maximized ? groups(state.root).filter((group) => group.id === state.maximized) : groups(state.root);
  return shown.map((group) => group.active).filter((id): id is string => !!id);
}

// ---- rewriting the tree --------------------------------------------------------

function mapNode(node: LayoutNode, fn: (node: LayoutNode) => LayoutNode): LayoutNode {
  const next = node.type === "split" ? { ...node, children: node.children.map((child) => mapNode(child, fn)) } : node;
  return fn(next);
}

function updateGroup(state: WorkspaceState, id: string, fn: (group: GroupNode) => GroupNode): WorkspaceState {
  return { ...state, root: mapNode(state.root, (node) => (node.type === "group" && node.id === id ? fn(node) : node)) };
}

function evenSizes(n: number) {
  return Array.from({ length: n }, () => 1 / n);
}

function fixSizes(sizes: number[], n: number): number[] {
  let list = sizes.length === n && sizes.every((size) => Number.isFinite(size) && size > 0) ? sizes : evenSizes(n);
  list = list.map((size) => Math.max(MIN_SIZE, size));
  const total = list.reduce((sum, size) => sum + size, 0);
  return list.map((size) => size / total);
}

// Tidies a tree after any edit: empty groups go (unless nothing is left),
// one-child splits collapse, a split inside a split of the same direction is
// flattened into it, and sizes are re-normalised.
function tidy(node: LayoutNode, order: Map<string, number>): LayoutNode | null {
  if (node.type === "group") {
    const panes = node.panes.filter((id) => order.has(id)).sort((a, b) => order.get(a)! - order.get(b)!);
    if (!panes.length) return null;
    return { ...node, panes, active: node.active && panes.includes(node.active) ? node.active : panes[0]! };
  }
  const children: LayoutNode[] = [],
    sizes: number[] = [];
  node.children.forEach((child, index) => {
    const size = node.sizes[index] ?? 1 / node.children.length;
    const kept = tidy(child, order);
    if (!kept) return;
    if (kept.type === "split" && kept.dir === node.dir) {
      kept.children.forEach((grand, i) => {
        children.push(grand);
        sizes.push(size * (kept.sizes[i] ?? 1 / kept.children.length));
      });
    } else {
      children.push(kept);
      sizes.push(size);
    }
  });
  if (!children.length) return null;
  if (children.length === 1) return children[0]!;
  return { ...node, children, sizes: fixSizes(sizes, children.length) };
}

export function normalize(state: WorkspaceState): WorkspaceState {
  const seen = new Set<string>();
  const panes = state.panes.filter((row) => !seen.has(row.id) && seen.add(row.id)).slice(0, MAX_PANES);
  const order = new Map(panes.map((row, index) => [row.id, index]));
  // A pane in two groups stays in the first; a pane in none joins the focused one.
  const placed = new Set<string>();
  let root = mapNode(state.root, (node) => {
    if (node.type !== "group") return node;
    const own = node.panes.filter((id) => order.has(id) && !placed.has(id));
    own.forEach((id) => placed.add(id));
    return { ...node, panes: own };
  });
  const orphans = panes.filter((row) => !placed.has(row.id)).map((row) => row.id);
  if (orphans.length) {
    const target = groups(root).find((group) => group.id === state.focus) || groups(root)[0]!;
    root = mapNode(root, (node) => (node.type === "group" && node.id === target.id ? { ...node, panes: [...node.panes, ...orphans], active: node.active || orphans[0]! } : node));
  }
  const tidied = tidy(root, order) || { type: "group" as const, id: groups(root)[0]?.id || newId("g"), panes: [], active: null };
  const all = groups(tidied);
  const focus = all.some((group) => group.id === state.focus) ? state.focus : all[0]!.id;
  const maximized = state.maximized && all.length > 1 && all.some((group) => group.id === state.maximized) ? state.maximized : null;
  return {
    panes,
    root: tidied,
    focus,
    maximized,
    mru: state.mru.filter((id, index) => order.has(id) && state.mru.indexOf(id) === index),
    closed: state.closed.slice(0, 10),
  };
}

function touch(state: WorkspaceState, paneId: string | null): WorkspaceState {
  if (!paneId) return state;
  return { ...state, mru: [paneId, ...state.mru.filter((id) => id !== paneId)] };
}

// ---- operations ----------------------------------------------------------------

export function openPane(state: WorkspaceState, ref: PaneRef, options: { group?: string; edge?: Edge; activate?: boolean } = {}): WorkspaceState {
  const existing = pane(state, ref.id);
  if (existing) {
    const renamed = { ...state, panes: state.panes.map((row) => (row.id === ref.id ? { ...row, title: ref.title || row.title, params: { ...row.params, ...ref.params } } : row)) };
    if (options.edge && options.edge !== "center" && options.group) return movePane(renamed, ref.id, options.group, options.edge);
    return options.activate === false ? renamed : activatePane(renamed, ref.id);
  }
  const target = (options.group && findGroup(state, options.group)) || focusedGroup(state);
  // New tabs go right after the one being looked at, like a browser.
  const after = target.active ? state.panes.findIndex((row) => row.id === target.active) : -1;
  const panes = [...state.panes];
  panes.splice(after >= 0 ? after + 1 : panes.length, 0, ref);
  let next: WorkspaceState = { ...state, panes, closed: state.closed.filter((row) => row.id !== ref.id) };
  next = updateGroup(next, target.id, (group) => ({ ...group, panes: [...group.panes, ref.id], active: options.activate === false && group.active ? group.active : ref.id }));
  if (options.edge && options.edge !== "center") return movePane(normalize(next), ref.id, target.id, options.edge);
  if (options.activate !== false) next = touch({ ...next, focus: target.id }, ref.id);
  return normalize(next);
}

export function activatePane(state: WorkspaceState, paneId: string): WorkspaceState {
  const group = groupOf(state, paneId);
  if (!group) return state;
  const next = updateGroup(state, group.id, (row) => ({ ...row, active: paneId }));
  return touch({ ...next, focus: group.id, maximized: state.maximized && state.maximized !== group.id ? group.id : state.maximized }, paneId);
}

export function focusGroup(state: WorkspaceState, groupId: string): WorkspaceState {
  const group = findGroup(state, groupId);
  if (!group) return state;
  return touch({ ...state, focus: groupId }, group.active);
}

export function closePane(state: WorkspaceState, paneId: string): WorkspaceState {
  const ref = pane(state, paneId);
  if (!ref) return state;
  const group = groupOf(state, paneId);
  let next: WorkspaceState = { ...state, panes: state.panes.filter((row) => row.id !== paneId), closed: [ref, ...state.closed.filter((row) => row.id !== paneId)].slice(0, 10) };
  if (group) {
    const index = group.panes.indexOf(paneId);
    const rest = group.panes.filter((id) => id !== paneId);
    // Closing the visible tab shows its neighbour: the one after, else before.
    const active = group.active === paneId ? rest[index] || rest[index - 1] || null : group.active;
    next = updateGroup(next, group.id, (row) => ({ ...row, panes: rest, active }));
    if (!rest.length && groups(next.root).length > 1) {
      // The emptied group goes; focus moves to the most recent pane still open.
      const recent = next.mru.find((id) => id !== paneId && next.panes.some((row) => row.id === id));
      const home = recent ? groups(next.root).find((row) => row.panes.includes(recent)) : undefined;
      if (home) next = { ...next, focus: home.id };
    }
  }
  next = normalize({ ...next, mru: next.mru.filter((id) => id !== paneId) });
  const shown = activePane(next);
  return shown ? touch(next, shown) : next;
}

export function reopenClosed(state: WorkspaceState): WorkspaceState {
  const [ref, ...rest] = state.closed;
  if (!ref) return state;
  return openPane({ ...state, closed: rest }, ref);
}

// Moves a pane into a group (edge "center") or beside it, which splits the
// group. A pane dropped on its own lone group's edge has nowhere to go.
export function movePane(state: WorkspaceState, paneId: string, targetId: string, edge: Edge, index?: number): WorkspaceState {
  const source = groupOf(state, paneId),
    target = findGroup(state, targetId);
  if (!source || !target) return state;
  if (source.id === target.id && (edge === "center" || source.panes.length === 1)) {
    if (edge === "center" && index !== undefined) return reorderPane(state, paneId, index, target.id);
    return activatePane(state, paneId);
  }
  // Take it out of the group it was in.
  let next = updateGroup(state, source.id, (group) => {
    const rest = group.panes.filter((id) => id !== paneId);
    const at = group.panes.indexOf(paneId);
    return { ...group, panes: rest, active: group.active === paneId ? rest[at] || rest[at - 1] || null : group.active };
  });
  if (edge === "center") {
    next = updateGroup(next, target.id, (group) => ({ ...group, panes: [...group.panes, paneId], active: paneId }));
    next = normalize({ ...next, focus: target.id, maximized: null });
    return index !== undefined ? reorderPane(next, paneId, index, target.id) : touch(next, paneId);
  }
  const fresh: GroupNode = { type: "group", id: newId("g"), panes: [paneId], active: paneId };
  const dir = edge === "left" || edge === "right" ? "row" : "column";
  const before = edge === "left" || edge === "top";
  const root = mapNode(next.root, (node) => {
    if (node.type === "group" && node.id === target.id)
      return { type: "split", id: newId("s"), dir, children: before ? [fresh, node] : [node, fresh], sizes: [0.5, 0.5] };
    return node;
  });
  return touch(normalize({ ...next, root, focus: fresh.id, maximized: null }), paneId);
}

// Reorders the global tab strip. With a group given, `index` counts within
// that group's own tabs, as its header shows them.
export function reorderPane(state: WorkspaceState, paneId: string, index: number, groupId?: string): WorkspaceState {
  const from = state.panes.findIndex((row) => row.id === paneId);
  if (from < 0) return state;
  const moving = state.panes[from]!;
  const rest = state.panes.filter((row) => row.id !== paneId);
  let at = Math.max(0, Math.min(rest.length, index));
  if (groupId) {
    const members = rest.filter((row) => findGroup(state, groupId)?.panes.includes(row.id));
    const anchor = members[Math.max(0, Math.min(members.length, index))];
    at = anchor ? rest.indexOf(anchor) : members.length ? rest.indexOf(members.at(-1)!) + 1 : rest.length;
  }
  rest.splice(at, 0, moving);
  return normalize({ ...state, panes: rest });
}

export function movePaneBy(state: WorkspaceState, paneId: string, delta: number): WorkspaceState {
  const group = groupOf(state, paneId);
  if (!group) return state;
  const at = group.panes.indexOf(paneId);
  return reorderPane(state, paneId, Math.max(0, Math.min(group.panes.length - 1, at + delta)), group.id);
}

// "Split" with no drag: the focused group gains a neighbour showing the most
// recently used pane that is not already on screen (or, failing that, one
// more tab from this same group). Nothing to show means nothing to split.
export function companion(state: WorkspaceState, groupId = state.focus): string | undefined {
  const shown = new Set(visiblePanes(state));
  const group = findGroup(state, groupId);
  const candidates = [...state.mru, ...state.panes.map((row) => row.id)];
  return candidates.find((id) => !shown.has(id) && pane(state, id)) || group?.panes.find((id) => id !== group.active);
}

export function splitGroup(state: WorkspaceState, dir: "row" | "column", groupId = state.focus): WorkspaceState {
  const moving = companion(state, groupId);
  if (!moving) return state;
  return movePane(state, moving, groupId, dir === "row" ? "right" : "bottom");
}

export function unsplit(state: WorkspaceState): WorkspaceState {
  const shown = activePane(state);
  const id = focusedGroup(state).id;
  return normalize({ ...state, root: { type: "group", id, panes: state.panes.map((row) => row.id), active: shown }, focus: id, maximized: null });
}

export function resize(state: WorkspaceState, splitId: string, sizes: number[]): WorkspaceState {
  return {
    ...state,
    root: mapNode(state.root, (node) => (node.type === "split" && node.id === splitId ? { ...node, sizes: fixSizes(sizes, node.children.length) } : node)),
  };
}

// Moves the boundary after child `index` of a split by `delta` (a fraction of
// the split), keeping every child at least MIN_SIZE.
export function nudge(state: WorkspaceState, splitId: string, index: number, delta: number): WorkspaceState {
  const split = findSplit(state.root, splitId);
  if (!split || index < 0 || index >= split.children.length - 1) return state;
  const sizes = [...split.sizes];
  const pair = sizes[index]! + sizes[index + 1]!;
  const left = Math.max(MIN_SIZE, Math.min(pair - MIN_SIZE, sizes[index]! + delta));
  sizes[index] = left;
  sizes[index + 1] = pair - left;
  return resize(state, splitId, sizes);
}

export function findSplit(node: LayoutNode, id: string): SplitNode | undefined {
  if (node.type === "group") return undefined;
  return node.id === id ? node : node.children.map((child) => findSplit(child, id)).find(Boolean);
}

export function equalize(state: WorkspaceState): WorkspaceState {
  return { ...state, root: mapNode(state.root, (node) => (node.type === "split" ? { ...node, sizes: evenSizes(node.children.length) } : node)) };
}

export function toggleMaximize(state: WorkspaceState, groupId = state.focus): WorkspaceState {
  if (groups(state.root).length < 2) return { ...state, maximized: null };
  return { ...state, maximized: state.maximized === groupId ? null : groupId, focus: groupId };
}

export function cycleTab(state: WorkspaceState, delta: number): WorkspaceState {
  const group = focusedGroup(state);
  if (group.panes.length < 2 || !group.active) return state;
  const at = group.panes.indexOf(group.active);
  const next = group.panes[(at + delta + group.panes.length) % group.panes.length]!;
  return activatePane(state, next);
}

export function cycleGroup(state: WorkspaceState, delta: number): WorkspaceState {
  const all = groups(state.root);
  const at = all.findIndex((group) => group.id === state.focus);
  return focusGroup(state, all[(at + delta + all.length) % all.length]!.id);
}

export function moveToAdjacentGroup(state: WorkspaceState, delta: number): WorkspaceState {
  const shown = activePane(state);
  const all = groups(state.root);
  if (!shown || all.length < 2) return state;
  const at = all.findIndex((group) => group.id === state.focus);
  return movePane(state, shown, all[(at + delta + all.length) % all.length]!.id, "center");
}

// ---- geometry ------------------------------------------------------------------

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}
export interface Handle {
  split: string;
  index: number;
  dir: "row" | "column";
  rect: Rect;
  // The whole split's rectangle, to turn a pointer position into sizes.
  box: Rect;
}

// Every group's rectangle, in fractions of the workspace, and the draggable
// boundaries between siblings.
export function layoutRects(root: LayoutNode, box: Rect = { x: 0, y: 0, w: 1, h: 1 }, out = { groups: new Map<string, Rect>(), handles: [] as Handle[] }) {
  if (root.type === "group") {
    out.groups.set(root.id, box);
    return out;
  }
  let offset = 0;
  root.children.forEach((child, index) => {
    const size = root.sizes[index] ?? 1 / root.children.length;
    const rect = root.dir === "row" ? { x: box.x + box.w * offset, y: box.y, w: box.w * size, h: box.h } : { x: box.x, y: box.y + box.h * offset, w: box.w, h: box.h * size };
    layoutRects(child, rect, out);
    offset += size;
    if (index < root.children.length - 1)
      out.handles.push({
        split: root.id,
        index,
        dir: root.dir,
        rect: root.dir === "row" ? { x: box.x + box.w * offset, y: box.y, w: 0, h: box.h } : { x: box.x, y: box.y + box.h * offset, w: box.w, h: 0 },
        box,
      });
  });
  return out;
}

export function focusDirection(state: WorkspaceState, direction: "left" | "right" | "up" | "down"): WorkspaceState {
  const { groups: rects } = layoutRects(state.root);
  const from = rects.get(state.focus);
  if (!from) return state;
  const cx = from.x + from.w / 2,
    cy = from.y + from.h / 2;
  let best: { id: string; score: number } | undefined;
  for (const [id, rect] of rects) {
    if (id === state.focus) continue;
    const x = rect.x + rect.w / 2,
      y = rect.y + rect.h / 2;
    const ahead = direction === "left" ? cx - x : direction === "right" ? x - cx : direction === "up" ? cy - y : y - cy;
    if (ahead <= 1e-6) continue;
    const side = direction === "left" || direction === "right" ? Math.abs(y - cy) : Math.abs(x - cx);
    const score = ahead + side * 2;
    if (!best || score < best.score) best = { id, score };
  }
  return best ? focusGroup(state, best.id) : state;
}

// ---- persistence ---------------------------------------------------------------

export interface SerializedLayout {
  v: 1;
  panes: PaneRef[];
  root: LayoutNode;
  focus: string;
  maximized: string | null;
  mru: string[];
}

export function serialize(state: WorkspaceState): SerializedLayout {
  return { v: 1, panes: state.panes, root: state.root, focus: state.focus, maximized: state.maximized, mru: state.mru.slice(0, MAX_PANES) };
}

const idPattern = /^[A-Za-z0-9_-]{1,40}$/;

function readNode(raw: unknown, ids: Set<string>, depth: number): LayoutNode | null {
  if (!raw || typeof raw !== "object" || depth > 12) return null;
  const node = raw as { type?: unknown; id?: unknown; panes?: unknown; active?: unknown; dir?: unknown; children?: unknown; sizes?: unknown };
  if (typeof node.id !== "string" || !idPattern.test(node.id) || ids.has(node.id)) return null;
  ids.add(node.id);
  if (node.type === "group") {
    const panes = Array.isArray(node.panes) ? node.panes.filter((id): id is string => typeof id === "string").slice(0, MAX_PANES) : [];
    return { type: "group", id: node.id, panes, active: typeof node.active === "string" ? node.active : null };
  }
  if (node.type === "split" && (node.dir === "row" || node.dir === "column") && Array.isArray(node.children)) {
    const children = (node.children as unknown[]).slice(0, 12).map((child) => readNode(child, ids, depth + 1)).filter((child): child is LayoutNode => !!child);
    if (!children.length) return null;
    const sizes = Array.isArray(node.sizes) ? (node.sizes as unknown[]).map(Number) : [];
    return { type: "split", id: node.id, dir: node.dir, children, sizes: fixSizes(sizes, children.length) };
  }
  return null;
}

// Rebuilds a state from stored JSON. `check` validates each pane (its kind
// must be registered and its params well formed) and may rewrite it; a pane
// it rejects is dropped. Junk gives an empty workspace, never an exception.
export function deserialize(raw: unknown, check: (ref: PaneRef) => PaneRef | null = (ref) => ref): WorkspaceState {
  if (!raw || typeof raw !== "object") return emptyState();
  const data = raw as Partial<SerializedLayout>;
  if (data.v !== 1 || !Array.isArray(data.panes)) return emptyState();
  const panes: PaneRef[] = [];
  for (const row of data.panes.slice(0, MAX_PANES)) {
    if (!row || typeof row !== "object") continue;
    const candidate = row as Partial<PaneRef>;
    if (typeof candidate.id !== "string" || typeof candidate.kind !== "string") continue;
    const params: Record<string, string> = {};
    if (candidate.params && typeof candidate.params === "object")
      for (const [key, value] of Object.entries(candidate.params)) if (typeof value === "string" && key.length < 40) params[key] = value.slice(0, 500);
    const checked = check({ id: candidate.id.slice(0, 200), kind: candidate.kind.slice(0, 40), title: String(candidate.title || "").slice(0, 160), params });
    if (checked) panes.push(checked);
  }
  const root = readNode(data.root, new Set(), 0) || emptyState().root;
  const state: WorkspaceState = {
    panes,
    root,
    focus: typeof data.focus === "string" ? data.focus : "",
    maximized: typeof data.maximized === "string" ? data.maximized : null,
    mru: Array.isArray(data.mru) ? data.mru.filter((id): id is string => typeof id === "string") : [],
    closed: [],
  };
  return normalize(state);
}

// A saved, named layout. project_id scopes it to one project (null: any).
export interface SavedLayout {
  id: string;
  name: string;
  project_id: number | null;
  saved_at: number;
  layout: SerializedLayout;
}

export function cleanSavedLayouts(value: unknown): SavedLayout[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((row): row is SavedLayout => !!row && typeof row === "object" && typeof row.id === "string" && typeof row.name === "string" && !!row.layout)
    .slice(0, 30)
    .map((row) => ({ id: row.id, name: row.name.slice(0, 80), project_id: typeof row.project_id === "number" ? row.project_id : null, saved_at: Number(row.saved_at) || 0, layout: row.layout }));
}
