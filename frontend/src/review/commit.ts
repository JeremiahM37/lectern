// The commit request the Commit tab sends, and the small rules around it,
// kept pure so they are testable without a browser.

export interface GitIdentity {
  name: string;
  email: string;
  /** "global": every repository on this machine; "repo": only this one. */
  scope: "global" | "repo";
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
