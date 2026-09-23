import type { Terminal } from "@xterm/xterm";
interface ScrollOptions {
  host: HTMLElement;
  term: Terminal;
  enabled: () => boolean;
  // A finger held still. xterm has no touch selection, so the page answers a
  // long press by showing the buffer as text the phone can select natively.
  longPress?: () => void;
  // A deliberate horizontal flick on the terminal body. The same gesture owner
  // that scrolls and turns a long press into text decides it, so a plain shell
  // and a full-screen app with mouse reporting behave alike.
  swipe?: (direction: 1 | -1) => void;
  selection?: () => boolean;
  retainedHistory: (lines: number) => void;
  liveIntent: () => void;
  autoscrollHost?: HTMLElement;
  historyViewport?: () => HTMLElement | null;
}
interface Gesture {
  id: number;
  y: number;
  x: number;
  start: number;
  startX: number;
  startedAt: number;
  axis?: "x" | "y";
  swipe: boolean;
  time: number;
  remainder: number;
  velocity: number;
  dragged: boolean;
}
interface Auto {
  origin: number;
  y: number;
  x: number;
  last: number;
  remainder: number;
  marker: HTMLElement;
}
// A full-screen chat owns its transcript; scrolling xterm's empty buffer cannot
// move it. Route touch drags through xterm's negotiated mouse protocol instead.
export function installTerminalScroll({
  host,
  term,
  enabled,
  retainedHistory,
  liveIntent,
  autoscrollHost = host,
  historyViewport = () => null,
  longPress,
  swipe,
  selection = () => false,
}: ScrollOptions) {
  let gesture: Gesture | null = null,
    held: number | undefined,
    momentum: number | undefined,
    disposed = false;
  const linePixels = () => Math.max(8, (term.options.fontSize || 15) * 0.8);
  // tmux itself uses the alternate screen even for a plain shell. Only a
  // negotiated mouse protocol establishes that the application owns scrolling.
  const appScroll = () => term.modes.mouseTrackingMode !== "none";
  const stop = () => {
    if (momentum !== undefined) cancelAnimationFrame(momentum);
    momentum = undefined;
  };
  function scroll(lines: number, x: number, y: number) {
    if (!lines || !enabled()) return;
    const history = historyViewport();
    if (history) {
      history.scrollTop += lines * linePixels();
      return;
    }
    if (lines > 0) liveIntent();
    if (appScroll()) {
      const viewport = host.querySelector(".xterm-viewport") || host;
      for (let i = 0; i < Math.min(30, Math.abs(lines)); i++) {
        viewport.dispatchEvent(
          new WheelEvent("wheel", {
            deltaY: Math.sign(lines) * 120,
            bubbles: true,
            cancelable: true,
            clientX: x,
            clientY: y,
          }),
        );
      }
    } else if (lines < 0 && term.buffer.active.viewportY === 0) {
      stop();
      retainedHistory(lines);
    } else term.scrollLines(lines);
  }
  const controller = new AbortController();
  const listen = <K extends keyof HTMLElementEventMap>(
    type: K,
    fn: (event: HTMLElementEventMap[K]) => void,
    options: AddEventListenerOptions = {},
  ) =>
    host.addEventListener(type, fn, { ...options, signal: controller.signal });
  listen(
    "wheel",
    (e) => {
      if (e.deltaY > 0 && enabled()) liveIntent();
      // Native mouse/trackpad scrolling already works inside mouse-driven apps.
      // At the top of a plain terminal, fetch history from before this attachment.
      if (
        e.ctrlKey ||
        e.deltaY >= 0 ||
        !enabled() ||
        appScroll() ||
        term.buffer.active.viewportY !== 0
      )
        return;
      e.preventDefault();
      e.stopPropagation();
      retainedHistory(
        -Math.max(3, Math.round(Math.abs(e.deltaY) / linePixels())),
      );
    },
    { capture: true, passive: false },
  );
  listen(
    "pointerdown",
    (e) => {
      if (e.pointerType === "touch" && !e.isPrimary) {
        // A second finger is a pinch to resize the type; the drag the first
        // finger began must not keep scrolling underneath it.
        gesture = null;
        clearTimeout(held);
        stop();
        return;
      }
      if (e.pointerType !== "touch" || !e.isPrimary || !enabled()) return;
      stop();
      clearTimeout(held);
      if (longPress)
        held = window.setTimeout(() => {
          // Still down and never moved: a press, not the start of a scroll.
          if (gesture && !gesture.dragged) {
            gesture = null;
            longPress();
          }
        }, 520);
      const started = performance.now();
      gesture = {
        id: e.pointerId,
        y: e.clientY,
        x: e.clientX,
        start: e.clientY,
        startX: e.clientX,
        startedAt: started,
        swipe: !selection(),
        time: started,
        remainder: 0,
        velocity: 0,
        dragged: false,
      };
    },
    { capture: true },
  );
  listen(
    "pointermove",
    (e) => {
      if (!gesture || e.pointerId !== gesture.id) return;
      const g = gesture,
        now = performance.now(),
        dx = e.clientX - g.startX,
        dy = e.clientY - g.start;
      // One axis wins for the whole gesture, so a vertical read that backtracks
      // sideways never turns into a tab change, and a swipe that drifts never
      // scrolls the terminal underneath it.
      if (!g.axis) {
        if (Math.abs(dx) < 14 && Math.abs(dy) < 6) return;
        g.axis =
          Math.abs(dx) >= 14 && Math.abs(dx) > Math.abs(dy) * 1.5 ? "x" : "y";
      }
      if (g.axis === "x") {
        // Sideways counts too: a slow swipe between terminals is not a press.
        clearTimeout(held);
        g.dragged = true;
        try {
          host.setPointerCapture(e.pointerId);
        } catch {}
        e.preventDefault();
        e.stopPropagation();
        return;
      }
      if (Math.abs(dy) < 6) return;
      g.dragged = true;
      clearTimeout(held);
      try {
        host.setPointerCapture(e.pointerId);
      } catch {}
      const delta = g.y - e.clientY;
      g.velocity = delta / Math.max(1, now - g.time);
      g.remainder += delta;
      const lines = Math.trunc(g.remainder / linePixels());
      g.remainder -= lines * linePixels();
      g.y = e.clientY;
      g.x = e.clientX;
      g.time = now;
      e.preventDefault();
      e.stopPropagation();
      scroll(lines, g.x, g.y);
    },
    { capture: true, passive: false },
  );
  // Prevent xterm's native touch handler from consuming the same gesture twice.
  listen(
    "touchmove",
    (e) => {
      if (gesture?.dragged) {
        e.preventDefault();
        e.stopPropagation();
      }
    },
    { capture: true, passive: false },
  );
  function end(e: PointerEvent) {
    if (!gesture || e.pointerId !== gesture.id) return;
    const g = gesture;
    gesture = null;
    clearTimeout(held);
    try {
      host.releasePointerCapture(e.pointerId);
    } catch {}
    if (!g.dragged || e.type === "pointercancel") return;
    if (g.axis === "x") {
      // A flick, not a scroll: hand it to the tab owner.
      const dx = e.clientX - g.startX,
        dy = e.clientY - g.start,
        width = host.getBoundingClientRect().width || 320,
        threshold = Math.max(56, Math.min(120, width * 0.18));
      e.preventDefault();
      e.stopPropagation();
      if (
        g.swipe &&
        enabled() &&
        !selection() &&
        performance.now() - g.startedAt <= 650 &&
        Math.abs(dx) >= threshold &&
        Math.abs(dy) <= Math.max(48, Math.abs(dx) * 0.7)
      )
        swipe?.(dx < 0 ? 1 : -1);
      return;
    }
    if (performance.now() - g.time > 100) return;
    e.preventDefault();
    e.stopPropagation();
    let last = performance.now(),
      velocity = g.velocity,
      remainder = 0;
    const step = (now: number) => {
      if (disposed || !enabled()) return;
      const dt = Math.min(40, now - last);
      last = now;
      velocity *= Math.pow(0.9, dt / 16);
      if (Math.abs(velocity) < 0.03) return;
      remainder += velocity * dt;
      const lines = Math.trunc(remainder / linePixels());
      remainder -= lines * linePixels();
      scroll(lines, g.x, g.y);
      momentum = requestAnimationFrame(step);
    };
    momentum = requestAnimationFrame(step);
  }
  listen("pointerup", end, { capture: true });
  listen("pointercancel", end, { capture: true });
  // Browser-native autoscroll is unavailable on many Linux browsers and cannot
  // reach tmux history or an app's mouse protocol. Use the same routing as touch.
  let auto: Auto | null = null,
    autoFrame: number | undefined;
  const stopAuto = () => {
    if (autoFrame !== undefined) cancelAnimationFrame(autoFrame);
    auto?.marker.remove();
    auto = null;
    autoscrollHost.classList.remove("autoscrolling");
  };
  const globalListen = <
    K extends keyof (WindowEventMap & DocumentEventMap & HTMLElementEventMap),
  >(
    target: Document | Window | HTMLElement,
    type: K,
    fn: (
      event: (WindowEventMap & DocumentEventMap & HTMLElementEventMap)[K],
    ) => void,
    options: AddEventListenerOptions = {},
  ) =>
    target.addEventListener(type, fn as EventListener, {
      ...options,
      signal: controller.signal,
    });
  function autoStep(now: number) {
    if (!auto || disposed || !enabled()) {
      stopAuto();
      return;
    }
    const a = auto,
      dt = Math.min(50, now - a.last);
    a.last = now;
    const distance = a.y - a.origin;
    const speed =
      Math.sign(distance) *
      Math.min(
        160,
        Math.pow(Math.max(0, Math.abs(distance) - 10) / 24, 1.5) * 8,
      );
    a.remainder += (speed * dt) / 1000;
    const lines = Math.trunc(a.remainder);
    a.remainder -= lines;
    const box = host.getBoundingClientRect();
    scroll(
      lines,
      Math.max(box.left + 1, Math.min(box.right - 1, a.x)),
      Math.max(box.top + 1, Math.min(box.bottom - 1, a.y)),
    );
    autoFrame = requestAnimationFrame(autoStep);
  }
  globalListen(
    document,
    "pointerdown",
    (e) => {
      if (e.defaultPrevented) return;
      if (auto) {
        stopAuto();
        e.preventDefault();
        e.stopPropagation();
        return;
      }
      if (
        e.button !== 1 ||
        !(e.target instanceof Node && autoscrollHost.contains(e.target)) ||
        !enabled()
      )
        return;
      e.preventDefault();
      e.stopPropagation();
      stop();
      const marker = document.createElement("span");
      marker.className = "terminal-autoscroll-marker";
      marker.textContent = "↕";
      marker.setAttribute("aria-hidden", "true");
      marker.style.left = e.clientX + "px";
      marker.style.top = e.clientY + "px";
      document.body.append(marker);
      autoscrollHost.classList.add("autoscrolling");
      auto = {
        origin: e.clientY,
        y: e.clientY,
        x: e.clientX,
        last: performance.now(),
        remainder: 0,
        marker,
      };
      autoFrame = requestAnimationFrame(autoStep);
    },
    { capture: true, passive: false },
  );
  globalListen(
    document,
    "pointermove",
    (e) => {
      if (auto) {
        auto.x = e.clientX;
        auto.y = e.clientY;
      }
    },
    { capture: true },
  );
  globalListen(
    autoscrollHost,
    "auxclick",
    (e) => {
      if (e.button === 1) {
        e.preventDefault();
        e.stopPropagation();
      }
    },
    { capture: true },
  );
  globalListen(
    document,
    "keydown",
    (e) => {
      if (!auto) return;
      stopAuto();
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
      }
    },
    { capture: true },
  );
  globalListen(window, "blur", stopAuto);
  globalListen(window, "pagehide", stopAuto);
  globalListen(document, "visibilitychange", () => {
    if (document.hidden) stopAuto();
  });
  return () => {
    disposed = true;
    stop();
    stopAuto();
    controller.abort();
  };
}
