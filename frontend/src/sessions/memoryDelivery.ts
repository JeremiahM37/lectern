// Pure helpers for the Memory section (docs/memory-visibility.md): what the
// agent was handed, when, and what each delivered record was. Framework-free
// and dependency-free so the shapes are unit-testable without a DOM
// (see memoryDelivery.test.ts).

export interface MemoryItem {
  id?: string;
  source?: string;
  title?: string;
  snippet?: string;
}

export interface MemoryDelivery {
  id: number;
  session_id: number | null;
  task_id: number | null;
  attempt_id: number | null;
  // Unix seconds, as every other timestamp in lectern's API is.
  at: number;
  mode: string;
  bytes: number;
  items: MemoryItem[];
}

// modeLabel says what the agent was allowed to read, in words. "Why did the
// agent see this" is usually answered by "what was it permitted to search",
// and the raw mode name is not a sentence. An unrecognised mode is shown as
// itself rather than hidden — a newer store's mode is still information.
const MODES: Record<string, string> = {
  scoped: "this project's notes",
  managed: "the project's own note",
  all: "the whole store",
  manual: "looked up by the agent itself",
  off: "nothing",
};

export function modeLabel(mode: string): string {
  const trimmed = (mode || "").trim();
  if (!trimmed) return "";
  return MODES[trimmed] || trimmed;
}

// itemLabel is what to call one delivered record when its store did not send a
// title. source (a note path) is the next most useful, and the id is the last
// resort — an unnamed item is shown as something, never as nothing.
export function itemLabel(item: MemoryItem): string {
  return (item.title || "").trim() || (item.source || "").trim() || (item.id || "").trim() || "untitled memory";
}

// formatBytes renders a delivery's size the way the header has room for:
// exact bytes below a kilobyte, one decimal above it.
export function formatBytes(n: number | null | undefined): string {
  if (n == null || !Number.isFinite(n) || n <= 0) return "0 B";
  if (n < 1024) return `${Math.round(n)} B`;
  const kb = n / 1024;
  if (kb < 1024) return `${kb < 10 ? kb.toFixed(1) : Math.round(kb)} KB`;
  return `${(kb / 1024).toFixed(1)} MB`;
}

export function itemCountLabel(n: number): string {
  if (n <= 0) return "no items";
  return n === 1 ? "1 item" : `${n} items`;
}

// deliverySummary is the one-line reading of a delivery: how much went out,
// how big it was, and where it came from. Empty parts are dropped rather than
// rendering a dangling separator.
export function deliverySummary(delivery: MemoryDelivery): string {
  const parts = [itemCountLabel(delivery.items.length), formatBytes(delivery.bytes)];
  const mode = modeLabel(delivery.mode);
  if (mode) parts.push(mode);
  return parts.join(" · ");
}

// deliveryItems flattens every item a set of deliveries handed over, pairings
// kept with the delivery it arrived in. The task timeline uses this to show
// the memory as part of the run's story rather than in a separate list.
export function deliveryItems(
  deliveries: MemoryDelivery[],
): { delivery: MemoryDelivery; item: MemoryItem }[] {
  return deliveries.flatMap((delivery) => delivery.items.map((item) => ({ delivery, item })));
}

// itemPath builds the review URL for one delivered item. The id comes from the
// store and may contain a path separator or a '#' (a fingerprint), so it is
// always encoded.
export function itemPath(id: string, action: "feedback" | "challenge"): string {
  return `/memory/items/${encodeURIComponent(id)}/${action}`;
}
