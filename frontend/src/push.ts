// Pure logic behind "Enable phone alerts" (Settings → Notifications, and the
// one-time Needs-you prompt): whether push is even possible in this browser,
// and — when it is not — the one sentence that tells the owner what to do
// about it. Kept free of any live `navigator`/`window` access beyond the
// PushEnv snapshot below, so it is unit-testable without a real browser (the
// same split service-worker.ts/sw-actions.ts already uses).

/** The subset of browser feature-detection the availability check needs. */
export interface PushEnv {
  isSecureContext: boolean;
  hasServiceWorker: boolean;
  hasPushManager: boolean;
  hasNotification: boolean;
  isIOS: boolean;
  isStandalone: boolean;
}

/** Builds a PushEnv snapshot from a real window — called at the one call site. */
export function envFromWindow(win: Window & typeof globalThis): PushEnv {
  const nav = win.navigator;
  return {
    isSecureContext: win.isSecureContext === true,
    hasServiceWorker: "serviceWorker" in nav,
    hasPushManager: "PushManager" in win,
    hasNotification: "Notification" in win,
    isIOS: /iphone|ipad|ipod/i.test(nav.userAgent),
    isStandalone:
      (typeof win.matchMedia === "function" &&
        win.matchMedia("(display-mode: standalone)").matches) ||
      (nav as unknown as { standalone?: boolean }).standalone === true,
  };
}

export type PushUnavailableReason = "insecure" | "ios-not-installed" | "unsupported";

export interface PushAvailability {
  available: boolean;
  /** One sentence explaining why not, and what to do about it. Unset when available. */
  reason?: string;
  // Machine-readable form of `reason`, for a UI that wants to render more
  // than one sentence — the iOS Home Screen steps, specifically — without
  // parsing prose. Unset when available.
  reasonKind?: PushUnavailableReason;
}

// pushAvailability checks the conditions in the order a person would actually
// need to fix them: a non-secure origin blocks everything else from even
// being checked (http origins never expose a real PushManager), then iOS
// Safari's Home Screen requirement (the API exists but subscribing fails
// until the page is installed), then generic feature support.
export function pushAvailability(env: PushEnv): PushAvailability {
  if (!env.isSecureContext)
    return {
      available: false,
      reason: "Open Lectern over https to enable alerts.",
      reasonKind: "insecure",
    };
  if (env.isIOS && !env.isStandalone)
    return {
      available: false,
      reason:
        "On iPhone/iPad, add Lectern to the Home Screen first (Share → Add to Home Screen), then enable alerts from there.",
      reasonKind: "ios-not-installed",
    };
  if (!env.hasServiceWorker || !env.hasPushManager || !env.hasNotification)
    return {
      available: false,
      reason: "This browser does not support push notifications.",
      reasonKind: "unsupported",
    };
  return { available: true };
}

/** One subscribed device, as /api/push/subscriptions lists it. */
export interface PushSubscriptionInfo {
  id: number;
  endpoint: string;
  created_at: number;
}

// shortEndpoint turns a push endpoint URL into something short enough to show
// in a settings list without it wrapping the layout — the host plus a
// fragment of the subscription id, which is enough to tell two devices on the
// same push service apart without displaying the whole opaque URL.
export function shortEndpoint(endpoint: string): string {
  try {
    const url = new URL(endpoint);
    const tail = url.pathname.replace(/\/$/, "").split("/").pop() || "";
    return tail ? `${url.host}/…${tail.slice(-6)}` : url.host;
  } catch {
    return endpoint.length > 28 ? `…${endpoint.slice(-28)}` : endpoint;
  }
}

// isThisDevice compares a listed subscription's endpoint against this
// browser's own current subscription endpoint (or null before it is known).
export function isThisDevice(endpoint: string, currentEndpoint: string | null): boolean {
  return currentEndpoint != null && endpoint === currentEndpoint;
}
