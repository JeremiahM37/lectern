// Files in the palette. When a "file" pane type is registered (the file
// viewer/editor registers one), typing in the command palette also searches
// the paths of the session whose pane is in front, through that workspace's
// file index (GET /api/term/session/<id>/index). Without a file pane type,
// or on a server without the index, this offers nothing.
import { createClient } from "../api/client";
import { registerPaletteProvider } from "../shell/palette-providers";
import type { PaneRef } from "./layout";
import { paneType } from "./registry";

let context: { session: number; open: (ref: PaneRef) => void } | null = null;
const cache = new Map<number, { at: number; files: string[] }>();
const request = createClient();

// The workspace says which session is in front and how to open a pane.
export function setFilesContext(next: typeof context) {
  context = next;
}

function score(path: string, query: string): number {
  const lower = path.toLowerCase(), q = query.toLowerCase();
  const base = lower.slice(lower.lastIndexOf("/") + 1);
  if (base.startsWith(q)) return 3;
  if (lower.includes(q)) return 2;
  let at = 0;
  for (const ch of q) {
    at = lower.indexOf(ch, at);
    if (at < 0) return 0;
    at++;
  }
  return 1;
}

registerPaletteProvider({
  id: "workspace-files",
  async search(query, signal) {
    const current = context;
    if (!current?.session || !paneType("file")) return [];
    let entry = cache.get(current.session);
    if (!entry || Date.now() - entry.at > 60000) {
      const index = await request<{ files?: string[] }>(`/term/session/${current.session}/index`, { signal });
      entry = { at: Date.now(), files: Array.isArray(index.files) ? index.files : [] };
      cache.set(current.session, entry);
    }
    return entry.files
      .map((path) => ({ path, score: score(path, query.trim()) }))
      .filter((row) => row.score > 0)
      .sort((a, b) => b.score - a.score || a.path.length - b.path.length)
      .slice(0, 20)
      .map(({ path }) => ({
        id: `file-${current.session}-${path}`,
        title: path.slice(path.lastIndexOf("/") + 1),
        category: "Files",
        detail: path,
        run: () => current.open({ id: `file:session:${current.session}:${path}`, kind: "file", title: path.slice(path.lastIndexOf("/") + 1), params: { session: String(current.session), path } }),
      }));
  },
});
