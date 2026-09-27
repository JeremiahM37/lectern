// Pure helpers for the Tasks hub, kept apart from the components so they are
// unit tested (logic.test.ts) without a browser.
import { ensureContrast } from "../theme/color";
import { themeTokens } from "../theme/app-theme";
import { t } from "../i18n";
import type { Item, ItemRef, MergeMethod, PRDetail, Source, Transition } from "./types";

export const SOURCE_NAME: Record<Source, string> = {
  github: "GitHub",
  gitlab: "GitLab",
  bitbucket: "Bitbucket",
  gitea: "Gitea",
  azure: "Azure DevOps",
  linear: "Linear",
  jira: "Jira",
};

/** A code host (pull requests live there), as opposed to an issue tracker. */
export function isForge(source: Source): boolean {
  return source === "github" || source === "gitlab" || source === "bitbucket" || source === "gitea" || source === "azure";
}

/** Items that come with workflow statuses rather than open/closed. */
export function hasStatuses(item: Pick<Item, "source" | "kind">): boolean {
  return item.source === "linear" || item.source === "jira" || (item.source === "azure" && item.kind === "issue");
}

/** Hosts where Lectern can add emoji reactions. */
export function canReact(source: Source): boolean {
  return source === "github" || source === "gitlab" || source === "gitea" || source === "linear";
}

/** Reaction names the API takes, with how they look. */
export const EMOJIS: [string, string][] = [
  ["+1", "👍"], ["-1", "👎"], ["laugh", "😄"], ["hooray", "🎉"], ["confused", "😕"], ["heart", "❤️"], ["rocket", "🚀"], ["eyes", "👀"],
];

/** The Issues tab's name for a host. */
export function issuesLabel(kind?: Source): string {
  return kind === "azure" ? t("trackers.hub.tab.workItems") : t("trackers.hub.tab.issues");
}

/** The short mark a list row shows before the id. */
export function itemMark(item: Pick<Item, "source" | "kind" | "id">): string {
  if (isForge(item.source)) {
    const sep = item.kind === "pr" && (item.source === "gitlab" || item.source === "azure") ? "!" : "#";
    return sep + item.id;
  }
  return item.id;
}

// ---- deep links ------------------------------------------------------------

const SOURCES: Source[] = ["github", "gitlab", "bitbucket", "gitea", "azure", "linear", "jira"];

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

const METHOD_VERB = (): Record<MergeMethod, string> => ({
  merge: t("trackers.merge.method.merge"),
  squash: t("trackers.merge.method.squash"),
  rebase: t("trackers.merge.method.rebase"),
});

export function methodLabel(m: MergeMethod): string {
  return METHOD_VERB()[m];
}

/** The merge button's words: queueing, auto-merge and a direct merge read
 * differently so the confirmation is never ambiguous about what happens. */
export function mergeButtonLabel(pr: Pick<PRDetail, "merge">, method: MergeMethod, auto: boolean): string {
  if (auto) return t("trackers.merge.enableAuto");
  if (pr.merge.merge_queue) return t("trackers.merge.addToQueue");
  return METHOD_VERB()[method];
}

/** Why merging is not possible right now ("" when it is), and a warning a
 * person should read before confirming anyway. */
export function mergeState(pr: Pick<PRDetail, "state" | "draft" | "mergeable" | "merge" | "checks" | "review">): {
  block: string;
  warn: string;
  autoOnly: boolean;
} {
  if (pr.state !== "open") return { block: t("trackers.merge.block.state", { state: pr.state }), warn: "", autoOnly: false };
  if (pr.draft) return { block: t("trackers.merge.block.draft"), warn: "", autoOnly: false };
  if (!pr.merge.can_merge) return { block: t("trackers.merge.block.permission"), warn: "", autoOnly: false };
  if (pr.mergeable === "conflicting")
    return { block: t("trackers.merge.block.conflicts"), warn: "", autoOnly: false };
  const warn: string[] = [];
  let autoOnly = false;
  if (pr.checks === "fail") warn.push(t("trackers.merge.warn.checksFailing"));
  if (pr.checks === "pending") {
    warn.push(t("trackers.merge.warn.checksRunning"));
    autoOnly = pr.merge.auto_merge_allowed;
  }
  if (pr.review === "changes_requested") warn.push(t("trackers.merge.warn.changesRequested"));
  if (pr.review === "review_required") warn.push(t("trackers.merge.warn.reviewRequired"));
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
  if (hasStatuses(item)) return item.state || t("trackers.column.noStatus");
  if (item.kind === "pr") {
    if (item.state !== "open") return item.state === "merged" ? t("trackers.column.merged") : t("trackers.column.closed");
    if (item.draft) return t("trackers.column.draft");
    if (item.checks === "fail") return t("trackers.column.checksFailing");
    if (item.conflicts) return t("trackers.column.conflicts");
    if (item.review === "approved") return t("trackers.column.approved");
    if (item.review === "changes_requested") return t("trackers.column.changesRequested");
    return t("trackers.column.inReview");
  }
  return item.state === "open" ? t("trackers.column.open") : t("trackers.column.closed");
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
  const ts = Date.parse(iso);
  if (Number.isNaN(ts)) return "";
  const s = Math.max(0, (now - ts) / 1000);
  if (s < 60) return t("trackers.ago.justNow");
  if (s < 3600) return t("trackers.ago.minutes", { n: Math.floor(s / 60) });
  if (s < 86400) return t("trackers.ago.hours", { n: Math.floor(s / 3600) });
  if (s < 86400 * 30) return t("trackers.ago.days", { n: Math.floor(s / 86400) });
  return new Date(ts).toISOString().slice(0, 10);
}

/** A readable label colour: GitHub stores label colours without '#'. */
export function labelStyle(color?: string, mode: "dark" | "light" = "dark"): { borderColor?: string; color?: string } {
  if (!color || !/^[0-9a-f]{6}$/i.test(color)) return {};
  // The label's own colour, lightened or darkened only as far as it must be to
  // read on the theme's least favourable surface.
  const surface = themeTokens(mode)[mode === "dark" ? "panel-2" : "bg-soft"];
  return { borderColor: `#${color}99`, color: ensureContrast(`#${color}`, surface) };
}
