// Parsing and resolving merge-conflict markers for the three-way conflict
// view (docs/review.md). A file is a list of plain text runs and conflict
// regions; each region carries ours, theirs and (with diff3-style markers)
// the common ancestor. resolveConflicts rebuilds the file from one choice
// per region.

export interface ConflictRegion {
  kind: "conflict";
  ours: string[];
  base?: string[];
  theirs: string[];
  oursLabel: string;
  theirsLabel: string;
}

export interface TextRun {
  kind: "text";
  lines: string[];
}

export type Segment = TextRun | ConflictRegion;

export type Choice = "ours" | "theirs" | "both" | "both-theirs-first" | "base" | { custom: string };

export function parseConflicts(text: string): Segment[] {
  const lines = text.split("\n");
  const out: Segment[] = [];
  let run: string[] = [];
  let region: ConflictRegion | undefined;
  let part: "ours" | "base" | "theirs" = "ours";
  const flush = () => {
    if (run.length) out.push({ kind: "text", lines: run });
    run = [];
  };
  for (const line of lines) {
    if (!region && line.startsWith("<<<<<<<")) {
      flush();
      region = {
        kind: "conflict",
        ours: [],
        theirs: [],
        oursLabel: line.slice(7).trim(),
        theirsLabel: "",
      };
      part = "ours";
      continue;
    }
    if (region) {
      if (line.startsWith("|||||||") && part === "ours") {
        region.base = [];
        part = "base";
        continue;
      }
      if (line.startsWith("=======") && part !== "theirs") {
        part = "theirs";
        continue;
      }
      if (line.startsWith(">>>>>>>") && part === "theirs") {
        region.theirsLabel = line.slice(7).trim();
        out.push(region);
        region = undefined;
        continue;
      }
      region[part]!.push(line);
      continue;
    }
    run.push(line);
  }
  if (region) {
    // An unterminated region is not a conflict at all: keep the text as is.
    run.push("<<<<<<< " + region.oursLabel, ...region.ours);
    if (region.base) run.push("|||||||", ...region.base);
    run.push("=======", ...region.theirs);
  }
  flush();
  return out;
}

export function conflictCount(segments: Segment[]): number {
  return segments.filter((s) => s.kind === "conflict").length;
}

function chosen(region: ConflictRegion, choice: Choice | undefined): string[] {
  if (choice === undefined) {
    // Unresolved: keep the markers so the result still shows it.
    return [
      "<<<<<<< " + region.oursLabel,
      ...region.ours,
      ...(region.base ? ["||||||| base", ...region.base] : []),
      "=======",
      ...region.theirs,
      ">>>>>>> " + region.theirsLabel,
    ];
  }
  if (typeof choice === "object") return choice.custom === "" ? [] : choice.custom.split("\n");
  switch (choice) {
    case "ours":
      return region.ours;
    case "theirs":
      return region.theirs;
    case "base":
      return region.base ?? [];
    case "both":
      return [...region.ours, ...region.theirs];
    case "both-theirs-first":
      return [...region.theirs, ...region.ours];
  }
}

/** Rebuilds the file from one choice per conflict region (by index). */
export function resolveConflicts(segments: Segment[], choices: (Choice | undefined)[]): string {
  const out: string[] = [];
  let c = 0;
  for (const s of segments) {
    if (s.kind === "text") out.push(...s.lines);
    else out.push(...chosen(s, choices[c++]));
  }
  return out.join("\n");
}

export function hasMarkers(text: string): boolean {
  return /^(<{7}|>{7})( |$)/m.test(text);
}
