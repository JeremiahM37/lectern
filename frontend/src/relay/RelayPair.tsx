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
  const [payload] = useState(() => parsePairFragment(window.location.hash));
  const [name, setName] = useState(() => suggestedDeviceName(navigator.userAgent));
  const [status, setStatus] = useState<Status>("idle");
  const [error, setError] = useState("");

  useEffect(() => {
    // Keep the one-time secrets out of browser history.
    if (window.location.hash) history.replaceState(null, "", "/relay-pair");
    // Install the app shell now, from the origin this page came from: after
    // pairing, the service worker serves it from cache and accepts only
    // updates signed by this Lectern (docs/relay.md).
    void navigator.serviceWorker?.register("/sw.js").catch(() => undefined);
  }, []);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    if (!payload) return;
    setStatus("working");
    setError("");
    try {
      // Pin first: if the app on this origin is not the one this Lectern
      // signs, stop before any key is paired.
      await pinShellKey(payload.sk);
      await pinShellNow();
      const pairing = await pairDevice(payload, name.trim() || "Relay device");
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
          <h1>Pair over the relay</h1>
          <p role="alert" className="pair-error">
            This link has no pairing code. Open Settings → Devices → Encrypted
            relay on your Lectern and scan a fresh QR code.
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
        <h1>Pair over the encrypted relay</h1>
        <p>
          This phone will reach Lectern through <strong>{relayHost}</strong>.
          The relay only passes encrypted messages; it cannot read or change them.
        </p>
        <p>
          Lectern's key: <code className="relay-fingerprint">{fingerprint(payload.hk)}</code>
          <br />It should match the one shown under the QR code.
        </p>
        <form onSubmit={(event) => void submit(event)}>
          <label>
            Name this device
            <input id="relay-pair-name" value={name} maxLength={120} onChange={(event) => setName(event.target.value)} />
          </label>
          {status === "error" && (
            <p role="alert" className="pair-error">
              {error}
            </p>
          )}
          <button id="relay-pair-submit" type="submit" disabled={status === "working" || status === "done"}>
            {status === "working" ? "Pairing…" : status === "done" ? "Paired" : "Pair this device"}
          </button>
        </form>
      </div>
    </main>
  );
}
