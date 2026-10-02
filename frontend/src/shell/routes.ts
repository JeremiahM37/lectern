// The app's pages and the hashes that name them (docs/design/simple-ui.md
// "Information architecture"). Pure, so the navigation rules — what is in the
// main bar, what is under More, how old links are rewritten — are testable
// without a browser.

export const VIEWS = ["sessions", "approvals", "tasks", "terminals", "overview", "issues", "media", "evals", "settings"] as const;
export type View = (typeof VIEWS)[number];

export const isView = (value: string): value is View => (VIEWS as readonly string[]).includes(value);

/**
 * The main navigation, the same on every screen size: Sessions, Approvals and
 * Settings, with everything else under More (re-audit N9, round 3 B3). Tasks —
 * the board with Orchestrate and Race — joins the main items once there is a
 * task, i.e. once someone uses it. On a desktop sidebar Terminals joins too
 * while a terminal tab is open; a narrow bar keeps it under More, with a count.
 */
export function primaryViews(hasTasks: boolean, desktop = false, terminalsOpen = false): View[] {
  const views: View[] = ["sessions", "approvals"];
  if (hasTasks) views.push("tasks");
  if (desktop && terminalsOpen) views.push("terminals");
  views.push("settings");
  return views;
}

/**
 * What More holds: every page not in the main items. A narrow screen also
 * lists two Settings sections people look for by name; a desktop sidebar does
 * not, because there they would sit beside Settings and duplicate its tabs.
 */
export type MoreEntry = { view: View } | { section: "machines" | "plugins" };
export function moreEntries(hasTasks: boolean, desktop = false, terminalsOpen = false): MoreEntry[] {
  const primary = primaryViews(hasTasks, desktop, terminalsOpen);
  const views = VIEWS.filter((view) => !primary.includes(view));
  const entries: MoreEntry[] = views.map((view) => ({ view }));
  return desktop ? entries : [...entries, { section: "machines" }, { section: "plugins" }];
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
