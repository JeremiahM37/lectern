// What the Android back key does (MainActivity asks window.__lecternBack):
// close the topmost thing open over the page, then step back through the
// views visited, and only at the first view let the app close. A browser's
// own back button keeps its usual meaning; this is only called by the app.
//
// Overlays whose state lives in React register a close function while open
// (useBackClose); native <dialog>s and open menus are found in the page and
// in its same-origin frames (the terminal) without registering.
import { useEffect, useRef } from "react";

type Close = () => void;
const stack: { id: number; close: Close }[] = [];
let serial = 0;

/** While open, the back key closes this overlay (last opened first). */
export function useBackClose(open: boolean, close: Close) {
  const latest = useRef(close);
  latest.current = close;
  useEffect(() => {
    if (!open) return;
    const entry = { id: ++serial, close: () => latest.current() };
    stack.push(entry);
    return () => {
      const at = stack.indexOf(entry);
      if (at >= 0) stack.splice(at, 1);
    };
  }, [open]);
}

const MENUS = "details.action-menu[open], details.terminal-actions[open], details.ws-layouts[open], details[open][data-back-close]";

function documents(root: Document): Document[] {
  const out = [root];
  for (const frame of root.querySelectorAll("iframe")) {
    try {
      const doc = frame.contentDocument;
      if (doc && frame.getClientRects().length) out.push(...documents(doc));
    } catch {
      /* another origin: not ours to close */
    }
  }
  return out;
}

/** Closes the topmost open <dialog> or menu. Returns whether one was. */
export function closeTopmost(root: Document = document): boolean {
  const docs = documents(root);
  // Frames come after the page, so their dialogs count as on top.
  for (const doc of docs.slice().reverse()) {
    const dialogs = [...doc.querySelectorAll<HTMLDialogElement>("dialog[open]")];
    const dialog = dialogs[dialogs.length - 1];
    if (dialog) {
      const cancel = new Event("cancel", { cancelable: true });
      dialog.dispatchEvent(cancel);
      if (!cancel.defaultPrevented) {
        const button = dialog.querySelector<HTMLElement>("[data-close], .modal-close, .sheet-close");
        if (button) button.click();
        else dialog.close();
      }
      return true;
    }
    const menus = [...doc.querySelectorAll<HTMLDetailsElement>(MENUS)];
    const menu = menus[menus.length - 1];
    if (menu) {
      menu.open = false;
      return true;
    }
  }
  return false;
}

// The views visited, so back returns to the previous one.
const visited: string[] = [];
let go: ((hash: string) => void) | undefined;

let started = 0;

export function noteView(hash: string, now = Date.now()) {
  if (visited[visited.length - 1] === hash) return;
  // The app settles on its first view (a remembered one, or Sessions on a
  // phone) a moment after it starts; that is where back ends, not the
  // default it showed on the way.
  started ||= now;
  if (visited.length === 1 && now - started < 2000) visited[0] = hash;
  else visited.push(hash);
  if (visited.length > 50) visited.shift();
}

export function setViewNavigator(navigate: (hash: string) => void) {
  go = navigate;
}

export function handleBack(): boolean {
  const top = stack[stack.length - 1];
  if (top) {
    top.close();
    return true;
  }
  if (closeTopmost()) return true;
  if (visited.length > 1 && go) {
    visited.pop();
    go(visited[visited.length - 1]!);
    return true;
  }
  return false;
}

declare global {
  interface Window {
    __lecternBack?: () => boolean;
  }
}
if (typeof window !== "undefined") window.__lecternBack = handleBack;
