// Quick commands: saved text a person sends to a terminal with one tap or
// chord — "y", "continue", "/compact", "npm test". Everywhere-commands follow
// the person across devices (prefs key "quick-commands"); a project's own
// commands (key "quick-commands:project:<id>") show only in its terminals.
// The terminal key bar, the Snippets sheet, Settings and the numbered
// shortcuts all read this one list.
import type { JsonValue } from "../api/client";
import { getPref, setPref } from "../prefs/store";
import { defaultSnippets, loadSnippets } from "../terminal/snippets";

export interface QuickCommand {
  id: string;
  label: string;
  text: string;
  // Sent with Enter, which is what a reply wants; off for a fragment to edit.
  enter: boolean;
}
export type QuickScope = { kind: "global" } | { kind: "project"; id: number };

export const GLOBAL_KEY = "quick-commands";
export const projectKey = (id: number) => `quick-commands:project:${id}`;
export const scopeKey = (scope: QuickScope) => (scope.kind === "global" ? GLOBAL_KEY : projectKey(scope.id));
const MAX = 60;

let serial = 0;
export function quickId() {
  serial = (serial + 1) % 1e6;
  return "q" + Date.now().toString(36) + serial.toString(36);
}

export function cleanQuickCommands(value: unknown): QuickCommand[] {
  if (!Array.isArray(value)) return [];
  const out: QuickCommand[] = [];
  for (const row of value) {
    if (!row || typeof row !== "object") continue;
    const item = row as Partial<QuickCommand>;
    if (typeof item.text !== "string" || !item.text) continue;
    out.push({
      id: typeof item.id === "string" && item.id ? item.id.slice(0, 40) : quickId(),
      label: typeof item.label === "string" ? item.label.slice(0, 60) : "",
      text: item.text.slice(0, 2000),
      enter: item.enter !== false,
    });
    if (out.length >= MAX) break;
  }
  return out;
}

// Before commands were stored on the server, each device kept its own
// snippets; the first read on such a device carries them over.
export function readGlobal(): QuickCommand[] {
  const stored = getPref<unknown>(GLOBAL_KEY, undefined);
  if (stored !== undefined) return cleanQuickCommands(stored);
  const legacy = loadSnippets();
  return cleanQuickCommands((legacy.length ? legacy : defaultSnippets).map((row) => ({ text: row.text, enter: row.enter })));
}

export function readScope(scope: QuickScope): QuickCommand[] {
  return scope.kind === "global" ? readGlobal() : cleanQuickCommands(getPref<unknown>(projectKey(scope.id), []));
}

export function writeScope(scope: QuickScope, commands: QuickCommand[]) {
  setPref(scopeKey(scope), cleanQuickCommands(commands) as unknown as JsonValue);
}

// What a terminal shows: its project's commands first, then everyone's.
export function commandsFor(projectId: number | null): { scope: QuickScope; command: QuickCommand }[] {
  const project = projectId ? readScope({ kind: "project", id: projectId }).map((command) => ({ scope: { kind: "project", id: projectId } as QuickScope, command })) : [];
  return [...project, ...readGlobal().map((command) => ({ scope: { kind: "global" } as QuickScope, command }))];
}

// What actually goes down the wire: Enter is a carriage return to a terminal.
export const commandBytes = (command: QuickCommand) => command.text + (command.enter ? "\r" : "");
