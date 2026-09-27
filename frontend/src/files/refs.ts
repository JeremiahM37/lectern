// Workspace pane references for files: a file, a session's explorer, and its
// project search. Ids are stable per session and path, so reopening focuses.
import { t } from "../i18n";
import type { PaneRef } from "../workspace/layout";

const name = (path: string) => path.slice(path.lastIndexOf("/") + 1);

export const fileRef = (session: number, path: string, line?: number, column?: number): PaneRef => ({
  id: `file:session:${session}:${path}`,
  kind: "file",
  title: name(path),
  // "at" makes asking again for a line a change, even for the same line.
  params: { session: String(session), path, ...(line ? { line: String(line), column: String(column || 1), at: String(Date.now()) } : {}) },
});
export const filesRef = (session: number, title: string): PaneRef => ({ id: `files:session:${session}`, kind: "files", title: t("files.paneFilesTitle", { name: title }), params: { session: String(session) } });
export const searchRef = (session: number, title: string): PaneRef => ({ id: `search:session:${session}`, kind: "search", title: t("files.paneSearchTitle", { name: title }), params: { session: String(session) } });
