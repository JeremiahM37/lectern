import type { IDecoration, IDisposable, Terminal } from "@xterm/xterm";
import { linksOnRow, type Row, type TerminalLink } from "../terminal/links";

// Terminal links on xterm's buffer: the rows around one row as text (with
// soft wraps marked, so a path the terminal wrapped is one link), and each
// link's cells on every row it covers. Detection itself is terminal/links.ts.

interface BufferRow extends Row {
  cellOfIndex: number[];
}

// One buffer row as text, with where each character's cell is. A wide
// character fills two cells but is one character of text.
function rowText(term: Terminal, row: number): BufferRow {
  const line = term.buffer.active.getLine(row);
  let text = "";
  const cellOfIndex: number[] = [];
  if (line)
    for (let x = 0; x < line.length; x++) {
      const cell = line.getCell(x);
      if (cell && cell.getWidth() === 0) continue;
      const chars = cell?.getChars() || " ";
      for (let i = 0; i < chars.length; i++) cellOfIndex[text.length + i] = x;
      text += chars;
    }
  cellOfIndex[text.length] = line?.length ?? text.length;
  return { text: text.replace(/\s+$/, ""), wrapped: !!line?.isWrapped, cellOfIndex };
}

/** A link and its cells: [row, first cell, last cell + 1] in buffer rows. */
export interface PlacedLink {
  link: TerminalLink;
  cells: { row: number; start: number; end: number }[];
}

const RADIUS = 12;

/** Every link with a part on buffer row y (0-based). */
export function linksAtRow(term: Terminal, y: number, workdir: string): PlacedLink[] {
  const buffer = term.buffer.active;
  const first = Math.max(0, y - RADIUS),
    last = Math.min(buffer.length - 1, y + RADIUS);
  const rows: BufferRow[] = [];
  for (let row = first; row <= last; row++) rows.push(rowText(term, row));
  return linksOnRow(rows, y - first, workdir, term.cols).map((link) => ({
    link,
    cells: (link.spans || []).map((span) => {
      const row = rows[span.row]!;
      return { row: first + span.row, start: row.cellOfIndex[span.start] ?? span.start, end: (row.cellOfIndex[span.end - 1] ?? span.end - 1) + 1 };
    }),
  }));
}

/** The link under a cell, if any. */
export function linkAtCell(term: Terminal, col: number, y: number, workdir: string): PlacedLink | undefined {
  return linksAtRow(term, y, workdir).find((placed) => placed.cells.some((cell) => cell.row === y && col >= cell.start && col < cell.end));
}

export interface LinkOptions {
  workdir: () => string;
  // Whether a workspace path names a file (cached by the caller); a bare
  // name is only a link if it does.
  exists: (path: string) => boolean | Promise<boolean>;
  activate: (link: TerminalLink, event: MouseEvent) => void;
}

/**
 * Paths and web addresses in terminal output, underlined on hover — on every
 * row of a wrapped one — and opened on click. Taps and OSC 8 hyperlinks are
 * the engine's, which reports them to the same handler.
 */
export function installTerminalLinks(term: Terminal, options: LinkOptions): IDisposable {
  let underlines: IDecoration[] = [];
  const clear = () => {
    underlines.forEach((decoration) => decoration.dispose());
    underlines = [];
  };
  // The other rows of a wrapped link: xterm underlines only the row hovered.
  const underline = (placed: PlacedLink, hovered: number) => {
    clear();
    const buffer = term.buffer.active;
    const cursor = buffer.baseY + buffer.cursorY;
    for (const cell of placed.cells) {
      if (cell.row === hovered) continue;
      const marker = term.registerMarker(cell.row - cursor);
      if (!marker) continue;
      const decoration = term.registerDecoration({ marker, x: cell.start, width: cell.end - cell.start, layer: "top" });
      if (!decoration) {
        marker.dispose();
        continue;
      }
      decoration.onDispose(() => marker.dispose());
      decoration.onRender((element) => element.classList.add("term-link-underline"));
      underlines.push(decoration);
    }
  };
  const provider = term.registerLinkProvider({
    provideLinks(y, callback) {
      const row = y - 1;
      const placed = linksAtRow(term, row, options.workdir()).filter((item) => item.link.kind === "url" || options.workdir());
      const checks = placed.map((item) => (item.link.kind === "file" && item.link.verify ? options.exists(item.link.path) : true));
      const answer = (found: boolean[]) =>
        callback(
          placed
            .filter((_, i) => found[i])
            .flatMap((item) =>
              item.cells
                .filter((cell) => cell.row === row)
                .map((cell) => ({
                  text: item.link.text,
                  range: { start: { x: cell.start + 1, y }, end: { x: cell.end, y } },
                  decorations: { underline: true, pointerCursor: true },
                  hover: () => underline(item, row),
                  leave: clear,
                  activate: (event: MouseEvent) => options.activate(item.link, event),
                })),
            ),
        );
      if (checks.every((check) => typeof check === "boolean")) answer(checks as boolean[]);
      else void Promise.all(checks.map((check) => Promise.resolve(check).catch(() => false))).then(answer);
    },
  });
  return {
    dispose() {
      clear();
      provider.dispose();
    },
  };
}
