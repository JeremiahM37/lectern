// Verifying the app shell against the host's signed manifest
// (internal/api/relay.go getShellManifest, docs/relay.md "Trusting the app
// code"). Used by the service worker; kept free of worker globals so it can
// be unit-tested.
import { ed25519 } from "@noble/curves/ed25519.js";
import { unb64url } from "./noise";

export const SHELL_LABEL = "lectern-shell-manifest-v1\0";

export interface SignedManifest {
  manifest: string;
  sig: string;
}

/** Returns the file hashes if the manifest is signed by the pinned key. */
export function verifyManifest(signed: SignedManifest, pinnedKey: string): Record<string, string> | null {
  try {
    const manifest = unb64url(signed.manifest);
    const label = new TextEncoder().encode(SHELL_LABEL);
    const message = new Uint8Array(label.length + manifest.length);
    message.set(label);
    message.set(manifest, label.length);
    if (!ed25519.verify(unb64url(signed.sig), message, unb64url(pinnedKey))) return null;
    const parsed = JSON.parse(new TextDecoder().decode(manifest)) as { v?: number; files?: Record<string, string> };
    return parsed.v === 1 && parsed.files ? parsed.files : null;
  } catch {
    return null;
  }
}

export async function sha256Hex(bytes: ArrayBuffer): Promise<string> {
  const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", bytes));
  return Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** The manifest key for a cached asset URL path. */
export function manifestKey(path: string): string {
  return path === "/index.html" ? "/" : path;
}
