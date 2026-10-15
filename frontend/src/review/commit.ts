// The commit request the Commit tab sends, and the small rules around it,
// kept pure so they are testable without a browser.

export interface GitIdentity {
  name: string;
  email: string;
  /** "global": every repository on this machine; "repo": only this one. */
  scope: "global" | "repo";
}

/** The state the person reviewed (the last diff load); commit and push are refused if it moved. */
export interface ReviewedState {
  head?: string;
  diffHash?: string;
  /** The diff response these pins came from: a new object on every load, so
   * the panel can tell a reload has landed even when the pins are unchanged.
   * Never sent to the server. */
  loaded?: object;
}

/** Only the pins that were actually loaded: an absent one is never checked. */
export function reviewedPins(r?: ReviewedState): { expected_head?: string; expected_diff_hash?: string } {
  return {
    ...(r?.head ? { expected_head: r.head } : {}),
    ...(r?.diffHash ? { expected_diff_hash: r.diffHash } : {}),
  };
}

/** The detail of a 409 {error: "stale"} refusal, or undefined for any other error body. */
export function staleDetail(payload: unknown): string | undefined {
  if (!payload || typeof payload !== "object") return undefined;
  const p = payload as { error?: unknown; detail?: unknown };
  if (p.error !== "stale") return undefined;
  return typeof p.detail === "string" && p.detail ? p.detail : "the changes moved";
}

export interface CommitChoices {
  repo: string;
  message: string;
  stagedCount: number;
  amend: boolean;
  allowPushedAmend: boolean;
  push: boolean;
  hasRemote: boolean | undefined;
  onBaseBranch: boolean;
  onMain: "branch" | "main";
  newBranch: string;
  pr: boolean;
  prTitle: string;
  prBody: string;
  /** Sent only after git said it has no name and email (409 no_git_identity). */
  identity?: GitIdentity;
  /** The reviewed state, so a commit of changes that moved since is refused. */
  reviewed?: ReviewedState;
}

export function commitRequestBody(c: CommitChoices) {
  return {
    repo: c.repo,
    message: c.message,
    stage_all: c.stagedCount === 0,
    amend: c.amend,
    allow_pushed_amend: c.allowPushedAmend,
    push: c.push && c.hasRemote !== false && !(c.amend && c.allowPushedAmend),
    ...(c.onBaseBranch
      ? c.onMain === "branch"
        ? { new_branch: c.newBranch.trim() }
        : { allow_base_branch: true }
      : {}),
    pr: c.pr && c.push,
    pr_title: c.prTitle,
    pr_body: c.prBody,
    ...(c.identity
      ? { identity: { name: c.identity.name.trim(), email: c.identity.email.trim(), scope: c.identity.scope } }
      : {}),
    ...reviewedPins(c.reviewed),
  };
}

/** What is still missing from the name-and-email form, or "" when it is complete. */
export function identityProblem(name: string, email: string): "" | "name" | "email" {
  if (!name.trim()) return "name";
  if (!/^[^\s@]+@[^\s@]+$/.test(email.trim())) return "email";
  return "";
}

/** Quotes a word for a POSIX shell only when it needs it. */
export function shellWord(word: string): string {
  return /^[A-Za-z0-9_./~:@%+=-]+$/.test(word) ? word : "'" + word.replace(/'/g, "'\\''") + "'";
}

/** The one git command that puts a folder back on its base branch. */
export function switchBackCommand(dir: string, base: string): string {
  return `git -C ${shellWord(dir || ".")} switch ${shellWord(base)}`;
}
