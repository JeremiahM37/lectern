// What enabled plugins add to the browser: quick commands, themes and palette
// commands (docs/plugins.md). The server decides what is enabled and
// consented; this module only keeps a local copy, so a plugin theme paints
// before the first request answers and the terminal frames — separate
// documents — see a change through the storage event, like prefs/store.ts.
import { useSyncExternalStore } from "react";
import { createClient } from "../api/client";

export interface PluginQuickCommand {
  id: string;
  plugin: string;
  label: string;
  text: string;
  enter: boolean;
  // Empty: every project.
  project_ids: number[];
}
export interface PluginTheme {
  id: string;
  plugin: string;
  name: string;
  accent?: string;
  dark?: Record<string, string>;
  light?: Record<string, string>;
}
export interface PluginPaletteCommand {
  id: string;
  plugin: string;
  title: string;
  href: string;
}
export interface PluginContributions {
  quick_commands: PluginQuickCommand[];
  themes: PluginTheme[];
  palette_commands: PluginPaletteCommand[];
}

const CACHE = "lec-plugin-ui-v1";
const EMPTY: PluginContributions = { quick_commands: [], themes: [], palette_commands: [] };
let current: PluginContributions = read();
const listeners = new Set<() => void>();
let request = createClient();

function clean(value: unknown): PluginContributions {
  const row = value && typeof value === "object" ? (value as Partial<PluginContributions>) : {};
  return {
    quick_commands: Array.isArray(row.quick_commands) ? row.quick_commands : [],
    themes: Array.isArray(row.themes) ? row.themes : [],
    palette_commands: Array.isArray(row.palette_commands) ? row.palette_commands : [],
  };
}
function read(): PluginContributions {
  try {
    return clean(JSON.parse(localStorage.getItem(CACHE) || "{}"));
  } catch {
    return EMPTY;
  }
}
function emit() {
  for (const listener of listeners) listener();
}

try {
  window.addEventListener("storage", (event) => {
    if (event.key !== CACHE) return;
    current = read();
    emit();
  });
} catch {}

// For tests: a store with no browser.
export function setPluginContributionsForTest(value: PluginContributions, client?: ReturnType<typeof createClient>) {
  current = clean(value);
  if (client) request = client;
  emit();
}

export function loadPluginContributions(): Promise<void> {
  return request<PluginContributions>("/plugins/contributions").then((body) => {
    current = clean(body);
    try {
      localStorage.setItem(CACHE, JSON.stringify(current));
    } catch {}
    emit();
  });
}

export function subscribePlugins(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function pluginContributions(): PluginContributions {
  return current;
}

export function usePluginContributions(): PluginContributions {
  return useSyncExternalStore(subscribePlugins, pluginContributions, pluginContributions);
}

// The quick commands a terminal of this project shows from plugins.
export function pluginQuickCommands(projectId: number | null): PluginQuickCommand[] {
  return current.quick_commands.filter((row) => !row.project_ids?.length || (projectId !== null && row.project_ids.includes(projectId)));
}

export function pluginTheme(id: string): PluginTheme | undefined {
  return id ? current.themes.find((row) => row.id === id) : undefined;
}

// A palette command opens a Lectern view (#…) or an https page; nothing else
// is ever followed, whatever the server sent.
export function safeHref(href: string): string {
  if (/^#[\w./-]*$/.test(href)) return href;
  try {
    const url = new URL(href);
    return url.protocol === "https:" ? url.href : "";
  } catch {
    return "";
  }
}
