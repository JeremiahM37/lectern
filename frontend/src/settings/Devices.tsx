import { useEffect, useState } from "react";
import type { SettingsApi } from "./Settings";
import { formatAgo } from "./ConnectTools";
import { QRCode } from "../pairing/QRCode";
import { PairLinkActions } from "../pairing/PairLinkActions";
import { RelayPanel } from "./RelayPanel";
import { t, useLocale } from "../i18n";
// devices.css is imported by main.tsx, matching connect-tools.css's own
// comment: this module gets its own node:test unit coverage (Devices.test.ts)
// and tsx's plain Node runtime has no loader for a bare .css import.

export interface PairedDevice {
  id: number;
  name: string;
  owner_login?: string;
  owner_kind?: string;
  user_agent?: string;
  paired_at: number;
  last_seen_at: number;
}

interface PairingSettings {
  enabled: boolean;
  env_forced: boolean;
  idle_days: number;
  device_count: number;
  untrusted_origin_hint: boolean;
  code_ttl_s: number;
}

interface MintedCode {
  code: string;
  expires_at: number;
  ttl_s: number;
}

// pairURL is what the QR code encodes and the typed field mirrors — a
// fragment (#code=...), not a query string, so the code never lands in a
// server access log on the phone's very first GET of the page.
function pairURL(code: string): string {
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  return `${origin}/pair#code=${encodeURIComponent(code)}`;
}

export function Devices({
  api,
  onNotice,
}: {
  api: SettingsApi;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [settings, setSettings] = useState<PairingSettings>();
  const [devices, setDevices] = useState<PairedDevice[]>([]);
  const [minted, setMinted] = useState<MintedCode>();
  const [secondsLeft, setSecondsLeft] = useState(0);
  const [idleDays, setIdleDays] = useState("30");
  const [busy, setBusy] = useState(false);

  function load() {
    void api
      .request<PairingSettings>("/pair/settings")
      .then((s) => {
        setSettings(s);
        setIdleDays(String(s.idle_days));
      })
      .catch((error) => onNotice(String(error), true));
    void api
      .request<PairedDevice[]>("/pair/devices")
      .then(setDevices)
      .catch(() => setDevices([]));
  }
  useEffect(load, []);

  // The minted code's own countdown — expiry is enforced server-side; this
  // is purely so the owner sees it go stale instead of scanning a dead code.
  useEffect(() => {
    if (!minted) return;
    const tick = () => setSecondsLeft(Math.max(0, Math.round(minted.expires_at - Date.now() / 1000)));
    tick();
    const id = window.setInterval(tick, 1000);
    return () => window.clearInterval(id);
  }, [minted]);

  async function toggle(enabled: boolean) {
    try {
      const s = await api.request<PairingSettings>("/pair/settings", { method: "PUT", body: { enabled } });
      setSettings(s);
      if (!enabled) setMinted(undefined);
    } catch (error) {
      onNotice(String(error), true);
    }
  }

  async function saveIdleDays() {
    const n = Number(idleDays);
    if (!Number.isFinite(n) || n <= 0) {
      onNotice(t("settings.devices.idleInvalid"), true);
      return;
    }
    try {
      const s = await api.request<PairingSettings>("/pair/settings", { method: "PUT", body: { idle_days: n } });
      setSettings(s);
      onNotice(t("settings.devices.saved"));
    } catch (error) {
      onNotice(String(error), true);
    }
  }

  async function mint() {
    setBusy(true);
    try {
      const out = await api.request<MintedCode>("/pair/mint", { method: "POST" });
      setMinted(out);
    } catch (error) {
      onNotice(String(error), true);
    } finally {
      setBusy(false);
    }
  }

  async function revoke(id: number) {
    if (!confirm(t("settings.devices.revokeConfirm"))) return;
    try {
      await api.request(`/pair/devices/${id}`, { method: "DELETE" });
      setDevices((old) => old.filter((d) => d.id !== id));
    } catch (error) {
      onNotice(String(error), true);
    }
  }

  if (!settings) return <RelayPanel api={api} onNotice={onNotice} />;
  const expired = !!minted && secondsLeft <= 0;

  return (
    <>
    <article className="devices-panel">
      <h3>{t("settings.section.devices")}</h3>
      <p className="subhint">
        {t("settings.devices.introBefore")} <code>tailscale funnel</code>{t("settings.devices.introAfter")}
      </p>
      {settings.untrusted_origin_hint && (
        <p className="subhint pairing-hint" role="alert">
          {t("settings.devices.untrusted")}
        </p>
      )}
      <label className="devices-toggle">
        <input
          type="checkbox"
          checked={settings.enabled}
          disabled={settings.env_forced}
          onChange={(e) => void toggle(e.target.checked)}
        />
        {t("settings.devices.allow")}
        {settings.env_forced && (
          <span className="subhint"> {t("settings.devices.forced")}</span>
        )}
      </label>
      {settings.enabled && (
        <>
          <div className="devices-idle">
            <label>
              {t("settings.devices.idleDays")}
              <input
                type="number"
                min={1}
                value={idleDays}
                onChange={(e) => setIdleDays(e.target.value)}
                onBlur={saveIdleDays}
              />
            </label>
          </div>
          <button className="b" onClick={() => void mint()} disabled={busy}>
            {busy ? t("settings.devices.generating") : t("settings.devices.pair")}
          </button>
          {minted && !expired && (
            <div className="pairing-mint" data-testid="pairing-mint">
              <QRCode value={pairURL(minted.code)} size={200} />
              <div className="pairing-mint-code">
                <p className="subhint">
                  {t("settings.devices.scanBefore")}{" "}
                  <code>{typeof window !== "undefined" ? window.location.origin : ""}/pair</code>{" "}
                  {t("settings.devices.scanAfter")}
                </p>
                <code className="pairing-code-text">{groupCode(minted.code)}</code>
                <p className="subhint">{t("settings.devices.expires", { seconds: secondsLeft })}</p>
                <PairLinkActions link={pairURL(minted.code)} onNotice={onNotice} />
              </div>
            </div>
          )}
          {minted && expired && (
            <p className="subhint">{t("settings.devices.expired")}</p>
          )}
          <h4 data-setting="devices.paired">{t("settings.devices.paired")}</h4>
          {devices.length === 0 ? (
            <p className="subhint">{t("settings.devices.none")}</p>
          ) : (
            <ul className="device-list">
              {devices.map((d) => (
                <li key={d.id} className="device-row">
                  <div className="device-info">
                    <strong>{d.name}</strong>
                    <span className="subhint">
                      {t("settings.devices.pairedSeen", { paired: formatAgo(d.paired_at), seen: formatAgo(d.last_seen_at) })}
                      {d.user_agent ? ` · ${d.user_agent}` : ""}
                    </span>
                  </div>
                  <button className="b" onClick={() => void revoke(d.id)}>
                    {t("settings.devices.revoke")}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </article>
    <RelayPanel api={api} onNotice={onNotice} />
    </>
  );
}

// groupCode mirrors the server's FormatCode (internal/pairing) purely for
// display — hyphenating a 32-character code into readable blocks. The
// exchange endpoint strips hyphens/case itself, so this is cosmetic only.
export function groupCode(raw: string): string {
  const upper = raw.toUpperCase();
  const groups: string[] = [];
  for (let i = 0; i < upper.length; i += 4) groups.push(upper.slice(i, i + 4));
  return groups.join("-");
}
