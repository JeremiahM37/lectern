// Pure formatting helpers for the usage/cost/context UI added across
// SessionCard, Conversation, NeedsYou, the board and the Usage panel. Kept
// framework-free and dependency-free so they are unit-testable without a DOM
// (see usageFormat.test.ts) and reusable everywhere a number needs to become
// a label.

// contextClass turns a "percent of context window used" reading into the
// card's CSS class: amber at 70%+, red at 85%+ (task contract thresholds),
// otherwise the neutral bar. Anything outside 0-100 is clamped rather than
// producing a nonsensical bar width or class.
export function contextClass(pct: number): "" | "ctx-amber" | "ctx-red" {
  if (pct >= 85) return "ctx-red";
  if (pct >= 70) return "ctx-amber";
  return "";
}

export function clampPct(pct: number): number {
  return Math.max(0, Math.min(100, pct));
}

// formatTokens renders a token count the way a card chip has room for:
// bare below 1000, "12.3k" style above it, and never a decimal on a whole
// thousand ("36k" not "36.0k").
export function formatTokens(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n)) return "";
  const abs = Math.abs(n);
  if (abs < 1000) return String(Math.round(n));
  const thousands = n / 1000;
  const rounded = Math.round(thousands * 10) / 10;
  return (Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)) + "k";
}

// formatCost renders a dollar figure at the precision that actually
// distinguishes two runs: sub-cent activity (most single sessions) gets 3
// decimals, anything a cent or over gets the usual 2.
export function formatCost(usd: number | null | undefined): string {
  if (usd == null || !Number.isFinite(usd)) return "";
  return "$" + usd.toFixed(usd < 0.01 && usd > 0 ? 3 : 2);
}

// formatLineDelta renders "+12 -3" style diff counts, omitting a side that is
// zero so a card with only additions does not show a pointless "-0".
export function formatLineDelta(
  added: number | null | undefined,
  removed: number | null | undefined,
): string {
  const a = added || 0,
    r = removed || 0;
  if (!a && !r) return "";
  const parts: string[] = [];
  if (a) parts.push(`+${a}`);
  if (r) parts.push(`−${r}`);
  return parts.join(" ");
}

// formatCountdown renders a unix-seconds reset time as "resets in 3h 12m" (or
// "resets now" once past), for the 5h/7d quota chip. Returns "" for an unset
// reset so the caller can omit the whole clause.
export function formatCountdown(resetsAt: number | null | undefined, nowSeconds = Date.now() / 1000): string {
  if (!resetsAt) return "";
  const remaining = Math.round(resetsAt - nowSeconds);
  if (remaining <= 0) return "resets now";
  const hours = Math.floor(remaining / 3600);
  const minutes = Math.floor((remaining % 3600) / 60);
  if (hours > 0) return `resets in ${hours}h ${minutes}m`;
  return `resets in ${minutes}m`;
}

// quotaClass mirrors contextClass's thresholds for the 5h/7d rate-limit
// chip: the same "amber at 70, red at 85" reading the task asked for applies
// equally to "percent of quota used".
export function quotaClass(pct: number): "" | "quota-amber" | "quota-red" {
  if (pct >= 85) return "quota-red";
  if (pct >= 70) return "quota-amber";
  return "";
}

// ageSeconds/isStale: the quota chip and the Usage panel both need to show
// "this number is old" rather than a live-looking but dead percentage.
export function isStale(atSeconds: number | null | undefined, nowSeconds = Date.now() / 1000, thresholdSeconds = 1800): boolean {
  if (!atSeconds) return false;
  return nowSeconds - atSeconds > thresholdSeconds;
}

// ResultUsage is what a task attempt's result payload (attempts.result_json,
// via internal/agents/parse.go's enriched "result" event) can carry. Every
// field is optional: a codex attempt has no cost, an older attempt has none
// of the newer context fields, and both are legitimate, not malformed.
export interface ResultUsage {
  costUSD?: number;
  inputTokens?: number;
  outputTokens?: number;
  contextTokens?: number;
  contextSize?: number;
  contextPct?: number;
}

// resultUsage reads whichever of the shapes NormalizeClaude/normalizeCodex
// produce are present (see internal/agents/parse.go): Claude's nested
// "usage" object plus "modelUsage"-derived context_size, or codex's flatter
// input_tokens/output_tokens/tokens. Board/TaskDetail cards use this so they
// do not need to know which driver produced a given attempt.
export function resultUsage(result: Record<string, unknown> | null | undefined): ResultUsage {
  if (!result) return {};
  const num = (v: unknown): number | undefined =>
    typeof v === "number" && Number.isFinite(v) ? v : undefined;
  const usage = (result.usage && typeof result.usage === "object" ? result.usage : {}) as Record<string, unknown>;
  const costUSD = num(result.cost_usd);
  const outputTokens = num(result.output_tokens) ?? num(usage.output_tokens) ?? num(result.tokens);
  const inputTokens = num(result.input_tokens) ?? num(usage.input_tokens);
  const contextTokens = num(result.context_tokens);
  const contextSize = num(result.context_size);
  const contextPct =
    contextTokens != null && contextSize ? Math.min(100, Math.round((contextTokens / contextSize) * 100)) : undefined;
  return { costUSD, inputTokens, outputTokens, contextTokens, contextSize, contextPct };
}

// formatAge renders "3m ago" / "2h ago" for a dimmed, stale reading.
export function formatAge(atSeconds: number | null | undefined, nowSeconds = Date.now() / 1000): string {
  if (!atSeconds) return "";
  const age = Math.max(0, Math.round(nowSeconds - atSeconds));
  if (age < 60) return `${age}s ago`;
  if (age < 3600) return `${Math.floor(age / 60)}m ago`;
  if (age < 86400) return `${Math.floor(age / 3600)}h ago`;
  return `${Math.floor(age / 86400)}d ago`;
}
