// What a tap on terminal output points at: a web address, or a file in the
// workspace (with an optional :line:col, as compilers and test runners print
// them). Pure string work, so it is tested without a terminal.

export type TerminalLink =
  | { kind: "url"; url: string; text: string }
  | { kind: "file"; path: string; line?: number; column?: number; text: string };

const URL_RE = /\bhttps?:\/\/[^\s<>"'`]+/g;
const PATH_RE = /(?:\/|\.\/|~\/)?[\w@.+~-]+(?:\/[\w@.+~-]+)*\.[A-Za-z0-9]{1,12}(?::\d+(?::\d+)?)?|(?:\/|\.\/)[\w@.+~-]+(?:\/[\w@.+~-]+)+/g;

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

function relative(path: string, workdir: string): string | undefined {
  const root = workdir.replace(/\/+$/, "");
  if (path.startsWith("~/")) return undefined;
  if (path.startsWith("/")) {
    if (!root || !path.startsWith(root + "/")) return undefined;
    path = path.slice(root.length + 1);
  }
  path = path.replace(/^\.\//, "");
  return path && !path.split("/").includes("..") ? path : undefined;
}

/** The link under character index col of one terminal line, if any.
 * Absolute paths inside workdir become relative; paths outside it are not
 * offered, since the workspace file viewer cannot open them. */
export function linkAt(line: string, col: number, workdir: string): TerminalLink | undefined {
  for (const match of line.matchAll(URL_RE)) {
    const text = trimTail(match[0]);
    if (col >= match.index && col < match.index + text.length) return { kind: "url", url: text, text };
    if (col >= match.index && col < match.index + match[0].length) return undefined;
  }
  for (const match of line.matchAll(PATH_RE)) {
    const text = trimTail(match[0]);
    if (col < match.index || col >= match.index + text.length) continue;
    const position = /:(\d+)(?::(\d+))?$/.exec(text);
    const path = relative(position ? text.slice(0, position.index) : text, workdir);
    if (!path) return undefined;
    return {
      kind: "file",
      path,
      line: position ? Number(position[1]) : undefined,
      column: position?.[2] ? Number(position[2]) : undefined,
      text,
    };
  }
  return undefined;
}

/** An OSC 8 hyperlink's target, as the program that printed it named it:
 * http(s) opens as a link, a file:// URL inside the workspace as a file. */
export function hyperlinkTarget(uri: string, workdir: string): TerminalLink | undefined {
  let parsed: URL;
  try {
    parsed = new URL(uri);
  } catch {
    return undefined;
  }
  if (parsed.protocol === "http:" || parsed.protocol === "https:") return { kind: "url", url: parsed.href, text: uri };
  if (parsed.protocol !== "file:") return undefined;
  const path = relative(decodeURIComponent(parsed.pathname), workdir);
  return path ? { kind: "file", path, text: uri } : undefined;
}
