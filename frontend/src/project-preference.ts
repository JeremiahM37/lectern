// Per-device memory of which projects a person actually works in.
//
// Two things live under one key: the project they chose last (which may be a
// deliberate "no project"), and a short list of the projects they successfully
// opened. localStorage is the store because the preference is about this
// device's habits, not the server's state: it never syncs, never leaves the
// browser and never needs a request. Every read and write is defensive — a
// corrupt value, a disabled store or a project that has since been deleted
// must leave the UI on its normal default rather than crash or pick a
// surprising project.
export const PROJECT_PREFERENCE_KEY = "lec-project-preference-v1";
export const RECENT_PROJECT_LIMIT = 8;

export interface ProjectPreference {
  /** The project chosen last. `null` is a real choice — blank/no project —
   * while `undefined` means nothing usable was stored (first visit, or a
   * corrupt value) and the caller should keep its own default. */
  last: number | null | undefined;
  /** Projects that were actually opened, most recent first, bounded. */
  recent: number[];
}

const EMPTY: ProjectPreference = { last: undefined, recent: [] };

function localStore(): Storage | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage;
  } catch {
    // Some privacy modes throw on the property itself rather than on use.
    return null;
  }
}

function projectID(value: unknown): number | null {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0
    ? value
    : null;
}

function normalizeRecent(value: unknown): number[] {
  if (!Array.isArray(value)) return [];
  const ids: number[] = [];
  for (const row of value) {
    const id = projectID(row);
    if (id === null || ids.includes(id)) continue;
    ids.push(id);
    if (ids.length >= RECENT_PROJECT_LIMIT) break;
  }
  return ids;
}

export function readProjectPreference(
  store: Storage | null = localStore(),
): ProjectPreference {
  if (!store) return { ...EMPTY };
  let raw: string | null;
  try {
    raw = store.getItem(PROJECT_PREFERENCE_KEY);
  } catch {
    return { ...EMPTY };
  }
  if (!raw) return { ...EMPTY };
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return { ...EMPTY };
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
    return { ...EMPTY };
  const record = parsed as Record<string, unknown>;
  let last: number | null | undefined;
  if ("last_project_id" in record) {
    // Only an explicit null is a blank choice; anything unreadable is treated
    // as if it had never been stored so the caller keeps its own default.
    last =
      record.last_project_id === null
        ? null
        : (projectID(record.last_project_id) ?? undefined);
  }
  return { last, recent: normalizeRecent(record.recent_project_ids) };
}

function write(store: Storage | null, preference: ProjectPreference): void {
  if (!store) return;
  const payload: Record<string, unknown> = {
    recent_project_ids: preference.recent,
  };
  if (preference.last !== undefined) payload.last_project_id = preference.last;
  try {
    store.setItem(PROJECT_PREFERENCE_KEY, JSON.stringify(payload));
  } catch {
    // A full or read-only store costs the memory, never the launch.
  }
}

/** Record an explicit choice from the new-session form, including "blank". */
export function rememberProjectSelection(
  projectId: number | null,
  store: Storage | null = localStore(),
): ProjectPreference {
  const next: ProjectPreference = {
    ...readProjectPreference(store),
    last: projectId,
  };
  write(store, next);
  return next;
}

/** Record a project that was successfully opened — never a failed creation. */
export function rememberRecentProject(
  projectId: number,
  store: Storage | null = localStore(),
): ProjectPreference {
  const current = readProjectPreference(store);
  const id = projectID(projectId);
  if (id === null) return current;
  const recent = [
    id,
    ...current.recent.filter((row) => row !== id),
  ].slice(0, RECENT_PROJECT_LIMIT);
  const next: ProjectPreference = { last: id, recent };
  write(store, next);
  return next;
}

/** Split a project list into the remembered ones and the rest, dropping any
 * ids whose project is gone. Both halves keep the order they were handed. */
export function orderProjectsByRecency<T extends { id: number }>(
  projects: T[],
  recent: number[],
): { recent: T[]; rest: T[] } {
  const byID = new Map(projects.map((row) => [row.id, row]));
  const seen = new Set<number>();
  const remembered: T[] = [];
  for (const id of recent) {
    const row = byID.get(id);
    if (!row || seen.has(id)) continue;
    seen.add(id);
    remembered.push(row);
  }
  return { recent: remembered, rest: projects.filter((row) => !seen.has(row.id)) };
}
