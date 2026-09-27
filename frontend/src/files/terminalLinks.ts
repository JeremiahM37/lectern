import type { IDisposable, Terminal } from "@xterm/xterm";
import { findLinks, type TextLink } from "./links";

// One buffer row as text, with the mapping between string indexes and cells.
// A wide character fills two cells but is one character of text.
function rowText(term: Terminal, row: number) {
  const line = term.buffer.active.getLine(row);
  let text = "";
  const indexOfCell: number[] = [];
  const cellOfIndex: number[] = [];
  if (line)
    for (let x = 0; x < line.length; x++) {
      const cell = line.getCell(x);
      indexOfCell[x] = text.length;
      if (cell && cell.getWidth() === 0) continue;
      const chars = cell?.getChars() || " ";
      for (let i = 0; i < chars.length; i++) cellOfIndex[text.length + i] = x;
      text += chars;
    }
  cellOfIndex[text.length] = line?.length ?? text.length;
  return { text: text.replace(/\s+$/, ""), indexOfCell, cellOfIndex };
}

/**
 * File references in terminal output, underlined on hover and opened at
 * their line and column on click. Taps, web addresses and OSC 8 hyperlinks
 * are the engine's (terminal/links.ts), which reports them to the same
 * handler.
 */
export function installTerminalLinks(term: Terminal, workdir: string, activate: (link: TextLink) => void): IDisposable {
  const provider = term.registerLinkProvider({
    provideLinks(y, callback) {
      const row = rowText(term, y - 1);
      callback(
        findLinks(row.text, workdir)
          .filter((link) => link.path)
          .map((link) => ({
            text: link.text,
            range: { start: { x: row.cellOfIndex[link.start]! + 1, y }, end: { x: row.cellOfIndex[link.end - 1]! + 1, y } },
            activate: () => activate(link),
          })),
      );
    },
  });
  return {
    dispose() {
      provider.dispose();
    },
  };
}
