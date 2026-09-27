// Turns key presses into registry actions. Each document (the app, and every
// terminal frame) installs one listener for the contexts it can serve; an
// action runs the most recently registered handler for its id. Chords for app
// actions pressed inside a terminal frame are posted to the app when the
// terminal can spare them (chords.terminalSafe).
import { useEffect, useRef } from "react";
import { getPref, subscribePrefs } from "../prefs/store";
import { eventChord, terminalSafe, type KeyLike } from "./chords";
import { bindings, cleanOverrides, SHORTCUTS, type ShortcutContext } from "./registry";

// A handler may return false to say "not now", letting the key through.
export type ShortcutHandler = (event?: KeyboardEvent) => void | boolean;

const EMPTY = {};
let current = compute();
function compute() {
  return bindings(cleanOverrides(getPref("shortcuts", EMPTY)));
}
subscribePrefs(() => {
  current = compute();
});
export function currentBindings() {
  return current;
}
export function chordsFor(id: string): string[] {
  return current.get(id) || [];
}

const handlers = new Map<string, ShortcutHandler[]>();

export function registerShortcut(id: string, handler: ShortcutHandler) {
  const list = handlers.get(id) || [];
  handlers.set(id, [...list, handler]);
  return () => {
    const rest = (handlers.get(id) || []).filter((row) => row !== handler);
    if (rest.length) handlers.set(id, rest);
    else handlers.delete(id);
  };
}

export function hasShortcutHandler(id: string) {
  return !!handlers.get(id)?.length;
}

export function runShortcut(id: string, event?: KeyboardEvent): boolean {
  const list = handlers.get(id);
  if (!list?.length) return false;
  return list[list.length - 1]!(event) !== false;
}

// Registers a set of handlers for the life of a component. The latest
// closures are always used, so callers need not memoise them.
export function useShortcuts(map: Record<string, ShortcutHandler>, enabled = true) {
  const latest = useRef(map);
  latest.current = map;
  const ids = Object.keys(map).sort().join(" ");
  useEffect(() => {
    if (!enabled) return;
    const off = ids.split(" ").filter(Boolean).map((id) => registerShortcut(id, (event) => latest.current[id]?.(event)));
    return () => off.forEach((fn) => fn());
  }, [ids, enabled]);
}

// Which action a chord means in the given contexts, in registry order.
export function actionFor(chord: string, contexts: ShortcutContext[], table = current): string | undefined {
  for (const row of SHORTCUTS) {
    if (!contexts.includes(row.context)) continue;
    if (table.get(row.id)?.includes(chord)) return row.id;
  }
  return undefined;
}

function editable(target: EventTarget | null) {
  if (!(target instanceof HTMLElement)) return false;
  if (target.classList.contains("xterm-helper-textarea")) return false;
  return target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName);
}

// Plain keys, and Shift+key, are typing when a field has focus.
export function typingChord(chord: string) {
  return !/(^|\+)(Ctrl|Alt|Meta)\+/.test(chord) && !/^F\d{1,2}$|\+F\d{1,2}$/.test(chord) && !/(^|\+)(Escape)$/.test(chord);
}

export interface ListenerOptions {
  contexts: ShortcutContext[];
  // Contexts whose chords are posted to the parent document instead.
  forward?: ShortcutContext[];
  target?: Window;
  // Checked per key: a context may only be live some of the time.
  active?: (context: ShortcutContext) => boolean;
}

export function handleShortcutKey(event: KeyboardEvent & KeyLike, options: ListenerOptions): boolean {
  if (event.isComposing || event.repeat && !/Tab|PageUp|PageDown|F3/.test(event.key)) return false;
  const chord = eventChord(event);
  if (!chord) return false;
  // A field recording a new chord owns every key.
  if (event.target instanceof Element && event.target.closest("[data-shortcut-capture]")) return false;
  if (editable(event.target) && typingChord(chord)) return false;
  const live = options.contexts.filter((context) => options.active?.(context) ?? true);
  const id = actionFor(chord, live);
  if (id && runShortcut(id, event)) {
    event.preventDefault();
    event.stopPropagation();
    return true;
  }
  if (options.forward?.length && terminalSafe(chord)) {
    const forwarded = actionFor(chord, options.forward);
    if (forwarded && parent !== window) {
      event.preventDefault();
      event.stopPropagation();
      parent.postMessage({ type: "lec-shortcut", id: forwarded, shift: event.shiftKey, ctrl: event.ctrlKey }, location.origin);
      return true;
    }
  }
  return false;
}

export function installShortcutListener(options: ListenerOptions) {
  const target = options.target ?? window;
  const listener = (event: KeyboardEvent) => void handleShortcutKey(event, options);
  target.addEventListener("keydown", listener, { capture: true });
  return () => target.removeEventListener("keydown", listener, { capture: true });
}

// The app side of forwarding: runs an action a same-origin frame posted.
export function installForwardedShortcuts(onForwarded?: (id: string) => void) {
  const listener = (event: MessageEvent) => {
    if (event.origin !== location.origin || !event.data || typeof event.data !== "object") return;
    const data = event.data as { type?: unknown; id?: unknown };
    if (data.type !== "lec-shortcut" || typeof data.id !== "string") return;
    // Only frames this page embeds may drive it.
    const frames = [...document.querySelectorAll("iframe")].map((frame) => frame.contentWindow);
    if (!frames.includes(event.source as Window)) return;
    onForwarded?.(data.id);
    runShortcut(data.id);
  };
  window.addEventListener("message", listener);
  return () => window.removeEventListener("message", listener);
}
