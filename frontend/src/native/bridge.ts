// The Android app's native side (mobile/android, docs/android.md). The app
// runs this same React build from inside its APK and exposes a small
// synchronous bridge as window.LecternNative. Everything here is a no-op in a
// browser, where the bridge does not exist.
//
// The bridge holds what a page should not: the relay device key (in Android
// Keystore, used through relayDH so the private half never reaches
// JavaScript), the relay route token and direct-mode device token (encrypted
// with a Keystore key), and the UnifiedPush registration.

export interface NativeBridge {
  version(): string;
  /** "relay", "direct" or "" (not connected yet). */
  mode(): string;
  /** This device's relay X25519 public key, base64url. Creates it once. */
  relayPublicKey(): string;
  /** X25519 between the device key and a peer public key, both base64url. */
  relayDH(peer: string): string;
  /** The stored relay pairing (JSON, without any secret key) or "". */
  relayPairing(): string;
  saveRelayPairing(json: string): void;
  saveDeviceToken(token: string): void;
  forgetPairing(): void;
  /** Starts UnifiedPush registration; answered with a "lectern-native-push" event. */
  enablePush(vapidKey: string): void;
  disablePush(): void;
  /** {"endpoint", "keys": {"p256dh", "auth"}} of the current registration, or "". */
  pushSubscription(): string;
  /** Records that the host accepted this endpoint. */
  pushSubscribed(endpoint: string): void;
  /** Result of a background action (native-action.ts). */
  actionResult(json: string): void;
  // ---- added in app 0.2.0; absent from 0.1.0, so callers check first ----
  /** A short vibration through the view: "tick", "confirm" or "warn". */
  haptic?(kind: string): void;
  /** The Lecterns this app is paired with (JSON: [{id, label, mode, origin, active}]). */
  hosts?(): string;
  /** Switches the app to another paired Lectern. */
  switchHost?(id: string): void;
  /** Opens the native list of paired Lecterns (add, rename, remove). */
  openHosts?(): void;
  /** Opens an http(s) address in the phone's browser. */
  openUrl?(url: string): void;
}

export interface AppHost {
  id: string;
  label: string;
  mode: string;
  origin: string;
  active: boolean;
}

/** The app's paired Lecterns, or [] in a browser or an older app. */
export function appHosts(): AppHost[] {
  try {
    const raw = nativeBridge()?.hosts?.();
    const list: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(list) ? (list as AppHost[]) : [];
  } catch {
    return [];
  }
}

declare global {
  interface Window {
    LecternNative?: NativeBridge;
  }
}

export function nativeBridge(): NativeBridge | undefined {
  try {
    return typeof window !== "undefined" ? window.LecternNative : undefined;
  } catch {
    return undefined;
  }
}

export function inApp(): boolean {
  return nativeBridge() !== undefined;
}
