import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";
import { formatAgo } from "./ConnectTools";
import { QRCode } from "../pairing/QRCode";
import { PairLinkActions } from "../pairing/PairLinkActions";
import { inApp } from "../native/bridge";
import { forgetPairing, preferDirect, relayFlagged, setPreferDirect } from "../relay/store";
import { t, useLocale } from "../i18n";

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
  useLocale();
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
        <h4>{t("settings.relay.thisApp")}</h4>
        <p className="subhint">{t("settings.relay.directTo", { host: window.location.host })}</p>
        <button className="b" onClick={() => { if (confirm(t("settings.relay.disconnectConfirm"))) void forgetPairing(); }}>{t("settings.relay.disconnect")}</button>
      </div>
    );
  }
  const direct = preferDirect();
  async function forget() {
    if (!confirm(t("settings.relay.forgetConfirm"))) return;
    await forgetPairing();
    window.location.reload();
  }
  return (
    <div className="relay-this-device" data-testid="relay-this-device">
      <h4>{t("settings.relay.thisDevice")}</h4>
      <p className="subhint">
        {direct
          ? t("settings.relay.pairedDirect")
          : `${t("settings.relay.through", { status: tunnel?.status ?? t("settings.relay.starting") })}${tunnel?.detail ? ` — ${tunnel.detail}` : ""}`}
      </p>
      {!inApp() && <label className="devices-toggle">
        <input type="checkbox" checked={direct} onChange={(e) => { setPreferDirect(e.target.checked); window.location.reload(); }} />
        {t("settings.relay.preferDirect")}
      </label>}
      <button className="b" onClick={() => void forget()}>{t("settings.relay.forget")}</button>
    </div>
  );
}

export function RelayPanel({ api, onNotice }: { api: SettingsApi; onNotice(t: string, e?: boolean): void }) {
  useLocale();
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
    if (!confirm(t("settings.relay.revokeConfirm", { name: d.name }))) return;
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
      <h3 data-setting="devices.relay">{t("settings.relay.title")}</h3>
      <p className="subhint">
        {t("settings.relay.hint")}
      </p>
      <ThisDevice />
      {!status ? null : !status.configured ? (
        <p className="subhint">{t("settings.relay.notSetUp1")} <code>lectern relay</code> {t("settings.relay.notSetUp2")} <code>LECTERN_RELAY_URL</code> {t("settings.relay.notSetUp3")} <code>LECTERN_RELAY_HOST_SECRET</code>{t("settings.relay.notSetUp4")}</p>
      ) : (
        <>
          <p className="subhint" data-testid="relay-state">
            {status.connected ? t("settings.relay.connectedTo") : t("settings.relay.notConnectedTo")} <code>{status.relay_url}</code>
            {!status.connected && status.error ? ` — ${status.error}` : ""}
            <br />{t("settings.relay.lecternKey")} <code>{status.host_fingerprint}</code> · {t("settings.relay.shellKey")} <code>{status.shell_fingerprint}</code>
          </p>
          <button className="b" onClick={() => void mint()} disabled={busy || !status.connected}>
            {busy ? t("settings.devices.generating") : t("settings.relay.pair")}
          </button>
          {minted && !expired && (
            <div className="pairing-mint" data-testid="relay-pairing-mint">
              <QRCode value={relayPairURL(minted.fragment, minted.shell_url, origin)} size={220} />
              <div className="pairing-mint-code">
                <p className="subhint">
                  {t("settings.relay.scanBefore")}{" "}
                  <code>{minted.shell_url || origin}</code> {t("settings.relay.scanAfter")}
                </p>
                <p className="subhint">{t("settings.relay.lecternKeyColon")} <code>{minted.host_fingerprint}</code></p>
                <a className="relay-pair-link" href={relayPairURL(minted.fragment, minted.shell_url, origin)}>{t("settings.relay.link")}</a>
                <p className="subhint">{t("settings.devices.expires", { seconds: secondsLeft })}</p>
                <PairLinkActions link={relayPairURL(minted.fragment, minted.shell_url, origin)} onNotice={onNotice} />
              </div>
            </div>
          )}
          {minted && expired && <p className="subhint">{t("settings.devices.expired")}</p>}
          <h4>{t("settings.relay.devices")}</h4>
          {status.devices.length === 0 ? (
            <p className="subhint">{t("settings.relay.none")}</p>
          ) : (
            <ul className="device-list">
              {status.devices.map((d) => (
                <li key={d.id} className="device-row" data-testid="relay-device">
                  <div className="device-info">
                    <strong>{d.name}</strong>
                    <span className="subhint">
                      {d.connected ? `${t("settings.relay.connectedNow")} · ` : ""}{t("settings.relay.key")} <code>{d.fingerprint}</code> · {t("settings.relay.pairedSeen", { paired: formatAgo(d.paired_at), seen: formatAgo(d.last_seen_at) })}
                    </span>
                  </div>
                  <button className="b" onClick={() => void revoke(d)}>{t("settings.devices.revoke")}</button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </article>
  );
}
