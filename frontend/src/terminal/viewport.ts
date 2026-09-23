// A phone keyboard overlays the visual viewport instead of resizing the
// embedded terminal frame: the frame keeps its full height while the part of
// it a phone can see gets shorter, and the caret ends up behind the keys. The
// parent measures the slice of the frame that is actually visible and tells
// the terminal page how tall to be, so the PTY — and a full-screen app that
// draws its input line at the bottom — fits above the keyboard.
export interface ViewportRect {
  top: number;
  height: number;
}
export interface ViewportBox {
  top: number;
  height: number;
}

export function visibleSlice(element: ViewportRect, view: ViewportRect): ViewportBox {
  const start = Math.max(element.top, view.top),
    end = Math.min(element.top + element.height, view.top + view.height);
  return { top: Math.max(0, start - element.top), height: Math.max(0, end - start) };
}

// The top-level visual viewport is the only one that knows a phone keyboard is
// open; an embedded frame keeps reporting its own, unchanged, size.
export function viewportSliceFor(node: HTMLElement): ViewportBox {
  const rect = node.getBoundingClientRect(),
    view = window.visualViewport;
  return visibleSlice(
    { top: rect.top, height: rect.height },
    view
      ? { top: view.offsetTop, height: view.height }
      : { top: 0, height: window.innerHeight },
  );
}

// The standalone terminal page has no parent to measure for it.
export function localViewportSlice(): ViewportBox {
  const height = window.innerHeight || document.documentElement.clientHeight || 0,
    view = window.visualViewport;
  return visibleSlice(
    { top: 0, height },
    view ? { top: view.offsetTop, height: view.height } : { top: 0, height },
  );
}

// Applies the measured slice to the terminal page. A slice that covers the
// whole frame is not a fit: clearing the override returns the page to its
// natural 100dvh sizing.
export function applyVisibleHeight(slice: ViewportBox | null): boolean {
  if (typeof document === "undefined" || !document.body) return false;
  const root = document.documentElement,
    natural = window.innerHeight || root.clientHeight || 0,
    usable = slice
      ? Math.round(Math.max(0, slice.top) + Math.max(0, slice.height))
      : 0,
    fitted = !!slice && usable > 0 && natural > 0 && usable < natural - 1;
  if (!fitted) {
    root.style.removeProperty("--lec-visible-height");
    document.body.classList.remove("fitted-viewport");
    return false;
  }
  root.style.setProperty("--lec-visible-height", `${usable}px`);
  document.body.classList.add("fitted-viewport");
  return true;
}
