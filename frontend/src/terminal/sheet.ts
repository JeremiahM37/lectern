// On a phone the terminal's menus (Tools inside the frame; the overflow ⋯,
// the new-terminal caret and saved layouts in the tab bar) open as bottom
// sheets over a dimmed page instead of desktop dropdowns hanging off a
// button. A menu that is a sheet carries the class "menu-sheet"; its dimmed
// backdrop is the <details> element's own ::before, so a tap there lands on
// the <details> itself and closes it. The desktop menus are untouched.

/** Phone-width, or a touch screen no wider than a tablet. */
export const SHEET_QUERY = "(max-width: 600px), (max-width: 1023px) and (pointer: coarse)";

export function wantsSheet(): boolean {
  return typeof matchMedia === "function" && matchMedia(SHEET_QUERY).matches;
}

export interface SheetBounds {
  /** Visible area, in the panel's own client coordinates. */
  top: number;
  bottom: number;
  left: number;
  width: number;
  /** The control that opened the menu, when it sits above the sheet. */
  anchor?: DOMRect;
}

/** Where a sheet's top edge may reach: below the button that opened it when
 * that leaves a usable sheet, so a second tap on the button still closes it. */
export function sheetCeiling(bounds: SheetBounds, minimum = 200): number {
  const gap = 8;
  let ceiling = bounds.top + 24;
  const anchor = bounds.anchor;
  if (anchor && anchor.bottom <= bounds.bottom - minimum) ceiling = Math.max(ceiling, anchor.bottom + gap);
  if (bounds.bottom - ceiling < minimum) ceiling = bounds.top + gap;
  return Math.min(ceiling, bounds.bottom);
}

/**
 * Lays out an open menu panel as a sheet resting on the bottom of the visible
 * area. Toggles "menu-sheet" on the <details> and reports whether the menu is
 * a sheet; when it is not, the caller places its dropdown as before. Runs
 * synchronously on activation, so the first painted frame is already placed.
 */
export function placeSheet(details: HTMLDetailsElement, panel: HTMLElement, bounds: SheetBounds): boolean {
  const sheet = details.open && wantsSheet(),
    was = details.classList.contains("menu-sheet");
  details.classList.toggle("menu-sheet", sheet);
  if (!sheet) {
    // Back to a dropdown (a rotation, a wider window): its own rules apply.
    if (was) for (const name of ["position", "left", "top", "width", "max-height"]) panel.style.removeProperty(name);
    return false;
  }
  // Each time it opens, a sheet starts at its top.
  if (!was) panel.scrollTop = 0;
  const ceiling = sheetCeiling(bounds);
  panel.style.position = "fixed";
  panel.style.left = Math.round(bounds.left) + "px";
  panel.style.width = Math.round(bounds.width) + "px";
  panel.style.maxHeight = Math.max(1, Math.round(bounds.bottom - ceiling)) + "px";
  panel.style.top = "0px";
  const height = panel.getBoundingClientRect().height;
  panel.style.top = Math.round(Math.max(ceiling, bounds.bottom - height)) + "px";
  return true;
}

// A tap on the dimmed backdrop (the sheet's <details>, outside its summary and
// panel) closes the sheet. Click, not pointerdown: closing on pointerdown would
// let the tap's click land on whatever was under the backdrop.
function closeFromBackdrop(event: MouseEvent) {
  const target = event.target;
  if (target instanceof HTMLDetailsElement && target.open && target.classList.contains("menu-sheet")) {
    event.preventDefault();
    event.stopPropagation();
    target.open = false;
  }
}
if (typeof document !== "undefined") document.addEventListener("click", closeFromBackdrop, true);
