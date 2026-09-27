// Pull down at the top of a list to refresh it, on a touch screen. The page
// has overscroll turned off (style.css), so the browser's own gesture never
// competes; this one refreshes the data, not the whole page, and works the
// same inside the Android app's WebView.
import { useEffect, useRef, useState } from "react";
import { dragAxis, pullOffset, PULL_TRIGGER } from "./gestures";
import { haptic, TOUCH_FIRST } from "./haptics";

// Places a pull must never start from: anything that scrolls or selects on
// its own, and anything that is not the list itself.
const IGNORE = "textarea,input,select,dialog,[role=dialog],.xterm,.no-pull,.action-menu[open],[data-swipe-row]";

export function PullToRefresh({ target, onRefresh }: { target: () => HTMLElement | null; onRefresh: () => Promise<unknown> }) {
  const indicator = useRef<HTMLDivElement>(null);
  const [busy, setBusy] = useState(false);
  const latest = useRef(onRefresh);
  latest.current = onRefresh;
  useEffect(() => {
    const el = target();
    if (!el || !matchMedia(TOUCH_FIRST).matches) return;
    let start: { x: number; y: number } | undefined,
      axis: "x" | "y" | undefined,
      offset = 0,
      armed = false,
      refreshing = false;
    const show = (px: number, animate = false) => {
      const node = indicator.current;
      if (!node) return;
      node.style.transition = animate ? "transform .2s ease, opacity .2s" : "none";
      node.style.transform = `translate(-50%, ${px - 44}px) rotate(${px * 4}deg)`;
      node.style.opacity = String(Math.min(1, px / PULL_TRIGGER));
      node.classList.toggle("armed", px >= PULL_TRIGGER);
    };
    const abort = new AbortController(),
      signal = abort.signal;
    el.addEventListener("touchstart", (e) => {
      if (refreshing || e.touches.length !== 1 || window.scrollY > 0) return;
      if (e.target instanceof Element && e.target.closest(IGNORE)) return;
      const t = e.touches[0]!;
      start = { x: t.clientX, y: t.clientY };
      axis = undefined;
      armed = false;
    }, { signal, passive: true });
    el.addEventListener("touchmove", (e) => {
      if (!start) return;
      const t = e.touches[0]!,
        dx = t.clientX - start.x,
        dy = t.clientY - start.y;
      axis ??= dragAxis(dx, dy);
      if (axis !== "y" || dy <= 0 || window.scrollY > 0) {
        if (axis) start = axis === "y" && dy > 0 && window.scrollY <= 0 ? start : undefined;
        if (!start) show(0, true);
        return;
      }
      e.preventDefault();
      offset = pullOffset(dy);
      if (offset >= PULL_TRIGGER && !armed) haptic("tick");
      armed = offset >= PULL_TRIGGER;
      show(offset);
    }, { signal, passive: false });
    const end = () => {
      if (!start) return;
      start = undefined;
      if (!armed) {
        show(0, true);
        return;
      }
      refreshing = true;
      setBusy(true);
      show(PULL_TRIGGER, true);
      void latest.current().catch(() => undefined).finally(() => {
        refreshing = false;
        setBusy(false);
        show(0, true);
      });
    };
    el.addEventListener("touchend", end, { signal });
    el.addEventListener("touchcancel", end, { signal });
    return () => abort.abort();
  }, [target]);
  return (
    // The arrow is drawn by CSS, so the page's text is the same with or
    // without it.
    <div ref={indicator} id="pull-refresh" className={busy ? "busy" : ""} role="status" aria-label={busy ? "Refreshing" : undefined} />
  );
}
