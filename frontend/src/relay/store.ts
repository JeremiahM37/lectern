// Where a paired device keeps its relay pairing: IndexedDB, which the
// service worker can read too (it needs the pinned shell key), plus one
// localStorage flag so the page can decide synchronously, before any module
// fetches anything, whether to route through the relay.
//
// In the Android app the pairing lives on the native side instead
// (native/bridge.ts): its route token encrypted with a Keystore key, its
// device key in Keystore itself.
import type { Pairing } from "./tunnel";
import { nativeBridge } from "../native/bridge";

const DB = "lectern-relay";
const STORE = "kv";
export const FLAG = "lec-relay";
export const TRANSPORT_PREF = "lec-transport";

function open(): Promise<IDBDatabase> {
  return new Promise((resolve, reject) => {
    const req = indexedDB.open(DB, 1);
    req.onupgradeneeded = () => req.result.createObjectStore(STORE);
    req.onsuccess = () => resolve(req.result);
    req.onerror = () => reject(req.error);
  });
}

async function tx<T>(mode: IDBTransactionMode, run: (s: IDBObjectStore) => IDBRequest<T>): Promise<T> {
  const db = await open();
  try {
    return await new Promise<T>((resolve, reject) => {
      const req = run(db.transaction(STORE, mode).objectStore(STORE));
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  } finally {
    db.close();
  }
}

export async function loadPairing(): Promise<Pairing | undefined> {
  const bridge = nativeBridge();
  if (bridge) {
    const raw = bridge.relayPairing();
    return raw ? (JSON.parse(raw) as Pairing) : undefined;
  }
  try {
    return (await tx<Pairing | undefined>("readonly", (s) => s.get("pairing"))) ?? undefined;
  } catch {
    return undefined;
  }
}

/** Pins the shell signing key the service worker checks every shell
 * against (docs/relay.md). */
export async function pinShellKey(key: string): Promise<void> {
  await tx("readwrite", (s) => s.put(key, "shell-key"));
}

/** Saves the pairing and turns the relay transport on for this origin. */
export async function savePairing(p: Pairing): Promise<void> {
  const bridge = nativeBridge();
  if (bridge) {
    bridge.saveRelayPairing(JSON.stringify(p));
    return;
  }
  await tx("readwrite", (s) => s.put(p, "pairing"));
  await tx("readwrite", (s) => s.put(p.sk, "shell-key"));
  try {
    localStorage.setItem(FLAG, "1");
    localStorage.removeItem(TRANSPORT_PREF);
  } catch { /* storage blocked: the IndexedDB copy still works */ }
}

/** Asks the service worker to pin the shell now, while the origin it was
 * installed from is still reachable, and waits for its verdict: the shell on
 * this origin must be exactly the one this Lectern signs. */
export async function pinShellNow(timeoutMs = 60000): Promise<void> {
  const sw = navigator.serviceWorker;
  if (!sw) throw new Error("This browser cannot install the app (no service worker support).");
  const registration = await sw.ready;
  const target = registration.active;
  if (!target) throw new Error("The app is not installed yet; reload and try again.");
  const channel = new MessageChannel();
  const verdict = new Promise<{ ok: boolean; error?: string }>((resolve) => {
    channel.port1.onmessage = (event) => resolve(event.data as { ok: boolean; error?: string });
    setTimeout(() => resolve({ ok: false, error: "the app did not finish installing" }), timeoutMs);
  });
  target.postMessage({ type: "lec-relay-pinned" }, [channel.port2]);
  const result = await verdict;
  if (!result.ok) throw new Error("Could not pin the app shell: " + (result.error || "unknown error"));
}

/** Forgets this device's key. The shell key stays pinned on purpose: a
 * device that was once paired keeps refusing unsigned shells until the app is
 * reinstalled. */
export async function forgetPairing(): Promise<void> {
  const bridge = nativeBridge();
  if (bridge) {
    bridge.forgetPairing();
    return;
  }
  await tx("readwrite", (s) => s.delete("pairing"));
  try { localStorage.removeItem(FLAG); } catch { /* ignore */ }
}

export function relayFlagged(): boolean {
  const bridge = nativeBridge();
  if (bridge) return bridge.mode() === "relay";
  try {
    return localStorage.getItem(FLAG) === "1";
  } catch {
    return false;
  }
}

export function preferDirect(): boolean {
  if (nativeBridge()) return false; // the app picks its transport natively
  try {
    return localStorage.getItem(TRANSPORT_PREF) === "direct";
  } catch {
    return false;
  }
}

export function setPreferDirect(direct: boolean) {
  try {
    if (direct) localStorage.setItem(TRANSPORT_PREF, "direct");
    else localStorage.removeItem(TRANSPORT_PREF);
  } catch { /* ignore */ }
}
