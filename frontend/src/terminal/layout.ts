// Parent iframe.onload can post before React effects run. Capture that first
// layout message at module evaluation and replay it to the mounted terminal.
export interface EmbeddedLayout {
  compact: boolean;
  mobile: boolean;
}
let latest: EmbeddedLayout | undefined;
const listeners = new Set<(layout: EmbeddedLayout) => void>();
window.addEventListener("message", (event) => {
  const data: unknown = event.data;
  if (
    event.source !== parent ||
    event.origin !== location.origin ||
    !data ||
    typeof data !== "object" ||
    !("type" in data) ||
    data.type !== "lec-terminal-visible"
  )
    return;
  const compact = "compact" in data && data.compact === true;
  latest = {
    compact,
    mobile: "mobile" in data ? data.mobile === true : compact,
  };
  for (const listener of listeners) listener(latest);
});
export function subscribeLayout(listener: (layout: EmbeddedLayout) => void) {
  listeners.add(listener);
  if (latest) listener(latest);
  return () => {
    listeners.delete(listener);
  };
}
