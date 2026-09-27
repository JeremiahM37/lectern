// Per-person UI preferences that follow the person to every device: theme,
// shortcuts, saved layouts, quick commands. The server keeps them
// (internal/api/ui_prefs.go); this module keeps a local copy so the app paints
// with the right theme before the first request answers, and so the terminal
// frames — separate documents — see a change made in the app at once through
// the storage event.
//
// A change is written locally first and marked unsent until the server
// confirms it. An unsent change survives a reload, and wins over the server's
// older copy until it has been delivered.
import { useRef, useSyncExternalStore } from "react";
import { ApiError, createClient, type JsonValue } from "../api/client";

const CACHE = "lec-ui-prefs-v1";
const UNSENT = "lec-ui-prefs-unsent-v1";
const WRITE_DELAY = 250;

type Values = Record<string, JsonValue>;
let values: Values = readJSON(CACHE);
let unsent: Record<string, true> = readJSON(UNSENT) as Record<string, true>;
let loaded = false;
let loading: Promise<void> | undefined;
let localOnly = false;
const timers = new Map<string, ReturnType<typeof setTimeout>>();
const listeners = new Set<() => void>();
let request = createClient();

function readJSON(key: string): Values {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(key) || "{}");
    return raw && typeof raw === "object" && !Array.isArray(raw) ? (raw as Values) : {};
  } catch {
    return {};
  }
}
function writeCache() {
  try {
    localStorage.setItem(CACHE, JSON.stringify(values));
    localStorage.setItem(UNSENT, JSON.stringify(unsent));
  } catch {}
}
function emit() {
  for (const listener of listeners) listener();
}

// For tests: a store with no browser and a scripted server.
export function resetPrefsForTest(initial: Values = {}, client?: ReturnType<typeof createClient>) {
  values = { ...initial };
  unsent = {};
  loaded = false;
  loading = undefined;
  localOnly = false;
  for (const timer of timers.values()) clearTimeout(timer);
  timers.clear();
  if (client) request = client;
}

try {
  window.addEventListener("storage", (event) => {
    if (event.key !== CACHE && event.key !== UNSENT) return;
    values = readJSON(CACHE);
    unsent = readJSON(UNSENT) as Record<string, true>;
    emit();
  });
} catch {}

export function subscribePrefs(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function getPref<T>(key: string, fallback: T): T {
  return key in values ? (values[key] as T) : fallback;
}

export function prefsLoaded() {
  return loaded;
}

// True once the server refused a write from this caller: the change is kept on
// this device, and Settings says so.
export function prefsLocalOnly() {
  return localOnly;
}

// Fetch this person's preferences. Unsent local changes keep their value and
// are sent again.
export function loadPrefs(): Promise<void> {
  loading ??= request<{ prefs: Values }>("/ui/prefs")
    .then((body) => {
      const next: Values = { ...(body.prefs || {}) };
      for (const key of Object.keys(unsent)) {
        if (key in values) next[key] = values[key]!;
        else delete next[key];
        schedule(key);
      }
      values = next;
      loaded = true;
      writeCache();
      emit();
    })
    .finally(() => {
      loading = undefined;
    });
  return loading;
}

function flush(key: string) {
  timers.delete(key);
  const value = values[key];
  const path = `/ui/prefs/${encodeURIComponent(key)}`;
  // keepalive lets a write started as the page unloads still arrive.
  const write = value === undefined
    ? request(path, { method: "DELETE", keepalive: true })
    : request(path, { method: "PUT", body: value, keepalive: true });
  void write.then(
    () => {
      if (values[key] === value && unsent[key]) {
        delete unsent[key];
        writeCache();
      }
    },
    (error: unknown) => {
      if (error instanceof ApiError && error.status === 403) {
        localOnly = true;
        emit();
        return;
      }
      try {
        globalThis.dispatchEvent?.(new CustomEvent("lec-prefs-error", { detail: String(error) }));
      } catch {}
    },
  );
}

function schedule(key: string) {
  clearTimeout(timers.get(key));
  timers.set(key, setTimeout(() => flush(key), WRITE_DELAY));
}

export function setPref(key: string, value: JsonValue | undefined) {
  const next = { ...values };
  if (value === undefined) delete next[key];
  else next[key] = value;
  values = next;
  unsent = { ...unsent, [key]: true };
  writeCache();
  emit();
  schedule(key);
}

// Writes still waiting for their debounce go now (tests, and page unload).
export function flushPrefs() {
  for (const [key, timer] of [...timers]) {
    clearTimeout(timer);
    flush(key);
  }
}
try {
  window.addEventListener("pagehide", flushPrefs);
} catch {}

// The fallback is held in a ref: a snapshot must be the same object from one
// read to the next, and a caller's literal is a new one every render.
export function usePref<T>(key: string, fallback: T): [T, (value: T | undefined) => void] {
  const initial = useRef(fallback);
  const value = useSyncExternalStore(
    subscribePrefs,
    () => getPref(key, initial.current),
    () => initial.current,
  );
  return [value, (next) => setPref(key, next as JsonValue | undefined)];
}

// Re-renders when preferences load or turn out to be local-only.
export function usePrefsState() {
  return useSyncExternalStore(
    subscribePrefs,
    () => (loaded ? 1 : 0) + (localOnly ? 2 : 0),
    () => 0,
  );
}
