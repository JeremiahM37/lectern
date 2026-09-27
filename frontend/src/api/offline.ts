// The last answer to each of the few requests the home screens are built
// from (sessions, tasks, projects, machines, approvals), kept on this device
// so a phone opening the app on a train shows what it last knew at once,
// marked stale, instead of an empty screen and a spinner.
//
// While the page has not yet heard from Lectern, a request that fails to
// reach it, or takes more than a moment, is answered from the cache while the
// real one keeps going; when it lands, "revalidated" tells the app to
// refresh, so the screen catches up by itself. Once a live answer has
// arrived, requests go to the network as usual and a failure only raises
// the banner. It is per origin like the rest of the
// app's storage, and a 401 or a forgotten pairing clears it.
//
// This works the same in a browser, an installed web app and the Android
// app's WebView, which has no service worker.
import { ApiError } from "./client";

const KEY = "lec-offline-v1";
// A phone's localStorage is usually 5 MB for the whole origin; stay well
// inside it and leave room for everything else the app stores.
const MAX_BYTES = 1_500_000;

/** GET paths worth keeping: the lists every home screen starts from. */
export function cacheable(path: string, method = "GET"): boolean {
  if (method.toUpperCase() !== "GET") return false;
  const [base = "", query = ""] = path.split("?", 2);
  if (base === "/approvals") return !query || query === "status=pending";
  return ["/sessions", "/tasks", "/projects", "/targets", "/live", "/sessions/relaunched"].includes(base);
}

/** A failure that says the network or relay was unreachable, not that
 * Lectern refused: only these fall back to what was cached. */
export function unreachable(error: unknown): boolean {
  if (error instanceof ApiError) return error.status === 502 || error.status === 503 || error.status === 504;
  return error instanceof TypeError || (error instanceof Error && /network|failed to fetch|load failed|relay/i.test(error.message));
}

interface Entry {
  at: number;
  body: unknown;
}

export type OfflineState = { stale: false } | { stale: true; since: number };

export class OfflineCache extends EventTarget {
  private entries: Record<string, Entry>;
  private live = false;
  private staleSince = 0;

  constructor(
    private storage: Pick<Storage, "getItem" | "setItem" | "removeItem"> | undefined = safeStorage(),
    private now: () => number = () => Date.now(),
    /** How long a first request may take before the cache answers it. */
    private patience = 1200,
  ) {
    super();
    this.entries = this.read();
  }

  get state(): OfflineState {
    return this.staleSince ? { stale: true, since: this.staleSince } : { stale: false };
  }

  cacheable(path: string, method?: string): boolean {
    return cacheable(path, method);
  }

  /** Whether anything was ever cached here (a first visit has nothing). */
  get empty(): boolean {
    return Object.keys(this.entries).length === 0;
  }

  async run<T>(path: string, load: () => Promise<T>): Promise<T> {
    const hit = this.entries[path];
    const request = load().then(
      (value) => {
        this.store(path, value);
        this.fresh();
        return value;
      },
    );
    if (!hit) return request;
    if (!this.live) {
      // Nothing heard yet in this page: give the network a moment, and if it
      // has not answered by then, answer from the cache and let the real
      // request finish in the background. A reachable Lectern wins the race,
      // so a working connection never shows stale data first.
      return new Promise<T>((resolve, reject) => {
        let settled = false;
        const timer = setTimeout(() => {
          settled = true;
          resolve(hit.body as T);
        }, this.patience);
        request.then(
          (value) => {
            clearTimeout(timer);
            if (!settled) resolve(value);
            else this.dispatchEvent(new Event("revalidated"));
          },
          (error) => {
            clearTimeout(timer);
            if (!unreachable(error)) {
              if (!settled) reject(error);
              return;
            }
            this.markStale(hit.at);
            if (!settled) resolve(hit.body as T);
          },
        );
      });
    }
    // Once the page has live data, a failed request fails as before: each
    // screen keeps what it shows and says so its own way (Needs you marks
    // its rows stale); the banner says the whole app is offline.
    try {
      return await request;
    } catch (error) {
      if (unreachable(error)) this.markStale(hit.at);
      throw error;
    }
  }

  clear() {
    this.entries = {};
    try {
      this.storage?.removeItem(KEY);
    } catch {
      /* storage blocked */
    }
  }

  private fresh() {
    this.live = true;
    if (!this.staleSince) return;
    this.staleSince = 0;
    this.dispatchEvent(new Event("change"));
  }

  private markStale(at: number) {
    const since = this.staleSince ? Math.min(this.staleSince, at) : at;
    if (since === this.staleSince) return;
    this.staleSince = since;
    this.dispatchEvent(new Event("change"));
  }

  private read(): Record<string, Entry> {
    try {
      const raw = this.storage?.getItem(KEY);
      const value: unknown = raw ? JSON.parse(raw) : {};
      return value && typeof value === "object" && !Array.isArray(value) ? (value as Record<string, Entry>) : {};
    } catch {
      return {};
    }
  }

  private store(path: string, body: unknown) {
    this.entries[path] = { at: this.now(), body };
    let raw = JSON.stringify(this.entries);
    // Too big: keep the newest entries that fit, dropping the oldest first.
    if (raw.length > MAX_BYTES) {
      const keep: Record<string, Entry> = {};
      for (const [key, entry] of Object.entries(this.entries).sort((a, b) => b[1].at - a[1].at)) {
        const next = JSON.stringify({ ...keep, [key]: entry });
        if (next.length <= MAX_BYTES) keep[key] = entry;
      }
      this.entries = keep;
      raw = JSON.stringify(keep);
    }
    try {
      this.storage?.setItem(KEY, raw);
    } catch {
      /* quota or blocked storage: the in-memory copy still serves this page */
    }
  }
}

function safeStorage(): Storage | undefined {
  try {
    return typeof localStorage === "undefined" ? undefined : localStorage;
  } catch {
    return undefined;
  }
}

/** "just now", "4 min ago", "2 h ago", "3 days ago". */
export function staleAge(since: number, now = Date.now()): string {
  const s = Math.max(0, Math.round((now - since) / 1000));
  if (s < 45) return "just now";
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 36) return `${h} h ago`;
  return `${Math.round(h / 24)} days ago`;
}

/** The one cache the whole app shares. */
export const offlineCache = new OfflineCache();
