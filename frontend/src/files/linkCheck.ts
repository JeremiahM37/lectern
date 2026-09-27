// Whether a workspace path an agent printed names a file, asked once and
// remembered for this page: a bare name is only offered as a link if it
// does. A miss is forgotten quickly, since the agent may be about to write it.
import type { FileApi } from "./api";

const HIT_MS = 5 * 60_000,
  MISS_MS = 5_000;

export type ExistsCheck = (path: string) => boolean | Promise<boolean>;

export function existenceCheck(api: Pick<FileApi, "exists">, now: () => number = Date.now): ExistsCheck {
  const known = new Map<string, { exists: boolean; at: number }>();
  const pending = new Map<string, Promise<boolean>>();
  return (path) => {
    const hit = known.get(path);
    if (hit && now() - hit.at < (hit.exists ? HIT_MS : MISS_MS)) return hit.exists;
    const waiting = pending.get(path);
    if (waiting) return waiting;
    const asked = api
      .exists(path)
      .then(
        (answer) => answer.exists && !answer.directory,
        () => false,
      )
      .then((exists) => {
        known.set(path, { exists, at: now() });
        pending.delete(path);
        return exists;
      });
    pending.set(path, asked);
    return asked;
  };
}
