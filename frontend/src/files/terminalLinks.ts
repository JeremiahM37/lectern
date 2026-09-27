import type { IDisposable, Terminal } from "@xterm/xterm";
import { findLinks, hyperlinkTarget, linkAt, type TextLink } from "./links";

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
 * File references and web addresses in terminal output. A mouse click uses
 * xterm's link provider (addresses are already handled by the web-links
 * add-on); a tap on a phone is resolved here, because touch never reaches
 * xterm's hover-based links.
 */
export function installTerminalLinks(term: Terminal, host: HTMLElement, workdir: string, activate: (link: TextLink) => void): IDisposable {
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
  // OSC 8 hyperlinks: xterm hands their address here instead of asking to
  // navigate to it.
  term.options.linkHandler = {
    allowNonHttpProtocols: true,
    activate: (_event, uri) => {
      const link = hyperlinkTarget(uri, workdir);
      if (link) activate(link);
    },
  };
  let down: { x: number; y: number; at: number; id: number } | undefined;
  let consumed = false;
  const pointerDown = (event: PointerEvent) => {
    down = event.pointerType === "touch" && event.isPrimary ? { x: event.clientX, y: event.clientY, at: performance.now(), id: event.pointerId } : undefined;
    consumed = false;
  };
  const pointerUp = (event: PointerEvent) => {
    const start = down;
    down = undefined;
    if (!start || start.id !== event.pointerId) return;
    if (Math.hypot(event.clientX - start.x, event.clientY - start.y) > 10 || performance.now() - start.at > 450) return;
    const screen = term.element?.querySelector(".xterm-screen")?.getBoundingClientRect();
    if (!screen || !term.cols || !term.rows) return;
    const column = Math.floor(((event.clientX - screen.left) / screen.width) * term.cols);
    const line = Math.floor(((event.clientY - screen.top) / screen.height) * term.rows);
    if (column < 0 || line < 0 || column >= term.cols || line >= term.rows) return;
    const row = rowText(term, term.buffer.active.viewportY + line);
    const link = linkAt(findLinks(row.text, workdir), row.indexOfCell[column] ?? -1);
    if (!link) return;
    consumed = true;
    event.preventDefault();
    event.stopPropagation();
    activate(link);
  };
  // The tap that opened a link must not also focus the terminal and raise
  // the phone's keyboard over what it opened.
  const touchEnd = (event: TouchEvent) => {
    if (!consumed) return;
    consumed = false;
    event.preventDefault();
    term.blur();
  };
  host.addEventListener("pointerdown", pointerDown, true);
  host.addEventListener("pointerup", pointerUp, true);
  host.addEventListener("touchend", touchEnd, { capture: true, passive: false });
  return {
    dispose() {
      provider.dispose();
      term.options.linkHandler = null;
      host.removeEventListener("pointerdown", pointerDown, true);
      host.removeEventListener("pointerup", pointerUp, true);
      host.removeEventListener("touchend", touchEnd, true);
    },
  };
}
