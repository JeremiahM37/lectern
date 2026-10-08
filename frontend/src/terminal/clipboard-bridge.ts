// Clipboard bridge: tells the server a browser is attached to a session and
// copies pasted images into the session machine's headless clipboard, so
// programs that read the clipboard natively (Codex) can find them.

export const MIRROR_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp"];
export const MIRROR_MAX_BYTES = 20 * 1024 * 1024;
const CLIENT_KEY = "lectern.clipboard.client";

const CLIENT_RE = /^[A-Za-z0-9_.-]{4,64}$/;

export function mirrorEligible(file: { type: string; size: number }): boolean {
  return MIRROR_TYPES.includes(file.type.toLowerCase()) && file.size > 0 && file.size <= MIRROR_MAX_BYTES;
}

export function newClientId(random: () => number = Math.random): string {
  let out = "web-";
  for (let i = 0; i < 12; i++) out += Math.floor(random() * 36).toString(36);
  return out;
}

/** A stable id for this browser tab; falls back to a fresh one without storage. */
export function clientId(storage?: Pick<Storage, "getItem" | "setItem">): string {
  try {
    const store = storage ?? sessionStorage;
    const old = store.getItem(CLIENT_KEY);
    if (old && CLIENT_RE.test(old)) return old;
    const made = newClientId();
    store.setItem(CLIENT_KEY, made);
    return made;
  } catch {
    return newClientId();
  }
}

export function listenUrl(client: string, session: number): string {
  return `/api/clipboard/listen?client=${encodeURIComponent(client)}&session=${session}&kind=web&can_read=0`;
}

/** Returns a function that reports when it is allowed to fire (at most once per interval). */
export function throttle(intervalMs: number, now: () => number = Date.now): () => boolean {
  let last = -Infinity;
  return () => {
    const t = now();
    if (t - last < intervalMs) return false;
    last = t;
    return true;
  };
}

/** Fire and forget: failures never block the paste flow. */
export function mirrorImage(file: File, session: number): void {
  if (!mirrorEligible(file)) return;
  try {
    void fetch(`/api/clipboard/mirror?session=${session}`, {
      method: "PUT",
      headers: { "Content-Type": file.type.toLowerCase() },
      body: file,
    }).catch(() => {});
  } catch {
    /* ignore */
  }
}

export function pingActive(client: string): void {
  try {
    void fetch(`/api/clipboard/active?client=${encodeURIComponent(client)}`, { method: "POST" }).catch(() => {});
  } catch {
    /* ignore */
  }
}

/**
 * The image on the phone's own clipboard, when this page runs inside the
 * Android app (which can read it; a web page cannot without a gesture). Null
 * in a browser, in an older app, or when the clipboard holds no image.
 */
export function nativeClipboardImage(): File | null {
  try {
    const host = (typeof window !== "undefined" ? window.LecternNative : undefined) as
      | { clipboardImageType?: () => string; clipboardImage?: () => string }
      | undefined;
    if (!host?.clipboardImageType?.()) return null;
    const raw = host.clipboardImage?.();
    if (!raw) return null;
    const { mime, base64 } = JSON.parse(raw) as { mime: string; base64: string };
    if (!mime || !base64) return null;
    const bytes = Uint8Array.from(atob(base64), (c) => c.charCodeAt(0));
    const ext = mime.split("/")[1] || "png";
    return new File([bytes], `screenshot-${Date.now()}.${ext}`, { type: mime });
  } catch {
    return null;
  }
}

/** Whether the Android app answers clipboard requests itself (so the page must not also register). */
export function nativeAnswersClipboard(): boolean {
  try {
    return typeof window !== "undefined" && typeof window.LecternNative?.clipboardSession === "function";
  } catch {
    return false;
  }
}
