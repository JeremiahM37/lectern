// Builds a small unified-diff string from a tool call's before/after text,
// so an Edit/Write/MultiEdit card can hand it straight to
// frontend/src/review/diffLines.ts's parsePatchLines (the same parser the
// session review diff already uses) instead of a bespoke renderer. These
// tool calls already carry the exact before/after text, so there is no real
// diff algorithm to run — just a plain "everything old that changed is
// removed, everything new is added" hunk, with unchanged leading/trailing
// lines trimmed to a couple of lines of context.

const CONTEXT = 2;

function linesOf(text: string): string[] {
  return text.length ? text.split("\n") : [];
}

function commonPrefix(a: string[], b: string[]): number {
  let n = 0;
  while (n < a.length && n < b.length && a[n] === b[n]) n++;
  return n;
}
function commonSuffix(a: string[], b: string[], from: number): number {
  let n = 0;
  while (
    n < a.length - from &&
    n < b.length - from &&
    a[a.length - 1 - n] === b[b.length - 1 - n]
  )
    n++;
  return n;
}

/** A replace-in-place edit (Claude/Gemini's Edit, one MultiEdit entry). */
export function buildReplacePatch(path: string, oldText: string, newText: string): string {
  const a = linesOf(oldText);
  const b = linesOf(newText);
  const prefix = commonPrefix(a, b);
  const suffix = commonSuffix(a, b, prefix);
  const oldEnd = a.length - suffix;
  const newEnd = b.length - suffix;
  const ctxBeforeStart = Math.max(0, prefix - CONTEXT);
  const ctxBefore = a.slice(ctxBeforeStart, prefix);
  const ctxAfter = a.slice(oldEnd, Math.min(a.length, oldEnd + CONTEXT));
  const removed = a.slice(prefix, oldEnd);
  const added = b.slice(prefix, newEnd);
  const oldStart = ctxBeforeStart + 1;
  const newStart = ctxBeforeStart + 1;
  const oldCount = ctxBefore.length + removed.length + ctxAfter.length;
  const newCount = ctxBefore.length + added.length + ctxAfter.length;
  const lines = [
    `--- a/${path}`,
    `+++ b/${path}`,
    `@@ -${oldStart},${oldCount || 1} +${newStart},${newCount || 1} @@`,
    ...ctxBefore.map((l) => " " + l),
    ...removed.map((l) => "-" + l),
    ...added.map((l) => "+" + l),
    ...ctxAfter.map((l) => " " + l),
  ];
  return lines.join("\n");
}

/** A brand-new file (Write with no prior content to diff against). */
export function buildNewFilePatch(path: string, content: string): string {
  const lines = linesOf(content);
  return [
    `--- /dev/null`,
    `+++ b/${path}`,
    `@@ -0,0 +1,${lines.length || 1} @@`,
    ...lines.map((l) => "+" + l),
  ].join("\n");
}

/** MultiEdit applies several independent replacements to one file. Each is
 * rendered as its own hunk against the same header — the line numbers are
 * relative to each edit, not the real file (we were never given the whole
 * file), which parsePatchLines handles fine since each `@@` resets its own
 * counters. */
export function buildMultiEditPatch(
  path: string,
  edits: { old_string: string; new_string: string }[],
): string {
  const header = [`--- a/${path}`, `+++ b/${path}`];
  const hunks = edits.map((edit) => buildReplacePatch(path, edit.old_string, edit.new_string).split("\n").slice(2));
  return [...header, ...hunks.flat()].join("\n");
}

// ---- Codex apply_patch (the V4A envelope) -------------------------------

export interface CodexPatchFile {
  path: string;
  action: "add" | "update" | "delete";
  patch: string;
}

const FILE_MARKER = /^\*\*\* (Add|Update|Delete) File: (.+)$/;

/** Splits Codex's apply_patch envelope (*** Begin Patch / *** Update File:
 * path / @@ ... / +/-/context lines / *** End Patch) into one pseudo-unified
 * diff per file, reusing parsePatchLines for the body since the envelope
 * already prefixes changed lines with +/-. Best-effort: an envelope this
 * cannot recognize is returned as a single unparsed file so the caller can
 * fall back to showing the raw text rather than nothing. */
export function parseCodexPatch(raw: string): CodexPatchFile[] {
  const lines = raw.split("\n");
  const files: CodexPatchFile[] = [];
  let current: CodexPatchFile | null = null;
  let body: string[] = [];
  const flush = () => {
    if (!current) return;
    const marker = current.action === "add" ? "--- /dev/null" : `--- a/${current.path}`;
    const to = current.action === "delete" ? "--- /dev/null" : `+++ b/${current.path}`;
    current.patch = [marker, to, ...body].join("\n");
    files.push(current);
  };
  for (const line of lines) {
    if (line.startsWith("*** Begin Patch") || line.startsWith("*** End Patch")) continue;
    const match = FILE_MARKER.exec(line.trim());
    if (match) {
      flush();
      const action = (match[1] ?? "Update").toLowerCase() as CodexPatchFile["action"];
      current = { path: match[2] ?? "changes", action, patch: "" };
      body = [];
      continue;
    }
    if (current) body.push(line);
  }
  flush();
  if (files.length === 0 && raw.trim()) {
    return [{ path: "changes", action: "update", patch: raw }];
  }
  return files;
}
