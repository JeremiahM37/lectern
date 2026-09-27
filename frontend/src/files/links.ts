// Finds what is clickable in one line of terminal output: web addresses and
// file references such as src/app.ts:12:5, ./main.go(40,2) or a Python
// traceback's File "x.py", line 9. File paths resolve against the workspace;
// an absolute path elsewhere on the machine is not offered, since only the
// workspace can be opened.

export interface TextLink {
  start: number; // index in the line, inclusive
  end: number; // exclusive
  text: string;
  url?: string;
  path?: string;
  line?: number;
  column?: number;
}

const URL_PATTERN = /\bhttps?:\/\/[^\s"'<>`)\]]+[^\s"'<>`)\].,;:!?]/g;
// A path segment: letters, digits and the usual punctuation of file names.
const SEGMENT = String.raw`[\w@+~-][\w@.+~-]*`;
const FILE_PATTERN = new RegExp(
  String.raw`(?:\.{1,2}\/|\/)?(?:${SEGMENT}\/)*${SEGMENT}` + // the path
    String.raw`(?:(?::(\d+))(?::(\d+))?|\((\d+)(?:,\s?(\d+))?\))?`, // :12:5 or (12,5)
  "g",
);
const PYTHON = /File "([^"]+)", line (\d+)/g;

function resolve(raw: string, workdir: string): string | undefined {
  let path = raw;
  if (path.startsWith(workdir + "/")) path = path.slice(workdir.length + 1);
  else if (path.startsWith("/")) return undefined;
  path = path.replace(/^\.\//, "");
  const parts: string[] = [];
  for (const part of path.split("/")) {
    if (part === "..") {
      if (!parts.length) return undefined;
      parts.pop();
    } else if (part && part !== ".") parts.push(part);
  }
  return parts.length ? parts.join("/") : undefined;
}

export function findLinks(text: string, workdir: string): TextLink[] {
  const links: TextLink[] = [];
  const taken = (start: number, end: number) => links.some((l) => start < l.end && end > l.start);
  for (const match of text.matchAll(URL_PATTERN)) {
    links.push({ start: match.index, end: match.index + match[0].length, text: match[0], url: match[0] });
  }
  for (const match of text.matchAll(PYTHON)) {
    const path = resolve(match[1]!, workdir);
    if (!path) continue;
    const start = match.index + match[0].indexOf('"') + 1;
    links.push({ start, end: start + match[1]!.length, text: match[1]!, path, line: Number(match[2]) });
  }
  for (const match of text.matchAll(FILE_PATTERN)) {
    const whole = match[0];
    const start = match.index,
      end = start + whole.length;
    if (taken(start, end)) continue;
    const line = match[1] ?? match[3],
      column = match[2] ?? match[4];
    const rawPath = whole.replace(/(?::\d+(?::\d+)?|\(\d+(?:,\s?\d+)?\))$/, "");
    const name = rawPath.slice(rawPath.lastIndexOf("/") + 1);
    // A bare word is not a file. Require an extension, a folder, or a line.
    const hasExtension = /\.[A-Za-z0-9]{1,12}$/.test(name) && !/^\d+(\.\d+)+$/.test(name);
    if (!hasExtension && !line && !rawPath.includes("/")) continue;
    if (!hasExtension && !line && !/^(\.{1,2}\/|\/)/.test(rawPath)) continue;
    // Version numbers and IP addresses look like names with extensions.
    if (/^v?\d+(\.\d+)+$/.test(rawPath)) continue;
    // "a/b" inside a URL-less sentence like "and/or" is not worth a link.
    if (!hasExtension && !line && rawPath.split("/").every((part) => /^[a-z]+$/.test(part)) && !rawPath.startsWith(".") && !rawPath.startsWith("/")) continue;
    const path = resolve(rawPath, workdir);
    if (!path) continue;
    links.push({
      start,
      end,
      text: whole,
      path,
      line: line ? Number(line) : undefined,
      column: column ? Number(column) : undefined,
    });
  }
  return links.sort((a, b) => a.start - b.start);
}

/** The link covering a character index, for a tap on the phone. */
export function linkAt(links: TextLink[], index: number): TextLink | undefined {
  return links.find((link) => index >= link.start && index < link.end);
}

