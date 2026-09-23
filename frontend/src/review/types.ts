// Shared types for review-and-merge: a session's or task's live diff, and the
// inline comments an operator drafts against it before sending them to the
// agent (SendText for a session) or back as request-changes feedback (a
// task). See docs/agent-events.md section 4.

export interface FileStat {
  path: string;
  additions?: number;
  deletions?: number;
}

export interface FilePatch {
  path: string;
  patch: string;
}

export interface RepoDiff {
  name?: string;
  project_id?: number;
  base_ref: string;
  stats: FileStat[];
  files: FilePatch[];
  truncated: boolean;
}

export interface DiffResponse {
  repos: RepoDiff[];
}

/** A comment drafted against one diff line, held client-side until sent. */
export interface DraftComment {
  /** Local-only identity for list rendering/removal; never sent to the API. */
  key: string;
  file: string;
  line: number;
  side: "old" | "new";
  text: string;
  code?: string;
}

let draftKeySeq = 0;
/** A fresh, process-lifetime-unique key for a new draft comment's React list
 * identity — never sent to the API. */
export function nextDraftKey(): string {
  draftKeySeq += 1;
  return `c${draftKeySeq}`;
}

export function toWireComments(
  comments: DraftComment[],
): { file: string; line: number; side: string; text: string; code?: string }[] {
  return comments.map(({ file, line, side, text, code }) => ({
    file,
    line,
    side,
    text,
    ...(code ? { code } : {}),
  }));
}
