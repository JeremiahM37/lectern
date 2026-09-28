// Naming the branch "Commit on a new branch" creates (docs/design/simple-ui.md).
/** A branch name from the session's name: lectern/fix-the-login-page. */
export function branchFor(name: string): string {
  const slug = name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
  return "lectern/" + (slug || "changes");
}
