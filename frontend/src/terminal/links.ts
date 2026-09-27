// What a click or tap on terminal output points at: a web address, or a file
// (with an optional :line:col, as compilers and test runners print them).
// Pure string work, so it is tested without a terminal. The native client
// has a Go port (internal/filelinks); both run the same test vectors
// (internal/filelinks/testdata/vectors.json).
//
// A path is often split across screen rows. The terminal soft-wraps a long
// line at the right edge (the row after is marked wrapped), and agent TUIs
// hard-wrap their own output with indentation or a gutter:
//
//   • Cerebras résumé (PDF) (/home/admin/.formwork/
//     application-testing-20260927/
//     Jeremiah_Mackey_Cerebras.pdf) — emphasizes GPU
//
// Rows are joined when the next one is a soft wrap, or when a row ends inside
// a path (a fragment ending in "/", or running to the full width) and the next
// row, after its indentation or gutter, starts with a path segment. Nothing
// else is joined, so prose never runs into the next line.

export interface Span {
  row: number;
  start: number; // character index in the row, inclusive
  end: number; // exclusive
}

export type TerminalLink =
  | { kind: "url"; url: string; text: string; spans?: Span[] }
  | {
      kind: "file";
      // Relative to the workspace, unless external: then an absolute or ~/ path
      // on the session's machine, which only a person may open, read-only.
      path: string;
      external?: boolean;
      // A relative path is only a link if that workspace file exists; the
      // caller checks before offering or opening it.
      verify?: boolean;
      line?: number;
      column?: number;
      text: string;
      spans?: Span[];
    };

export interface Row {
  text: string;
  // This row continues the one before it (a terminal soft wrap).
  wrapped?: boolean;
}

const URL_RE = /\bhttps?:\/\/[^\s<>"'`]+/gu;
// The characters of a file name, in any script. These patterns are written
// the same way in the Go port.
const NAME = String.raw`\p{L}\p{N}_@.+~\-`;
const SEGMENT = `[${NAME}]+`;
// :12, :12:5 or (12,5), as compilers and test runners print a position.
const POSITION = String.raw`(?::\d+(?::\d+)?|\(\d+(?:,\s?\d+)?\))`;
// A name with an extension, a path with a folder, or a name with a line
// (Makefile:12). Relative ones are only links if the file exists.
const PATH_RE = new RegExp(
  String.raw`(?:/|\./|~/)?${SEGMENT}(?:/${SEGMENT})*\.[A-Za-z0-9]{1,12}${POSITION}?` +
    String.raw`|(?:/|\./|~/)${SEGMENT}(?:/${SEGMENT})+${POSITION}?` +
    String.raw`|[${NAME}]*\p{L}[${NAME}]*(?:/${SEGMENT})*:\d+(?::\d+)?`,
  "gu",
);
// A Python traceback names the line after the path: File "x.py", line 9.
const PYTHON_LINE = /^", line (\d+)/;
// Box borders, tree gutters and the markers agent TUIs draw beside text.
const LEFT_GUTTER = /^[\s│┃║▏▕╎┆┊⎿└├╰]*/u;
const RIGHT_GUTTER = /[\s│┃║▕]+$/u;
// The path fragment a row ends with; it must hold a "/".
const TAIL = new RegExp(String.raw`(?:^|[\s(\[{<'"` + "`" + String.raw`])((?:~|\.{1,2})?/?[${NAME}]*/[${NAME}/]*)$`, "u");
// The start of a following row that continues a path.
const CONTINUATION = new RegExp(String.raw`^\.?[\p{L}\p{N}_@+~][${NAME}]*`, "u");
const MAX_ROWS = 12;

const count = (text: string, ch: string) => text.split(ch).length - 1;

// A sentence's closing punctuation is not part of the address it ends with,
// and neither is a bracket the address did not open.
function trimTail(text: string): string {
  let out = text;
  for (let changed = true; changed; ) {
    changed = false;
    const stripped = out.replace(/[.,;:!?'"]+$/, "");
    if (stripped !== out) (out = stripped), (changed = true);
    for (const [open, close] of [["(", ")"], ["[", "]"], ["{", "}"]] as const)
      if (out.endsWith(close) && count(out, close) > count(out, open)) (out = out.slice(0, -1)), (changed = true);
  }
  return out;
}

/** How row a runs into row b, if it does: where a's text ends and b's begins. */
export function joinRows(a: Row, b: Row, width = 0): { aEnd: number; bStart: number } | undefined {
  if (b.wrapped) return { aEnd: a.text.length, bStart: 0 };
  const trimmed = a.text.replace(RIGHT_GUTTER, "");
  const tail = TAIL.exec(trimmed)?.[1];
  if (!tail || /^\/+$/.test(tail)) return undefined;
  const folder = /[\p{L}\p{N}_.~-]\/$/u.test(tail);
  const cut = width > 0 && a.text.length >= width && !/\s$/.test(a.text) && !tail.endsWith("/");
  if (!folder && !cut) return undefined;
  const bStart = LEFT_GUTTER.exec(b.text)![0].length;
  if (!CONTINUATION.test(b.text.slice(bStart))) return undefined;
  return { aEnd: trimmed.length, bStart };
}

interface Piece {
  row: number;
  from: number;
  to: number;
  offset: number; // where this piece starts in the joined text
}

/** The rows joined around one row, as text plus where each piece came from. */
export function chainAround(rows: Row[], row: number, width = 0): { text: string; pieces: Piece[] } {
  let start = row,
    end = row;
  while (start > 0 && row - start < MAX_ROWS && joinRows(rows[start - 1]!, rows[start]!, width)) start--;
  while (end < rows.length - 1 && end - row < MAX_ROWS && joinRows(rows[end]!, rows[end + 1]!, width)) end++;
  const pieces: Piece[] = [];
  let text = "";
  for (let i = start; i <= end; i++) {
    const from = i === start ? 0 : joinRows(rows[i - 1]!, rows[i]!, width)!.bStart;
    const to = i === end ? rows[i]!.text.length : joinRows(rows[i]!, rows[i + 1]!, width)!.aEnd;
    pieces.push({ row: i, from, to: Math.max(from, to), offset: text.length });
    text += rows[i]!.text.slice(from, Math.max(from, to));
  }
  return { text, pieces };
}

function spansOf(pieces: Piece[], start: number, end: number): Span[] {
  const spans: Span[] = [];
  for (const piece of pieces) {
    const lo = Math.max(start, piece.offset),
      hi = Math.min(end, piece.offset + piece.to - piece.from);
    if (lo < hi) spans.push({ row: piece.row, start: piece.from + lo - piece.offset, end: piece.from + hi - piece.offset });
  }
  return spans;
}

function normalize(parts: string[]): string[] | undefined {
  const out: string[] = [];
  for (const part of parts) {
    if (part === "..") {
      if (!out.length) return undefined;
      out.pop();
    } else if (part && part !== ".") out.push(part);
  }
  return out;
}

/** What a path token names, relative to the workspace where it can be. */
export function classify(raw: string, workdir: string): Omit<Extract<TerminalLink, { kind: "file" }>, "text" | "kind" | "spans"> | undefined {
  const position = /(?::(\d+)(?::(\d+))?|\((\d+)(?:,\s?(\d+))?\))$/.exec(raw);
  const token = position ? raw.slice(0, position.index) : raw;
  const line = position?.[1] ?? position?.[3],
    column = position?.[2] ?? position?.[4];
  const where = { line: line ? Number(line) : undefined, column: column ? Number(column) : undefined };
  // Version numbers and IP addresses look like names with extensions.
  if (/^v?\d+(\.\d+)+$/.test(token)) return undefined;
  const root = workdir.replace(/\/+$/, "");
  if (token.startsWith("~/")) return { path: token, external: true, ...where };
  if (token.startsWith("/")) {
    const parts = normalize(token.split("/"));
    if (!parts?.length) return undefined;
    const path = "/" + parts.join("/");
    if (root && path.startsWith(root + "/")) return { path: path.slice(root.length + 1), ...where };
    return { path, external: true, ...where };
  }
  const relative = normalize(token.split("/"));
  if (relative?.length) return { path: relative.join("/"), verify: true, ...where };
  // A relative path that climbs out of the workspace is a path beside it.
  if (!root) return undefined;
  const absolute = normalize([...root.split("/"), ...token.split("/")]);
  return absolute?.length ? { path: "/" + absolute.join("/"), external: true, ...where } : undefined;
}

/** Every link with a part on this row, each with its spans on all rows. */
export function linksOnRow(rows: Row[], row: number, workdir: string, width = 0): TerminalLink[] {
  if (!rows[row]) return [];
  const { text, pieces } = chainAround(rows, row, width);
  const found: TerminalLink[] = [];
  const taken: [number, number][] = [];
  for (const match of text.matchAll(URL_RE)) {
    const value = trimTail(match[0]);
    taken.push([match.index, match.index + match[0].length]);
    found.push({ kind: "url", url: value, text: value, spans: spansOf(pieces, match.index, match.index + value.length) });
  }
  for (const match of text.matchAll(PATH_RE)) {
    const start = match.index,
      raw = trimTail(match[0]);
    // A path cut short with an ellipsis (a status line, a truncated title) names nothing.
    if (text[start + match[0].length] === "…") continue;
    if (taken.some(([lo, hi]) => start < hi && start + raw.length > lo)) continue;
    const target = classify(raw, workdir);
    const python = PYTHON_LINE.exec(text.slice(start + match[0].length));
    if (target && python && target.line === undefined) target.line = Number(python[1]);
    if (target) found.push({ kind: "file", ...target, text: raw, spans: spansOf(pieces, start, start + raw.length) });
  }
  return found.filter((link) => link.spans!.some((span) => span.row === row));
}

/** The link under a character of a row, if any. */
export function linkAtRows(rows: Row[], row: number, col: number, workdir: string, width = 0): TerminalLink | undefined {
  return linksOnRow(rows, row, workdir, width).find((link) => link.spans!.some((span) => span.row === row && col >= span.start && col < span.end));
}

/** The link under character index col of one line, if any. */
export function linkAt(line: string, col: number, workdir: string): TerminalLink | undefined {
  return linkAtRows([{ text: line }], 0, col, workdir);
}

/** An OSC 8 hyperlink's target, as the program that printed it named it:
 * http(s) opens as a link; a file:// URL, or a bare absolute or ~/ path (as
 * agent TUIs print for Markdown links to files), as a file, read-only outside
 * the workspace. Percent-encoding is decoded. */
export function hyperlinkTarget(uri: string, workdir: string): TerminalLink | undefined {
  if (uri.startsWith("/") || uri.startsWith("~/")) {
    let path = uri;
    try {
      path = decodeURIComponent(uri);
    } catch {
      // Not percent-encoded after all; use it as printed.
    }
    const target = classify(path, workdir);
    return target ? { kind: "file", ...target, text: uri } : undefined;
  }
  let parsed: URL;
  try {
    parsed = new URL(uri);
  } catch {
    return undefined;
  }
  if (parsed.protocol === "http:" || parsed.protocol === "https:") return { kind: "url", url: parsed.href, text: uri };
  if (parsed.protocol !== "file:") return undefined;
  let path: string;
  try {
    path = decodeURIComponent(parsed.pathname);
  } catch {
    return undefined;
  }
  const target = classify(path, workdir);
  return target ? { kind: "file", ...target, text: uri } : undefined;
}
