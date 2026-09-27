// Files in the palette and Go to file in the workspace. Typing in the command
// palette also searches the paths of the session whose pane is in front,
// through that workspace's file index (GET /api/term/session/<id>/index),
// ranked like the terminal page's Go to file (files/fuzzy.ts). `name:42`
// opens at line 42. Without a file pane type, or on a server without the
// index, this offers nothing.
import { createClient } from "../api/client";
import { parseQuery, rank } from "../files/fuzzy";
import { fileRef } from "../files/refs";
import { registerPaletteProvider } from "../shell/palette-providers";
import type { Edge, PaneRef } from "./layout";
import { paneType } from "./registry";

export interface FilesContext {
  session: number;
  name: string;
  // The pane in front, and whether the layout is the phone's single pane.
  focused: string;
  mobile: boolean;
  open: (ref: PaneRef, options?: { beside?: string; edge?: Edge }) => void;
}

let context: FilesContext | null = null;
const cache = new Map<number, { at: number; files: string[]; ignored: string[] }>();
const request = createClient();

// The workspace says which session is in front and how to open a pane.
export function setFilesContext(next: FilesContext | null) {
  context = next;
}
export function filesContext() {
  return context;
}

export async function sessionIndex(session: number, signal?: AbortSignal, fresh = false) {
  let entry = cache.get(session);
  if (fresh || !entry || Date.now() - entry.at > 60000) {
    const index = await request<{ files?: string[]; ignored?: string[] }>(`/term/session/${session}/index`, { signal });
    entry = { at: Date.now(), files: Array.isArray(index.files) ? index.files : [], ignored: Array.isArray(index.ignored) ? index.ignored : [] };
    cache.set(session, entry);
  }
  return entry;
}

// A file pane opens beside the focused pane, as a tab on phones.
export function openFilePane(current: FilesContext, path: string, line?: number, column?: number) {
  current.open(fileRef(current.session, path, line, column), current.focused ? { beside: current.focused, edge: current.mobile ? "center" : "right" } : undefined);
}

registerPaletteProvider({
  id: "workspace-files",
  async search(query, signal) {
    const current = context;
    if (!current?.session || !paneType("file")) return [];
    const parsed = parseQuery(query);
    if (!parsed.text) return [];
    const entry = await sessionIndex(current.session, signal);
    return rank(parsed.text, entry.files, entry.ignored, 20)
      .slice(0, 20)
      .map(({ path, ignored }) => ({
        id: `file-${current.session}-${path}`,
        title: path.slice(path.lastIndexOf("/") + 1) + (parsed.line ? ":" + parsed.line : ""),
        category: ignored ? "Ignored files" : "Files",
        detail: path,
        run: () => openFilePane(current, path, parsed.line, parsed.column),
      }));
  },
});
