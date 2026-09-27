import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";
import { formatAgo } from "./ConnectTools";
import { QRCode } from "../pairing/QRCode";
import { inApp } from "../native/bridge";
import { forgetPairing, preferDirect, relayFlagged, setPreferDirect } from "../relay/store";

// Settings → Devices → Encrypted relay (docs/relay.md). The server half is
// internal/api/relay.go.

export interface RelayDevice {
  id: number;
  name: string;
  fingerprint: string;
  owner_login?: string;
  paired_at: number;
  last_seen_at: number;
  connected: boolean;
}

interface RelayStatus {
  configured: boolean;
  connected?: boolean;
  relay_url?: string;
  host_fingerprint?: string;
  shell_fingerprint?: string;
  shell_url?: string;
  error?: string;
  devices: RelayDevice[];
}

interface Minted {
  fragment: string;
  shell_url?: string;
  expires_at: number;
  host_fingerprint: string;
}

/** The QR code's link: the shell origin the phone installs the app from,
 * with every secret in the fragment. */
export function relayPairURL(fragment: string, shellURL: string | undefined, origin: string): string {
  return `${(shellURL || origin).replace(/\/+$/, "")}/relay-pair#p=${fragment}`;
}

function ThisDevice() {
  const tunnel = typeof window !== "undefined" ? window.__lecternRelay : undefined;
  const [, rerender] = useState(0);
  useEffect(() => {
    if (!tunnel) return;
    const on = () => rerender((n) => n + 1);
    tunnel.addEventListener("status", on);
    return () => tunnel.removeEventListener("status", on);
  }, [tunnel]);
  if (!relayFlagged()) {
    // The Android app connected straight to this Lectern: the one place to
    // disconnect it and pair again (docs/android.md).
    if (!inApp()) return null;
    return (
      <div className="relay-this-device" data-testid="app-this-device">
        <h4>This app</h4>
        <p className="subhint">Connected directly to {window.location.host}.</p>
        <button className="b" onClick={() => { if (confirm("Disconnect this app from Lectern?")) void forgetPairing(); }}>Disconnect this app</button>
      </div>
    );
  }
  const direct = preferDirect();
  async function forget() {
    if (!confirm("Forget this device's relay pairing? You will need a new QR code to pair it again.")) return;
    await forgetPairing();
    window.location.reload();
  }
  return (
    <div className="relay-this-device" data-testid="relay-this-device">
      <h4>This device</h4>
      <p className="subhint">
        {direct
          ? "Paired over the relay, but set to connect directly."
          : `Connected through the encrypted relay: ${tunnel?.status ?? "starting"}${tunnel?.detail ? ` — ${tunnel.detail}` : ""}`}
      </p>
      {!inApp() && <label className="devices-toggle">
        <input type="checkbox" checked={direct} onChange={(e) => { setPreferDirect(e.target.checked); window.location.reload(); }} />
        Connect directly instead of through the relay
      </label>}
      <button className="b" onClick={() => void forget()}>Forget this pairing</button>
    </div>
  );
}

export function RelayPanel({ api, onNotice }: { api: SettingsApi; onNotice(t: string, e?: boolean): void }) {
  const [status, setStatus] = useState<RelayStatus>();
  const [minted, setMinted] = useState<Minted>();
  const [secondsLeft, setSecondsLeft] = useState(0);
  const [busy, setBusy] = useState(false);

  function load() {
    void api.request<RelayStatus>("/relay").then(setStatus).catch(() => setStatus(undefined));
  }
  useEffect(load, []);
  useEffect(() => {
    if (!minted) return;
    const tick = () => {
      const left = Math.max(0, Math.round(minted.expires_at - Date.now() / 1000));
      setSecondsLeft(left);
      if (left % 5 === 0) load(); // pick up the new device once it pairs
    };
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, [minted]);

  async function mint() {
    setBusy(true);
    try {
      setMinted(await api.request<Minted>("/relay/pair", { method: "POST" }));
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }

  async function revoke(d: RelayDevice) {
    if (!confirm(`Revoke ${d.name}? Its key is deleted and its connection closed at once.`)) return;
    try {
      await api.request(`/relay/devices/${d.id}`, { method: "DELETE" });
      load();
    } catch (error) {
      onNotice(String(error), true);
    }
  }

  const expired = !!minted && secondsLeft <= 0;
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  return (
    <article className="devices-panel relay-panel" data-testid="relay-panel">
      <h3>Encrypted relay</h3>
      <p className="subhint">
        Reach Lectern from a phone with no VPN and no open port. Lectern and the
        phone both connect out to a relay, which passes only end-to-end encrypted
        messages and cannot read them. See docs/relay.md.
      </p>
      <ThisDevice />
      {!status ? null : !status.configured ? (
        <p className="subhint">Not set up. Run <code>lectern relay</code> somewhere both sides can reach and set <code>LECTERN_RELAY_URL</code> and <code>LECTERN_RELAY_HOST_SECRET</code>.</p>
      ) : (
        <>
          <p className="subhint" data-testid="relay-state">
            {status.connected ? "Connected to " : "Not connected to "}<code>{status.relay_url}</code>
            {!status.connected && status.error ? ` — ${status.error}` : ""}
            <br />Lectern key <code>{status.host_fingerprint}</code> · shell key <code>{status.shell_fingerprint}</code>
          </p>
          <button className="b" onClick={() => void mint()} disabled={busy || !status.connected}>
            {busy ? "Generating…" : "Pair a phone over the relay"}
          </button>
          {minted && !expired && (
            <div className="pairing-mint" data-testid="relay-pairing-mint">
              <QRCode value={relayPairURL(minted.fragment, minted.shell_url, origin)} size={220} />
              <div className="pairing-mint-code">
                <p className="subhint">
                  Scan with the phone's camera. It opens Lectern on{" "}
                  <code>{minted.shell_url || origin}</code> once to install the app, then uses the relay.
                </p>
                <p className="subhint">Lectern key: <code>{minted.host_fingerprint}</code></p>
                <a className="relay-pair-link" href={relayPairURL(minted.fragment, minted.shell_url, origin)}>Pairing link</a>
                <p className="subhint">Expires in {secondsLeft}s · single use</p>
              </div>
            </div>
          )}
          {minted && expired && <p className="subhint">That code expired. Generate a new one.</p>}
          <h4>Relay devices</h4>
          {status.devices.length === 0 ? (
            <p className="subhint">No devices paired over the relay.</p>
          ) : (
            <ul className="device-list">
              {status.devices.map((d) => (
                <li key={d.id} className="device-row" data-testid="relay-device">
                  <div className="device-info">
                    <strong>{d.name}</strong>
                    <span className="subhint">
                      {d.connected ? "Connected now · " : ""}key <code>{d.fingerprint}</code> · paired {formatAgo(d.paired_at)} · last seen {formatAgo(d.last_seen_at)}
                    </span>
                  </div>
                  <button className="b" onClick={() => void revoke(d)}>Revoke</button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </article>
  );
}
