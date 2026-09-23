// Turns one unified-diff patch string into displayable, clickable lines with
// real old/new line numbers, by walking hunk headers the way a terminal pager
// would. Shared by the session review panel and the task diff view so a click
// target is computed identically in both.

export type DiffLineKind = "meta" | "hunk" | "add" | "del" | "ctx";

export interface DiffLine {
  text: string;
  kind: DiffLineKind;
  oldLine?: number;
  newLine?: number;
}

const hunkRe = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

export function parsePatchLines(patch: string): DiffLine[] {
  const out: DiffLine[] = [];
  let oldLine = 0,
    newLine = 0;
  for (const raw of patch.split("\n")) {
    if (
      raw.startsWith("diff --git") ||
      raw.startsWith("index ") ||
      raw.startsWith("--- ") ||
      raw.startsWith("+++ ") ||
      raw.startsWith("\\ No newline")
    ) {
      out.push({ text: raw, kind: "meta" });
      continue;
    }
    const hunk = hunkRe.exec(raw);
    if (hunk) {
      oldLine = parseInt(hunk[1] ?? "0", 10);
      newLine = parseInt(hunk[3] ?? "0", 10);
      out.push({ text: raw, kind: "hunk" });
      continue;
    }
    if (raw.startsWith("+")) {
      out.push({ text: raw, kind: "add", newLine });
      newLine++;
    } else if (raw.startsWith("-")) {
      out.push({ text: raw, kind: "del", oldLine });
      oldLine++;
    } else {
      // a context line (leading space), or the trailing empty split() entry
      out.push({ text: raw, kind: "ctx", oldLine, newLine });
      oldLine++;
      newLine++;
    }
  }
  return out;
}

/** What a click on this line means for a review comment: which line number,
 * which side of the diff, and the source text (without the +/-/space marker). */
export function commentTarget(
  line: DiffLine,
): { line: number; side: "old" | "new"; code: string } | undefined {
  if (line.kind === "meta" || line.kind === "hunk") return undefined;
  const code = line.text.slice(1);
  if (line.kind === "del") return { line: line.oldLine!, side: "old", code };
  return { line: line.newLine!, side: "new", code };
}
