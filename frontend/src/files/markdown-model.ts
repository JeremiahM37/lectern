// Pure Markdown helpers for the preview: front matter, the table of contents
// and heading anchors. Rendering itself lives in viewers.tsx.

export interface FrontMatter {
  raw: string;
  fields: [string, string][];
  body: string;
  /** Lines the front matter occupied, so preview lines map back to source. */
  lines: number;
}

export function splitFrontMatter(text: string): FrontMatter {
  const match = /^(?:﻿)?---\r?\n([\s\S]*?)\r?\n(?:---|\.\.\.)\r?\n?/.exec(text);
  if (!match) return { raw: "", fields: [], body: text, lines: 0 };
  const raw = match[1]!;
  const fields: [string, string][] = [];
  let current: [string, string] | undefined;
  for (const line of raw.split(/\r?\n/)) {
    const pair = /^([A-Za-z0-9_][\w .-]*):\s*(.*)$/.exec(line);
    if (pair) {
      current = [pair[1]!.trim(), pair[2]!.trim()];
      fields.push(current);
    } else if (current && /^\s+/.test(line)) {
      // Nested YAML is shown as written, beneath its key.
      current[1] += (current[1] ? "\n" : "") + line.trim();
    }
  }
  return { raw, fields, body: text.slice(match[0].length), lines: match[0].split("\n").length - 1 };
}

export interface Heading {
  level: number;
  text: string;
  slug: string;
  line: number; // 1-based line in the body
}

export function slugify(text: string, used: Map<string, number>): string {
  const base =
    text
      .toLowerCase()
      .replace(/<[^>]+>/g, "")
      .replace(/[`*_~[\]()]/g, "")
      .trim()
      .replace(/[^\p{L}\p{N}\s-]/gu, "")
      .replace(/\s+/g, "-") || "section";
  const count = used.get(base) || 0;
  used.set(base, count + 1);
  return count ? `${base}-${count}` : base;
}

/** ATX and setext headings outside fenced code, in document order. */
export function headings(body: string): Heading[] {
  const out: Heading[] = [];
  const used = new Map<string, number>();
  const lines = body.split(/\r?\n/);
  let fence: string | undefined;
  lines.forEach((line, index) => {
    const opener = /^\s{0,3}(```+|~~~+)/.exec(line);
    if (opener) {
      if (!fence) fence = opener[1]![0]!;
      else if (opener[1]![0] === fence) fence = undefined;
      return;
    }
    if (fence) return;
    const atx = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
    if (atx) {
      out.push({ level: atx[1]!.length, text: atx[2]!, slug: slugify(atx[2]!, used), line: index + 1 });
      return;
    }
    const next = lines[index + 1];
    if (line.trim() && next && /^\s{0,3}(=+|-+)\s*$/.test(next) && !/^\s{0,3}([-*+]|\d+\.)\s/.test(line)) {
      const level = next.trim()[0] === "=" ? 1 : 2;
      out.push({ level, text: line.trim(), slug: slugify(line.trim(), used), line: index + 1 });
    }
  });
  return out;
}

/** Inline [[wiki links]] as ordinary links to a workspace file name. */
export function wikiLinks(body: string): string {
  return body.replace(/\[\[([^\]|#\n]+)(?:#([^\]|\n]+))?(?:\|([^\]\n]+))?\]\]/g, (_all, target: string, anchor: string | undefined, label: string | undefined) => {
    const href = "lectern-wiki:" + encodeURIComponent(target.trim()) + (anchor ? "#" + encodeURIComponent(anchor.trim()) : "");
    return `[${(label || target).trim()}](${href})`;
  });
}
