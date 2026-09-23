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

// Overlay keyboards can leave BOTH viewports unchanged. Their geometry is
// still authoritative when supplied by the VirtualKeyboard API. Observe it
// without opting the browser into overlay mode ourselves.
interface KeyboardGeometry extends EventTarget {
  readonly boundingRect: DOMRectReadOnly;
  readonly overlaysContent?: boolean;
}
export function virtualKeyboard(): KeyboardGeometry | undefined {
  return (navigator as Navigator & { virtualKeyboard?: KeyboardGeometry }).virtualKeyboard;
}

export function visibleViewport(): ViewportBox {
  const view = window.visualViewport,
    top = view?.offsetTop ?? 0,
    height = view?.height ?? window.innerHeight,
    left = view?.offsetLeft ?? 0,
    width = view?.width ?? window.innerWidth,
    api = virtualKeyboard(),
    keyboard = api?.overlaysContent ? api.boundingRect : undefined;
  let bottom = top + height;
  if (keyboard && keyboard.height > 0 && keyboard.width > 0 &&
      Number.isFinite(keyboard.top + keyboard.height + keyboard.left + keyboard.width) &&
      keyboard.left < left + width && keyboard.left + keyboard.width > left &&
      keyboard.top + keyboard.height > top && keyboard.top < bottom) {
    // Android Chromium before M152 reports window insets as a rectangle
    // (crbug.com/493416495): y can be the browser toolbar height, not the
    // keyboard's top. For its full-width docked keyboard, height is usable.
    // Do not apply this workaround to floating keyboards or other engines.
    const chrome = navigator.userAgent.match(/(?:Chrome|Chromium)\/(\d+)/),
      legacyAndroid = /Android/.test(navigator.userAgent) && chrome && Number(chrome[1]) < 152,
      docked = Math.abs(keyboard.left) < 2 && keyboard.width >= window.innerWidth - 2,
      keyboardTop = legacyAndroid && docked ? window.innerHeight - keyboard.height : keyboard.top;
    bottom = Math.min(bottom, Math.max(top, keyboardTop));
  }
  return { top, height: Math.max(0, bottom - top) };
}

// Only the parent knows how much of an embedded frame is above the keyboard.
export function viewportSliceFor(node: HTMLElement): ViewportBox {
  const rect = node.getBoundingClientRect();
  return visibleSlice({ top: rect.top, height: rect.height }, visibleViewport());
}

export function localViewportSlice(): ViewportBox {
  const height = window.innerHeight || document.documentElement.clientHeight || 0;
  return visibleSlice({ top: 0, height }, visibleViewport());
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
