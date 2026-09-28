// The app's pages and the hashes that name them (docs/design/simple-ui.md
// "Information architecture"). Pure, so the navigation rules — what is in the
// main bar, what is under More, how old links are rewritten — are testable
// without a browser.

export const VIEWS = ["sessions", "approvals", "tasks", "terminals", "overview", "issues", "media", "evals", "settings"] as const;
export type View = (typeof VIEWS)[number];

export const isView = (value: string): value is View => (VIEWS as readonly string[]).includes(value);

/** Three primary pages plus More: opening a terminal never grows the bar. */
export function primaryViews(_terminalsOpen: boolean): View[] {
  return ["sessions", "approvals", "settings"];
}

/** What More holds: every other page, then two Settings sections people look for by name. */
export type MoreEntry = { view: View } | { section: "machines" | "plugins" };
export function moreEntries(_terminalsOpen: boolean): MoreEntry[] {
  const views: View[] = ["tasks", "terminals", "overview", "issues", "media", "evals"];
  return [...views.map((view) => ({ view })), { section: "machines" }, { section: "plugins" }];
}

/** Home, on every device and after every reload that names no page. */
export const HOME: View = "sessions";

/**
 * Rewrites a hash from before the rename to the one that means the same page
 * now: #board → #tasks, #tasks/<project>/… (issues and pull requests) →
 * #issues/<project>/…, #deck → #overview, #targets → #settings/machines. Anything else
 * comes back as it was. A bare #tasks is the task list now, as its name says.
 */
export function canonicalHash(hash: string): string {
  const raw = hash.replace(/^#/, "");
  const [head = "", ...rest] = raw.split("/");
  const tail = rest.length ? "/" + rest.join("/") : "";
  switch (head) {
    case "board":
      return "#tasks";
    case "deck":
      return "#overview";
    case "targets":
      // Its page opened on Machines, so that is where an old link lands.
      return "#settings" + (tail || "/machines");
    case "tasks":
      return /^[1-9]\d*$/.test(rest[0] || "") ? "#issues" + tail : "#" + raw;
  }
  return "#" + raw;
}
