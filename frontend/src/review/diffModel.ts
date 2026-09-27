// The review workspace's diff model (docs/review.md): hunks, side-by-side
// rows, the changed-file tree, file fingerprints for "viewed", comment
// re-anchoring across agent edits, and line attribution lookups. Pure
// functions over parsePatchLines' output, so both views and the tests share
// one reading of a patch.

import { parsePatchLines, type DiffLine } from "./diffLines";

export type DiffMode = "unified" | "split";

export interface Hunk {
  /** Index among this file's hunks, as the server's hunk action counts them. */
  index: number;
  header: DiffLine;
  lines: DiffLine[];
}

export interface ParsedFile {
  meta: DiffLine[];
  hunks: Hunk[];
  binary: boolean;
}

export function parseFile(patch: string): ParsedFile {
  const meta: DiffLine[] = [];
  const hunks: Hunk[] = [];
  let binary = false;
  for (const line of parsePatchLines(patch)) {
    if (line.kind === "hunk") {
      hunks.push({ index: hunks.length, header: line, lines: [] });
      continue;
    }
    const current = hunks.at(-1);
    if (current && line.kind !== "meta") {
      // parsePatchLines turns the patch's trailing newline into an empty
      // context line; it is not part of the file.
      if (line.kind === "ctx" && line.text === "") continue;
      current.lines.push(line);
    } else if (current && line.text.startsWith("\\ No newline")) {
      current.lines.push(line);
    } else {
      if (/^Binary files |^GIT binary patch/.test(line.text)) binary = true;
      if (line.text !== "") meta.push(line);
    }
  }
  if (!hunks.length && /\nBinary files .* differ/.test("\n" + patch)) binary = true;
  return { meta, hunks, binary };
}

/** One row of the side-by-side view: the old line on the left, the new on the
 * right. A run of deletions followed by additions pairs up row by row. */
export interface SplitRow {
  left?: DiffLine;
  right?: DiffLine;
}

export function splitRows(lines: DiffLine[]): SplitRow[] {
  const rows: SplitRow[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i]!;
    if (line.kind === "ctx" || line.kind === "meta") {
      rows.push({ left: line, right: line });
      i++;
      continue;
    }
    const dels: DiffLine[] = [];
    const adds: DiffLine[] = [];
    while (i < lines.length && lines[i]!.kind === "del") dels.push(lines[i++]!);
    while (i < lines.length && lines[i]!.kind === "add") adds.push(lines[i++]!);
    if (!dels.length && !adds.length) {
      rows.push({ left: line, right: line });
      i++;
      continue;
    }
    for (let k = 0; k < Math.max(dels.length, adds.length); k++) {
      rows.push({ left: dels[k], right: adds[k] });
    }
  }
  return rows;
}

// ---- file tree ---------------------------------------------------------------

export interface TreeNode {
  name: string;
  /** Full path for a file; the directory prefix for a folder. */
  path: string;
  file: boolean;
  children: TreeNode[];
}

/** A directory tree of changed paths, with single-child folders collapsed
 * into one row ("src/review") the way code hosts show them. */
export function buildTree(paths: string[]): TreeNode[] {
  const root: TreeNode = { name: "", path: "", file: false, children: [] };
  for (const p of [...paths].sort((a, b) => a.localeCompare(b))) {
    const parts = p.split("/");
    let node = root;
    parts.forEach((part, i) => {
      const file = i === parts.length - 1;
      const path = parts.slice(0, i + 1).join("/");
      let child = node.children.find((c) => c.name === part && c.file === file);
      if (!child) {
        child = { name: part, path, file, children: [] };
        node.children.push(child);
      }
      node = child;
    });
  }
  const compact = (n: TreeNode): TreeNode => {
    let cur = n;
    while (!cur.file && cur.children.length === 1 && !cur.children[0]!.file) {
      const only = cur.children[0]!;
      cur = { ...only, name: `${cur.name}/${only.name}` };
    }
    const children = cur.children
      .map(compact)
      .sort((a, b) => Number(a.file) - Number(b.file) || a.name.localeCompare(b.name));
    return { ...cur, children };
  };
  return compact(root).children;
}

// ---- fingerprints --------------------------------------------------------------

/** A short content hash (FNV-1a) of a file's patch: "viewed" is recorded
 * against it, so any change to the file's diff clears the mark. */
export function fingerprint(text: string): string {
  let h = 0x811c9dc5;
  for (let i = 0; i < text.length; i++) {
    h ^= text.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return (h >>> 0).toString(16).padStart(8, "0") + text.length.toString(16);
}

const IMAGE = /\.(png|jpe?g|gif|webp|bmp|ico|avif|svg)$/i;
export function isImagePath(path: string): boolean {
  return IMAGE.test(path);
}

// ---- comment anchoring -----------------------------------------------------------

export interface Anchor {
  side: "old" | "new";
  line: number;
  code: string;
  context_before: string;
  context_after: string;
}

/** The lines of one side of a file's diff, in order, with their numbers. */
export function sideLines(file: ParsedFile, side: "old" | "new"): { n: number; text: string }[] {
  const out: { n: number; text: string }[] = [];
  for (const h of file.hunks) {
    for (const l of h.lines) {
      if (side === "new" && (l.kind === "add" || l.kind === "ctx") && l.newLine !== undefined)
        out.push({ n: l.newLine, text: l.text.slice(1) });
      if (side === "old" && (l.kind === "del" || l.kind === "ctx") && l.oldLine !== undefined)
        out.push({ n: l.oldLine, text: l.text.slice(1) });
    }
  }
  return out;
}

const CONTEXT = 2;
const norm = (s: string) => s.replace(/\s+$/, "");

/** What a new comment records about where it sits: its code and up to two
 * lines either side, so it can be found again after the file changes. */
export function anchorFor(file: ParsedFile, side: "old" | "new", line: number): Anchor | undefined {
  const lines = sideLines(file, side);
  const i = lines.findIndex((l) => l.n === line);
  if (i < 0) return undefined;
  // Context is only the lines truly adjacent in the file, never the tail of
  // an earlier hunk.
  const before: string[] = [];
  for (let k = 1; k <= CONTEXT && lines[i - k]?.n === line - k; k++) before.unshift(lines[i - k]!.text);
  const after: string[] = [];
  for (let k = 1; k <= CONTEXT && lines[i + k]?.n === line + k; k++) after.push(lines[i + k]!.text);
  return {
    side,
    line,
    code: lines[i]!.text,
    context_before: before.join("\n"),
    context_after: after.join("\n"),
  };
}

export interface Placement {
  line: number;
  /** The code line is unchanged and so is the context around it. */
  exact: boolean;
}

/** Finds a comment's line in the current diff by content: the same code, the
 * best-matching context, then the nearest to where it was. Undefined when
 * that code is no longer in the diff at all. */
export function placeComment(file: ParsedFile | undefined, a: Anchor): Placement | undefined {
  if (!file) return undefined;
  const lines = sideLines(file, a.side);
  const before = a.context_before ? a.context_before.split("\n").map(norm) : [];
  const after = a.context_after ? a.context_after.split("\n").map(norm) : [];
  let best: { line: number; score: number; exact: boolean } | undefined;
  lines.forEach((l, i) => {
    if (norm(l.text) !== norm(a.code)) return;
    let score = 0;
    let matched = 0;
    before.forEach((b, k) => {
      const at = lines[i - before.length + k];
      if (at && at.n === l.n - before.length + k && norm(at.text) === b) matched++;
    });
    after.forEach((b, k) => {
      const at = lines[i + 1 + k];
      if (at && at.n === l.n + 1 + k && norm(at.text) === b) matched++;
    });
    score = matched * 1000 - Math.min(999, Math.abs(l.n - a.line));
    const exact = matched === before.length + after.length;
    if (!best || score > best.score) best = { line: l.n, score, exact };
  });
  return best && { line: best.line, exact: best.exact };
}

/** Where a sent comment stands after the agent's edits: "open" when its code
 * and context are exactly as they were, "addressed" when the agent changed
 * them (or removed the line), "resolved" once the reviewer said so. */
export type CommentState = "draft" | "open" | "addressed" | "resolved";

export function commentState(
  status: string,
  placement: Placement | undefined,
): CommentState {
  if (status === "resolved") return "resolved";
  if (status === "draft") return "draft";
  return placement?.exact ? "open" : "addressed";
}

// ---- attribution ---------------------------------------------------------------------

export interface FileAttribution {
  agent?: [number, number][] | null;
  human?: [number, number][] | null;
}

const within = (ranges: [number, number][] | null | undefined, n: number) =>
  !!ranges?.some(([a, b]) => n >= a && n <= b);

/** Who wrote an added line, by its new-side number. */
export function authorOf(attr: FileAttribution | undefined, line: number): "agent" | "human" | undefined {
  if (!attr) return undefined;
  if (within(attr.agent, line)) return "agent";
  if (within(attr.human, line)) return "human";
  return undefined;
}

/** Counts of agent/human lines, for the file header and the legend. */
export function attributionCounts(attr: FileAttribution | undefined): { agent: number; human: number } {
  const count = (r: [number, number][] | null | undefined) =>
    (r ?? []).reduce((n, [a, b]) => n + b - a + 1, 0);
  return { agent: count(attr?.agent), human: count(attr?.human) };
}
