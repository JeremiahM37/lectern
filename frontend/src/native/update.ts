// Updating the Android app from inside it (docs/android.md, "Updates"). The
// native side (Updates.kt) reads the newest release's lectern-android.json,
// downloads and verifies the APK and hands it to Android's installer; this
// keeps what it last said, for the banner and Settings → Phone & devices.
import { useSyncExternalStore } from "react";
import { nativeBridge } from "./bridge";

export interface UpdateStatus {
  state: "idle" | "checking" | "current" | "available" | "downloading" | "installing" | "error";
  current?: string;
  latest?: string;
  progress?: number;
  error?: string;
}

const CHECKED_KEY = "lec-app-update-checked";
const DISMISSED_KEY = "lec-app-update-dismissed";
const EVERY_MS = 6 * 60 * 60 * 1000;

let status: UpdateStatus = { state: "idle" };
const listeners = new Set<() => void>();
const set = (next: UpdateStatus) => {
  status = next;
  for (const fn of listeners) fn();
};
let listening = false;
function listen() {
  if (listening || typeof window === "undefined") return;
  listening = true;
  window.addEventListener("lectern-native-update", (event) => {
    const detail = (event as CustomEvent<UpdateStatus>).detail;
    // An error from the installer does not repeat which version it was.
    if (detail && typeof detail.state === "string") set({ latest: status.latest, ...detail });
  });
}

/** Whether this app can update itself (app 2.8.0 and later). */
export function canUpdate(): boolean {
  return typeof nativeBridge()?.checkUpdate === "function";
}

export function checkForUpdate() {
  const bridge = nativeBridge();
  if (!bridge?.checkUpdate) return;
  listen();
  set({ ...status, state: "checking", error: undefined });
  try {
    localStorage.setItem(CHECKED_KEY, String(Date.now()));
  } catch {
    // Private storage: the check still runs, only more often.
  }
  bridge.checkUpdate();
}

/** At most every few hours, quietly, when the app opens. */
export function checkForUpdateNow(now = Date.now()) {
  if (!canUpdate()) return;
  let last = 0;
  try {
    last = Number(localStorage.getItem(CHECKED_KEY)) || 0;
  } catch {
    last = 0;
  }
  if (now - last >= EVERY_MS) checkForUpdate();
}

export function installUpdate() {
  const bridge = nativeBridge();
  if (!bridge?.installUpdate) return;
  listen();
  set({ ...status, state: "downloading", progress: 0, error: undefined });
  bridge.installUpdate();
}

export function dismissUpdate(version: string) {
  try {
    localStorage.setItem(DISMISSED_KEY, version);
  } catch {
    // Not remembered; the banner comes back next time.
  }
  set({ ...status });
}

export function updateDismissed(version: string | undefined): boolean {
  try {
    return !!version && localStorage.getItem(DISMISSED_KEY) === version;
  } catch {
    return false;
  }
}

const subscribe = (fn: () => void) => {
  listen();
  listeners.add(fn);
  return () => listeners.delete(fn);
};

export function useUpdateStatus(): UpdateStatus {
  return useSyncExternalStore(subscribe, () => status, () => status);
}

/** Test seam: feed a status as the native side would. */
export function _setUpdateStatus(next: UpdateStatus) {
  set(next);
}
