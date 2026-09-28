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
  /** Up to two lines either side, so the comment can be found again after
   * the file changes (diffModel.placeComment). */
  context_before?: string;
  context_after?: string;
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

// ---- review workspace (docs/review.md) ------------------------------------

export interface GitFile {
  path: string;
  status: string;
  previous_path?: string | null;
  conflicted: boolean;
  untracked: boolean;
  new_file: boolean;
  staged: boolean;
  unstaged: boolean;
  staged_patch: string;
  unstaged_patch: string;
  staged_hunks: string[];
  unstaged_hunks: string[];
}

export interface GitStatus {
  branch: string;
  head: string;
  upstream: string;
  remote_sha: string;
  ahead: number;
  behind: number;
  head_pushed: boolean;
  head_message: string;
  operation: string;
  merge_message: string;
  hooks: string[];
  files: GitFile[];
  truncated: boolean;
  base: string;
  on_base_branch: boolean;
  session_live: boolean;
  // Whether the repository has any remote; Push starts unticked without one.
  has_remote?: boolean;
}

export interface CommitStep {
  step: string;
  rc: number;
  output?: string;
  url?: string;
}

export interface CommitResult {
  steps: CommitStep[];
  failed?: string;
  detail?: string;
  hook_failure?: { hooks: string[] | null; output: string };
  // The branch the commit landed on (a new one, for "Commit on a new branch").
  branch?: string;
}

export interface StoredComment {
  id: number;
  repo: string;
  file: string;
  side: "old" | "new";
  line: number;
  code: string;
  context_before: string;
  context_after: string;
  text: string;
  status: "draft" | "sent" | "resolved";
  round: number;
  author: string;
  created_at: number;
  sent_at: number | null;
  resolved_at: number | null;
}

export interface ViewedMark {
  repo: string;
  path: string;
  fingerprint: string;
}
