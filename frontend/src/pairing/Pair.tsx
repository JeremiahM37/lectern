// The one page an unpaired device can reach with no credential at all (see
// internal/api/server.go's withAuth exemption for /api/pair/exchange, and
// this page's own reachability: any path with no file extension falls back
// to this same app bundle — see appRoute in internal/api/server.go — so
// /pair needs no server-side route of its own). Deliberately minimal: no
// board, no SSE, no other API calls before the exchange succeeds.
import { useEffect, useState } from "react";

// Pure logic, deliberately free of any live window/navigator access beyond
// an explicit string argument — same split push.ts uses for its own
// browser-feature checks, so this is unit-testable with no DOM at all.

// parseCodeFromHash reads a URL fragment, not a query string: the code never
// reaches a server access log on this page's very first GET (before any JS
// has run).
export function parseCodeFromHash(hash: string): string {
  const match = /code=([^&]+)/.exec(hash);
  return match?.[1] ? decodeURIComponent(match[1]) : "";
}

export function suggestedDeviceName(userAgent: string): string {
  const ua = userAgent || "";
  if (/iPad/.test(ua)) return "iPad";
  if (/iPhone/.test(ua)) return "iPhone";
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? "Android phone" : "Android tablet";
  if (/Macintosh/.test(ua)) return "Mac";
  if (/Windows/.test(ua)) return "Windows PC";
  if (/Linux/.test(ua)) return "Linux";
  return "My device";
}

type Status = "idle" | "working" | "error";

export default function Pair() {
  const [code, setCode] = useState(() => parseCodeFromHash(window.location.hash));
  const [name, setName] = useState(() => suggestedDeviceName(navigator.userAgent));
  const [status, setStatus] = useState<Status>("idle");
  const [error, setError] = useState("");

  // A code arriving via the QR link is ready to submit with one tap; strip
  // it from the visible URL so it doesn't linger in browser history.
  useEffect(() => {
    if (parseCodeFromHash(window.location.hash)) history.replaceState(null, "", "/pair");
  }, []);

  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setStatus("working");
    setError("");
    try {
      const response = await fetch("/api/pair/exchange", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          code: code.replace(/[-\s]/g, ""),
          name: name.trim(),
        }),
      });
      const body = (await response.json().catch(() => ({}))) as { detail?: string };
      if (!response.ok) throw new Error(body.detail || `Pairing failed (${response.status})`);
      // A non-secret marker, NOT the credential itself (that's the HttpOnly
      // cookie the exchange just set) — App.tsx's onUnauthorized reads this
      // to tell "this browser was paired and its device was revoked" (go
      // back to /pair) apart from "this browser never had any credential"
      // (show the access-token prompt instead, for the admin bootstrapping
      // over the tunnel with LECTERN_AUTH_TOKEN — see docs/remote-access.md).
      try {
        localStorage.setItem("lec-paired", "1");
      } catch {
        /* best-effort; worst case a revoked device sees the token prompt instead */
      }
      // The exchange set the device cookie already; reload straight into the
      // real app rather than trying to reuse any state from this page.
      window.location.href = "/";
    } catch (err) {
      setStatus("error");
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  return (
    <main className="pair-page">
      <div className="pair-card">
        <h1>Pair this device</h1>
        <p>
          Enter the code shown on your Lectern's Settings → Devices page, or
          scan its QR code with your camera.
        </p>
        <form onSubmit={(event) => void submit(event)}>
          <label>
            Pairing code
            <input
              id="pair-code"
              autoFocus
              autoCapitalize="characters"
              autoCorrect="off"
              autoComplete="off"
              spellCheck={false}
              value={code}
              onChange={(event) => setCode(event.target.value)}
              placeholder="XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX-XXXX"
            />
          </label>
          <label>
            Name this device
            <input
              id="pair-name"
              value={name}
              maxLength={120}
              onChange={(event) => setName(event.target.value)}
            />
          </label>
          {status === "error" && (
            <p role="alert" className="pair-error">
              {error}
            </p>
          )}
          <button id="pair-submit" type="submit" disabled={status === "working" || !code.trim()}>
            {status === "working" ? "Pairing…" : "Pair device"}
          </button>
        </form>
      </div>
    </main>
  );
}
