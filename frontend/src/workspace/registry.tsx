// Pane types. Anything that can sit in the workspace — a terminal, a chat, a
// diff, a file, a browser — registers here once, and the layout, tab strip,
// drag and drop, saved layouts and the phone switcher all work for it
// unchanged. A feature adds a pane type with one call:
//
//   registerPaneType({
//     kind: "browser",
//     label: "Browser",
//     icon: "◎",
//     render: ({ pane, services, close }) => <BrowserPane ... />,
//     check: (ref) => (/^\d+$/.test(ref.params.session || "") ? ref : null),
//   });
//
// and opens one with services.openPane({ id, kind, title, params }).
import { createContext, useSyncExternalStore, type ReactNode } from "react";
import type { SessionsApi } from "../sessions/Sessions";
import type { SessionView } from "../types";
import type { Edge, PaneRef } from "./layout";

// What a pane can reach in the app around it.
export interface PaneServices {
  api: SessionsApi;
  sessions: SessionView[];
  notice(text: string, error?: boolean): void;
  attach(sessionId: number): void;
  openPane(ref: PaneRef, options?: { beside?: string; edge?: Edge }): void;
  closePane(id: string): void;
}

export interface PaneRenderProps {
  pane: PaneRef;
  // On screen in the layout, and the workspace itself is showing.
  shown: boolean;
  visible: boolean;
  focused: boolean;
  primary: boolean;
  mobile: boolean;
  compact: boolean;
  services: PaneServices;
  close(): void;
  // Makes this pane's group the focused one (a click inside a frame cannot
  // tell the page by itself).
  focus(): void;
  relative(delta: number): boolean;
}

export interface PaneType {
  kind: string;
  label: string;
  icon: string;
  render(props: PaneRenderProps): ReactNode;
  // Validates a stored or restored ref; null drops it.
  check?(ref: PaneRef): PaneRef | null;
  // A same-origin address to open this pane in its own browser tab.
  popout?(ref: PaneRef): string | undefined;
  // Panes rendered as a native dialog in their standalone form show inline here.
  embedsDialog?: boolean;
}

const types = new Map<string, PaneType>();
let version = 0;
const listeners = new Set<() => void>();

export function registerPaneType(type: PaneType) {
  types.set(type.kind, type);
  version++;
  for (const listener of listeners) listener();
  return () => {
    if (types.get(type.kind) === type) types.delete(type.kind);
    version++;
    for (const listener of listeners) listener();
  };
}

export function paneType(kind: string) {
  return types.get(kind);
}

export function paneTypes() {
  return [...types.values()];
}

export function checkPane(ref: PaneRef): PaneRef | null {
  const type = types.get(ref.kind);
  if (!type) return null;
  return type.check ? type.check(ref) : ref;
}

export function usePaneTypes() {
  useSyncExternalStore(
    (listener) => {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    () => version,
    () => 0,
  );
  return paneTypes();
}

// True inside a workspace pane. A component that is a modal dialog on its own
// (Modal, Review) shows itself inline instead when it finds this set.
export const PaneEmbedContext = createContext(false);
