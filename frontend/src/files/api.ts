// The workspace file API (internal/api/workspace_files.go). Every call runs on
// the attachment's own target; the server resolves the workspace root.
import { ApiError } from "../api/client";
import { request } from "../terminal/model";

export interface Entry {
  name: string;
  path: string;
  directory: boolean;
  size: number;
  mtime?: number;
  link?: boolean;
}

export interface Listing {
  path: string;
  entries: Entry[];
  limit?: number;
}

export interface Opened {
  blob: Blob;
  sha256: string;
  mtime: number;
}

export interface Saved {
  sha256: string;
  size: number;
  mtime: number;
  path: string;
}

export interface Index {
  files: string[];
  ignored: string[];
  truncated: boolean;
  source: string;
  elapsed_ms: number;
}

export interface Hit {
  path: string;
  line: number;
  column: number;
  end: number;
  text: string;
}

export interface SearchOptions {
  regex: boolean;
  caseSensitive: boolean;
  word: boolean;
  include: string;
  ignored: boolean;
}

export type GitKind = "modified" | "added" | "deleted" | "renamed" | "untracked" | "ignored" | "conflict";

/** A save refused because the file on disk is not the one the editor opened. */
export class ConflictError extends Error {
  constructor(
    message: string,
    readonly sha256: string | null,
  ) {
    super(message);
  }
}

const q = (path: string) => encodeURIComponent(path);

/** An absolute or ~/ path names a file outside the workspace, on the
 * session's machine: a person may view it read-only (external_files.go). */
export const isOutside = (path: string) => path.startsWith("/") || path.startsWith("~/");

export class FileApi {
  constructor(readonly base: string) {}

  async list(path: string, signal?: AbortSignal): Promise<Listing> {
    return (await request(`${this.base}/files?path=${q(path)}`, { signal })).json();
  }

  async open(path: string, signal?: AbortSignal): Promise<Opened> {
    const response = await request(`${this.base}/${isOutside(path) ? "external" : "file"}?path=${q(path)}`, { signal });
    return {
      blob: await response.blob(),
      sha256: response.headers.get("X-Lectern-Sha256") || "",
      mtime: Number(response.headers.get("X-Lectern-Mtime")) || 0,
    };
  }

  async stat(path: string, signal?: AbortSignal): Promise<{ exists: boolean; sha256?: string; mtime?: number; size?: number }> {
    return (await request(`${this.base}/stat?path=${q(path)}`, { signal })).json();
  }

  /** Whether a workspace path names something, without reading it. */
  async exists(path: string, signal?: AbortSignal): Promise<{ exists: boolean; directory?: boolean; path: string }> {
    return (await request(`${this.base}/exists?path=${q(path)}`, { signal })).json();
  }

  /** A file outside the workspace: whether it exists and is a regular file. */
  async outsideStat(path: string, signal?: AbortSignal): Promise<{ exists: boolean; regular?: boolean; path: string; size?: number }> {
    return (await request(`${this.base}/external-stat?path=${q(path)}`, { signal })).json();
  }

  /** base is the hash the editor started from, "absent" to create, "any" to overwrite. */
  async save(path: string, body: Blob | string, base: string): Promise<Saved> {
    try {
      const response = await request(`${this.base}/file?path=${q(path)}`, {
        method: "PUT",
        headers: { "X-Lectern-Base": base, "Content-Type": "application/octet-stream" },
        body,
      });
      return response.json();
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) {
        // request() keeps only the message; ask the disk for the new hash.
        const current = await this.stat(path).catch(() => undefined);
        throw new ConflictError(error.message, current?.exists ? current.sha256 || null : null);
      }
      throw error;
    }
  }

  async op(op: "mkdir" | "create" | "rename" | "delete", path: string, to?: string): Promise<{ path: string }> {
    return (
      await request(`${this.base}/fileops`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ op, path, to }),
      })
    ).json();
  }

  async archive(path: string): Promise<Blob> {
    return (await request(`${this.base}/archive?path=${q(path)}`)).blob();
  }

  async index(signal?: AbortSignal): Promise<Index> {
    return (await request(`${this.base}/index`, { signal })).json();
  }

  async gitStatus(signal?: AbortSignal): Promise<{ repository: boolean; status: Record<string, GitKind> }> {
    return (await request(`${this.base}/git-status`, { signal })).json();
  }

  /** Long-poll until a folder in dirs (or git's HEAD/index) changes. */
  async watch(dirs: string[], token: string, timeout: number, signal?: AbortSignal): Promise<{ token: string; changed: boolean; mode: "inotify" | "poll" }> {
    return (
      await request(`${this.base}/watch`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ dirs, token, timeout }),
        signal,
      })
    ).json();
  }

  async search(query: string, options: SearchOptions, signal?: AbortSignal): Promise<{ results: Hit[]; truncated: boolean; source: string }> {
    const params = new URLSearchParams({ q: query });
    if (options.regex) params.set("regex", "1");
    if (options.caseSensitive) params.set("case", "1");
    if (options.word) params.set("word", "1");
    if (options.ignored) params.set("ignored", "1");
    if (options.include.trim()) params.set("include", options.include.trim());
    return (await request(`${this.base}/search?${params}`, { signal })).json();
  }
}

/** The git status of a path: its own entry, or a status folder that holds it. */
export function statusOf(status: Record<string, GitKind>, path: string, directory: boolean): GitKind | undefined {
  const own = status[path] || (directory ? status[path + "/"] : undefined);
  if (own) return own;
  // An untracked or ignored folder is listed once; everything in it shares that.
  for (let at = path.lastIndexOf("/"); at > 0; at = path.lastIndexOf("/", at - 1)) {
    const parent = status[path.slice(0, at + 1)];
    if (parent === "untracked" || parent === "ignored") return parent;
  }
  if (directory) {
    // A folder holding changes is marked with the most important one.
    const prefix = path + "/";
    let found: GitKind | undefined;
    for (const [name, kind] of Object.entries(status)) {
      if (!name.startsWith(prefix) || kind === "ignored") continue;
      if (kind === "conflict") return kind;
      if (!found || found === "untracked") found = kind === "deleted" || kind === "renamed" || kind === "added" ? "modified" : kind;
    }
    return found;
  }
  return undefined;
}
