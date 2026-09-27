// Push in the Android app: UnifiedPush (with ntfy or any other distributor)
// instead of the browser's PushManager. The distributor hands the app a Web
// Push endpoint plus RFC 8291 keys, so the host stores and encrypts to it
// exactly like a browser subscription (/api/push/subscribe, internal/push).
import { nativeBridge } from "./bridge";

export interface NativeSubscription {
  endpoint: string;
  keys: { p256dh: string; auth: string };
  /** Whether the host has accepted this endpoint. */
  confirmed?: boolean;
}

type Request = <T>(path: string, init?: { method?: string; body?: { [key: string]: string | { [key: string]: string } } }) => Promise<T>;

export const PUSH_EVENT = "lectern-native-push";

export function nativeSubscription(): NativeSubscription | undefined {
  const raw = nativeBridge()?.pushSubscription();
  if (!raw) return undefined;
  try {
    const sub = JSON.parse(raw) as NativeSubscription;
    return sub.endpoint ? sub : undefined;
  } catch {
    return undefined;
  }
}

/** Sends the app's endpoint to the host when it has not accepted it yet
 * (first registration, or the distributor issued a new endpoint). */
export async function syncNativePush(request: Request): Promise<string | undefined> {
  const bridge = nativeBridge();
  const sub = nativeSubscription();
  if (!bridge || !sub) return undefined;
  if (!sub.confirmed) {
    await request("/push/subscribe", { method: "POST", body: { endpoint: sub.endpoint, keys: sub.keys } });
    bridge.pushSubscribed(sub.endpoint);
  }
  return sub.endpoint;
}

/** Registers with the device's UnifiedPush distributor and the host. */
export async function enableNativePush(request: Request, timeoutMs = 60000): Promise<string> {
  const bridge = nativeBridge();
  if (!bridge) throw new Error("not running in the Lectern app");
  const { key } = await request<{ key: string }>("/push/vapid");
  const result = new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(() => {
      window.removeEventListener(PUSH_EVENT, on);
      reject(new Error("the push distributor did not answer; is ntfy (or another UnifiedPush app) installed?"));
    }, timeoutMs);
    const on = (event: Event) => {
      const detail = (event as CustomEvent<{ ok: boolean; error?: string }>).detail;
      window.clearTimeout(timer);
      window.removeEventListener(PUSH_EVENT, on);
      if (detail?.ok) resolve();
      else reject(new Error(detail?.error || "push registration failed"));
    };
    window.addEventListener(PUSH_EVENT, on);
  });
  bridge.enablePush(key);
  await result;
  const endpoint = await syncNativePush(request);
  if (!endpoint) throw new Error("push registration failed");
  return endpoint;
}
