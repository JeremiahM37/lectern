// Parent iframe.onload can post before React effects run. Capture that first
// layout message at module evaluation and replay it to the mounted terminal.
import type { ViewportBox } from "./viewport";
export interface EmbeddedLayout {
  compact: boolean;
  mobile: boolean;
}
let latest: EmbeddedLayout | undefined;
let latestViewport: ViewportBox | undefined;
const listeners = new Set<(layout: EmbeddedLayout) => void>();
const viewportListeners = new Set<(viewport: ViewportBox | undefined) => void>();
window.addEventListener("message", (event) => {
  const data: unknown = event.data;
  if (
    event.source !== parent ||
    event.origin !== location.origin ||
    !data ||
    typeof data !== "object" ||
    !("type" in data)
  )
    return;
  if (data.type === "lec-terminal-visible") {
    const compact = "compact" in data && data.compact === true;
    latest = {
      compact,
      mobile: "mobile" in data ? data.mobile === true : compact,
    };
    for (const listener of listeners) listener(latest);
    return;
  }
  if (data.type === "lec-terminal-viewport") {
    const height = "height" in data && typeof data.height === "number" ? data.height : 0,
      top = "top" in data && typeof data.top === "number" && data.top > 0 ? data.top : 0;
    latestViewport = height > 0 ? { top, height } : undefined;
    for (const listener of viewportListeners) listener(latestViewport);
  }
});
export function subscribeLayout(listener: (layout: EmbeddedLayout) => void) {
  listeners.add(listener);
  if (latest) listener(latest);
  return () => {
    listeners.delete(listener);
  };
}
// The phone keyboard changes what a phone can see without resizing the frame,
// so the parent sends the visible slice as the viewport moves.
export function subscribeViewport(listener: (viewport: ViewportBox | undefined) => void) {
  viewportListeners.add(listener);
  if (latestViewport) listener(latestViewport);
  return () => {
    viewportListeners.delete(listener);
  };
}
