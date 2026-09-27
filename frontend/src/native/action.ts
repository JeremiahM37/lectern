// /native-action: a page the Android app loads, unseen, when a notification
// button is pressed while no Lectern screen is open (docs/android.md). It makes
// one API call over this device's normal connection (the encrypted relay
// here, through relay/boot.ts's shims) and reports the result to the app.
// Nothing is rendered.
import { nativeBridge } from "./bridge";
import { unb64url } from "../relay/noise";

export interface NativeAction {
  id: string;
  method: string;
  path: string;
  body?: unknown;
}

export function parseAction(hash: string): NativeAction | undefined {
  const match = /[#&]a=([A-Za-z0-9_-]+)/.exec(hash);
  if (!match?.[1]) return undefined;
  try {
    const a = JSON.parse(new TextDecoder().decode(unb64url(match[1]))) as NativeAction;
    // Only this Lectern's own API, never an arbitrary URL.
    if (typeof a.id !== "string" || typeof a.path !== "string" || !/^\/api\/[A-Za-z0-9_/-]+$/.test(a.path)) return undefined;
    if (!["GET", "POST", "PUT", "DELETE"].includes(a.method)) return undefined;
    return a;
  } catch {
    return undefined;
  }
}

export async function runNativeAction(): Promise<void> {
  const bridge = nativeBridge();
  const action = parseAction(window.location.hash);
  if (!bridge || !action) return;
  const report = (r: { ok: boolean; status: number; body: string }) => bridge.actionResult(JSON.stringify({ id: action.id, ...r }));
  try {
    const response = await fetch(action.path, {
      method: action.method,
      headers: action.body === undefined ? undefined : { "Content-Type": "application/json" },
      body: action.body === undefined ? undefined : JSON.stringify(action.body),
    });
    report({ ok: response.ok, status: response.status, body: (await response.text()).slice(0, 2000) });
  } catch (err) {
    report({ ok: false, status: 0, body: err instanceof Error ? err.message : String(err) });
  }
}
