// Pure helpers for the Tasks hub, kept apart from the components so they are
// unit tested (logic.test.ts) without a browser.
import type { Item, ItemRef, MergeMethod, PRDetail, Source, Transition } from "./types";

export const SOURCE_NAME: Record<Source, string> = {
  github: "GitHub",
  gitlab: "GitLab",
  linear: "Linear",
  jira: "Jira",
};

/** The short mark a list row shows before the id. */
export function itemMark(item: Pick<Item, "source" | "kind" | "id">): string {
  if (item.source === "github" || item.source === "gitlab") {
    const sep = item.kind === "pr" ? (item.source === "gitlab" ? "!" : "#") : "#";
    return sep + item.id;
  }
  return item.id;
}

// ---- deep links ------------------------------------------------------------

const SOURCES: Source[] = ["github", "gitlab", "linear", "jira"];

/** Reads #tasks/<project>[/<source>/<kind>/<id>[/<connection>]]. */
export function parseTasksHash(hash: string): { project?: number; ref?: ItemRef } {
  let raw = hash.replace(/^#/, "");
  try {
    raw = decodeURIComponent(raw);
  } catch {
    return {};
  }
  const [head, project, source, kind, id, conn] = raw.split("/");
  if (head !== "tasks") return {};
  const pid = /^[1-9]\d*$/.test(project || "") ? Number(project) : undefined;
  if (!pid) return {};
  if (!SOURCES.includes(source as Source) || (kind !== "pr" && kind !== "issue") || !id) return { project: pid };
  const ref: ItemRef = { source: source as Source, kind, id };
  if (/^[1-9]\d*$/.test(conn || "")) ref.connection_id = Number(conn);
  return { project: pid, ref };
}

export function tasksHash(project: number, ref?: ItemRef): string {
  if (!ref) return `#tasks/${project}`;
  const tail = ref.connection_id ? `/${ref.connection_id}` : "";
  return `#tasks/${project}/${ref.source}/${ref.kind}/${encodeURIComponent(ref.id)}${tail}`;
}

export function sameRef(a?: ItemRef, b?: Pick<Item, "source" | "kind" | "id" | "connection_id">): boolean {
  return !!a && !!b && a.source === b.source && a.kind === b.kind && a.id === b.id &&
    (a.connection_id || 0) === (b.connection_id || 0);
}

// ---- merging ---------------------------------------------------------------

const METHOD_VERB: Record<MergeMethod, string> = {
  merge: "Create a merge commit",
  squash: "Squash and merge",
  rebase: "Rebase and merge",
};

export function methodLabel(m: MergeMethod): string {
  return METHOD_VERB[m];
}

/** The merge button's words: queueing, auto-merge and a direct merge read
 * differently so the confirmation is never ambiguous about what happens. */
export function mergeButtonLabel(pr: Pick<PRDetail, "merge">, method: MergeMethod, auto: boolean): string {
  if (auto) return "Enable auto-merge";
  if (pr.merge.merge_queue) return "Add to merge queue";
  return METHOD_VERB[method];
}

/** Why merging is not possible right now ("" when it is), and a warning a
 * person should read before confirming anyway. */
export function mergeState(pr: Pick<PRDetail, "state" | "draft" | "mergeable" | "merge" | "checks" | "review">): {
  block: string;
  warn: string;
  autoOnly: boolean;
} {
  if (pr.state !== "open") return { block: `This pull request is ${pr.state}.`, warn: "", autoOnly: false };
  if (pr.draft) return { block: "Drafts cannot be merged. Mark it ready for review first.", warn: "", autoOnly: false };
  if (!pr.merge.can_merge) return { block: "Your login on this machine cannot merge into this repository.", warn: "", autoOnly: false };
  if (pr.mergeable === "conflicting")
    return { block: "This branch has conflicts that must be resolved first.", warn: "", autoOnly: false };
  const warn: string[] = [];
  let autoOnly = false;
  if (pr.checks === "fail") warn.push("Some checks are failing.");
  if (pr.checks === "pending") {
    warn.push("Checks are still running.");
    autoOnly = pr.merge.auto_merge_allowed;
  }
  if (pr.review === "changes_requested") warn.push("A reviewer requested changes.");
  if (pr.review === "review_required") warn.push("A review is still required.");
  return { block: "", warn: warn.join(" "), autoOnly };
}

// ---- lists and boards ------------------------------------------------------

export interface Column {
  key: string;
  title: string;
  type?: string;
  items: Item[];
}

const TYPE_ORDER = ["triage", "backlog", "unstarted", "new", "started", "indeterminate", "completed", "done", "canceled"];

/** Board columns: a Linear team's states when known (so empty columns still
 * show), otherwise one column per status seen, ordered by status type. */
export function boardColumns(items: Item[], states: Transition[] = []): Column[] {
  const cols = new Map<string, Column>();
  for (const s of states) cols.set(s.name, { key: s.name, title: s.name, type: s.type, items: [] });
  for (const item of items) {
    const title = columnTitle(item);
    if (!cols.has(title)) cols.set(title, { key: title, title, type: item.status_type || item.state, items: [] });
    cols.get(title)!.items.push(item);
  }
  const out = [...cols.values()];
  if (!states.length) {
    const rank = (c: Column) => {
      const i = TYPE_ORDER.indexOf(c.type || "");
      return i < 0 ? TYPE_ORDER.length : i;
    };
    out.sort((a, b) => rank(a) - rank(b));
  }
  return out;
}

function columnTitle(item: Item): string {
  if (item.source === "linear" || item.source === "jira") return item.state || "No status";
  if (item.kind === "pr") {
    if (item.state !== "open") return item.state === "merged" ? "Merged" : "Closed";
    if (item.draft) return "Draft";
    if (item.checks === "fail") return "Checks failing";
    if (item.conflicts) return "Conflicts";
    if (item.review === "approved") return "Approved";
    if (item.review === "changes_requested") return "Changes requested";
    return "In review";
  }
  return item.state === "open" ? "Open" : "Closed";
}

/** Free-text narrowing applied on top of the server's filter, so typing in
 * the search box answers instantly. */
export function matches(item: Item, text: string): boolean {
  const q = text.trim().toLowerCase();
  if (!q) return true;
  const hay = [item.id, item.title, item.author, item.head, item.state, ...item.assignees, ...item.labels.map((l) => l.name)]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return q.split(/\s+/).every((w) => hay.includes(w));
}

// ---- time ------------------------------------------------------------------

export function ago(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  const s = Math.max(0, (now - t) / 1000);
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  if (s < 86400 * 30) return `${Math.floor(s / 86400)}d ago`;
  return new Date(t).toISOString().slice(0, 10);
}

/** A readable label colour: GitHub stores label colours without '#'. */
export function labelStyle(color?: string): { borderColor?: string; color?: string } {
  if (!color || !/^[0-9a-f]{6}$/i.test(color)) return {};
  // lightened so a dark label colour stays readable on the dark theme
  return { borderColor: `#${color}99`, color: `color-mix(in srgb, #${color} 65%, white)` };
}
