// Line deep links. A file is named by ?open=<workspace path> and a line or
// range by the fragment, as on code hosts: #L42, #L42C5 or #L10-L20.

export interface LineTarget {
  line: number;
  column?: number;
  endLine?: number;
}

export function parseLineHash(hash: string): LineTarget | undefined {
  const match = /^#?L(\d+)(?:C(\d+))?(?:-L(\d+))?$/i.exec(hash.trim());
  if (!match) return undefined;
  const line = Number(match[1]);
  if (!line) return undefined;
  return {
    line,
    column: match[2] ? Number(match[2]) : undefined,
    endLine: match[3] ? Number(match[3]) : undefined,
  };
}

export function lineHash(target: LineTarget): string {
  let hash = "#L" + target.line;
  if (target.column && target.column > 1) hash += "C" + target.column;
  if (target.endLine && target.endLine > target.line) hash += "-L" + target.endLine;
  return hash;
}

/** A shareable address of this terminal page opening path at a line. */
export function fileLink(location: { origin: string; pathname: string }, path: string, target?: LineTarget): string {
  return location.origin + location.pathname + "?open=" + encodeURIComponent(path) + (target ? lineHash(target) : "");
}

/** What the page was opened for, if a deep link. */
export function readDeepLink(search: string, hash: string): { path: string; target?: LineTarget } | undefined {
  const path = new URLSearchParams(search).get("open");
  if (!path) return undefined;
  return { path, target: parseLineHash(hash) };
}
