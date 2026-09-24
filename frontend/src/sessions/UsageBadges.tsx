// Shared usage badges for a session: context bar, cost, line delta and a
// compaction warning. One component set used from SessionCard, the
// Conversation header and the Needs-you rows so the three surfaces the task
// asked for never drift out of sync with each other.
import type { Session } from "../types";
import { clampPct, contextClass, formatCost, formatLineDelta, formatTokens } from "./usageFormat";

// UsageFields is the subset of a session row these badges read — a Pick
// rather than the whole Session so a caller with a partial/joined shape
// (e.g. a Needs-you row) can pass it directly.
export type UsageFields = Pick<
  Session,
  | "context_pct"
  | "context_used_pct"
  | "context_tokens"
  | "context_size"
  | "cost_usd"
  | "lines_added"
  | "lines_removed"
  | "precompact_at"
>;

// ContextBadge prefers the agent-reported context_used_pct (statusline or
// codex rollout — "percent of the window used", high is bad, amber >=70%
// red >=85% per the task contract) and falls back to the older
// screen-scraped context_pct ("percent left until auto-compact", low is bad)
// only when a session has never reported the newer field — see
// docs/agent-events.md's "Correction" note for why these are two different
// columns rather than one.
export function ContextBadge({ session, tag = "ctx" }: { session: UsageFields; tag?: string }) {
  if (session.context_used_pct != null) {
    const pct = clampPct(session.context_used_pct);
    const cls = contextClass(pct);
    const hasDetail = session.context_tokens != null && session.context_size != null;
    const title = hasDetail
      ? `${formatTokens(session.context_tokens)} / ${formatTokens(session.context_size)} tokens used`
      : undefined;
    return (
      <span className={`ctxbar ctx-used ${cls}`} title={title}>
        {tag}{" "}
        <i>
          <b style={{ width: `${pct}%` }} />
        </i>{" "}
        {pct}%
      </span>
    );
  }
  if (session.context_pct != null) {
    const pct = session.context_pct;
    return (
      <span
        className={`ctxbar ${pct <= 10 ? "crit" : pct <= 25 ? "low" : ""}`}
        title="Percent of context left until auto-compact"
      >
        {tag}{" "}
        <i>
          <b style={{ width: `${clampPct(pct)}%` }} />
        </i>{" "}
        {pct}%
      </span>
    );
  }
  return null;
}

export function CostBadge({ session }: { session: UsageFields }) {
  if (session.cost_usd == null) return null;
  return <span className="chip cost">{formatCost(session.cost_usd)}</span>;
}

export function LinesBadge({ session }: { session: UsageFields }) {
  const label = formatLineDelta(session.lines_added, session.lines_removed);
  if (!label) return null;
  return <span className="chip ds">{label}</span>;
}

// COMPACTION_WARNING_WINDOW_S: how long a PreCompact hook keeps showing a
// warning after it fires — long enough to notice on a card that only
// refreshes every so often, short enough that a compaction from an hour ago
// does not read as still happening.
const COMPACTION_WARNING_WINDOW_S = 120;

export function CompactionWarning({ session, now = Date.now() / 1000 }: { session: UsageFields; now?: number }) {
  const nearFull = session.context_used_pct != null && session.context_used_pct >= 85;
  const recentlyCompacted = session.precompact_at != null && now - session.precompact_at < COMPACTION_WARNING_WINDOW_S;
  if (!nearFull && !recentlyCompacted) return null;
  return (
    <span
      className="chip warn compaction-warning"
      title={recentlyCompacted ? "A context compaction just ran" : "Context window is nearly full"}
    >
      {recentlyCompacted ? "⚠ compacting" : `⚠ context ${session.context_used_pct}%`}
    </span>
  );
}
