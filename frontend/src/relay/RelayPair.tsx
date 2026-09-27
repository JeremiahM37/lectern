// /relay-pair: where a phone lands after scanning Settings → Devices →
// Encrypted relay's QR code. The whole payload is in the URL fragment, which
// the browser never sends anywhere. Pairing generates this device's key,
// redeems the one-time code with the host over the relay, and stores the
// result; from then on every request goes through the tunnel (boot.ts).
import { useEffect, useState } from "react";
import { sha256 } from "@noble/hashes/sha2.js";
import { suggestedDeviceName } from "../pairing/Pair";
import { pairDevice, type PairPayload } from "./tunnel";
import { pinShellKey, pinShellNow, savePairing } from "./store";
import { unb64url } from "./noise";
import { inApp } from "../native/bridge";
import { t, useLocale } from "../i18n";

export function parsePairFragment(hash: string): PairPayload | undefined {
  const match = /[#&]p=([A-Za-z0-9_-]+)/.exec(hash);
  if (!match?.[1]) return undefined;
  try {
    const p = JSON.parse(new TextDecoder().decode(unb64url(match[1]))) as PairPayload;
    if (typeof p.relay !== "string" || typeof p.ch !== "string" || typeof p.hk !== "string" ||
        typeof p.sk !== "string" || typeof p.c !== "string" || typeof p.rt !== "string") return undefined;
    if (!/^wss?:\/\//.test(p.relay)) return undefined;
    return p;
  } catch {
    return undefined;
  }
}

/** Same format as internal/relay.Fingerprint, for comparing with Settings. */
export function fingerprint(keyB64: string): string {
  const hex = Array.from(sha256(unb64url(keyB64)).slice(0, 8), (b) => b.toString(16).padStart(2, "0")).join("");
  return hex.match(/.{4}/g)!.join(" ");
}

type Status = "idle" | "working" | "done" | "error";

export default function RelayPair() {
  useLocale();
  const [payload] = useState(() => parsePairFragment(window.location.hash));
  const [name, setName] = useState(() => suggestedDeviceName(navigator.userAgent));
  const [status, setStatus] = useState<Status>("idle");
  const [error, setError] = useState("");

  useEffect(() => {
    // Keep the one-time secrets out of browser history.
    if (window.location.hash) history.replaceState(null, "", "/relay-pair");
    // Install the app shell now, from the origin this page came from: after
    // pairing, the service worker serves it from cache and accepts only
    // updates signed by this Lectern (docs/relay.md). The Android app needs
    // none of this: its shell is the signed APK itself (docs/android.md).
    if (!inApp()) void navigator.serviceWorker?.register("/sw.js").catch(() => undefined);
  }, []);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!payload) return;
    setStatus("working");
    setError("");
    try {
      // Pin first: if the app on this origin is not the one this Lectern
      // signs, stop before any key is paired.
      if (!inApp()) {
        await pinShellKey(payload.sk);
        await pinShellNow();
      }
      const pairing = await pairDevice(payload, name.trim() || t("app.relayPair.defaultName"));
      await savePairing(pairing);
      setStatus("done");
      window.location.href = "/";
    } catch (err) {
      setStatus("error");
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  if (!payload) {
    return (
      <main className="pair-page">
        <div className="pair-card">
          <h1>{t("app.relayPair.titleShort")}</h1>
          <p role="alert" className="pair-error">
            {t("app.relayPair.noCode")}
          </p>
        </div>
      </main>
    );
  }

  let relayHost = payload.relay;
  try { relayHost = new URL(payload.relay).host; } catch { /* show as is */ }
  return (
    <main className="pair-page">
      <div className="pair-card" data-testid="relay-pair">
        <h1>{t("app.relayPair.title")}</h1>
        <p>
          {t("app.relayPair.through")}<strong>{relayHost}</strong>{t("app.relayPair.throughAfter")}
        </p>
        <p>
          {t("app.relayPair.key")}<code className="relay-fingerprint">{fingerprint(payload.hk)}</code>
          <br />{t("app.relayPair.keyMatch")}
        </p>
        <form onSubmit={(event) => void submit(event)}>
          <label>
            {t("app.pair.name")}
            <input id="relay-pair-name" value={name} maxLength={120} onChange={(event) => setName(event.target.value)} />
          </label>
          {status === "error" && (
            <p role="alert" className="pair-error">
              {error}
            </p>
          )}
          <button id="relay-pair-submit" type="submit" disabled={status === "working" || status === "done"}>
            {status === "working" ? t("app.pair.pairing") : status === "done" ? t("app.relayPair.paired") : t("app.pair.title")}
          </button>
        </form>
      </div>
    </main>
  );
}
