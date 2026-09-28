import { useEffect, useState } from "react";
import { Modal } from "../sessions/Modal";
import { QRCode } from "../pairing/QRCode";
import { PairLinkActions } from "../pairing/PairLinkActions";
import { bestChoice, directPairURL, phoneChoices, type PhoneAddresses, type PhoneOption } from "../pairing/phone";
import { relayPairURL } from "./RelayPanel";
import type { SettingsApi } from "./Settings";
import { t, useLocale } from "../i18n";

interface Minted {
  url: string;
  expiresAt: number;
}

// "Connect your phone" (docs/design/simple-ui.md): pick an address the phone
// can actually reach — this address, the tailnet name, the LAN address (with a
// warning) or the relay — each with one line saying what it means, then show
// a QR code for it. Pairing is turned on if it was off; the code expires.
export function PhoneWizard({ api, onNotice, onClose }: { api: SettingsApi; onNotice(text: string, error?: boolean): void; onClose(): void }) {
  useLocale();
  const [addresses, setAddresses] = useState<PhoneAddresses>();
  const [error, setError] = useState("");
  const [chosen, setChosen] = useState<PhoneOption>();
  const [minted, setMinted] = useState<Minted>();
  const [left, setLeft] = useState(0);
  const [busy, setBusy] = useState(false);
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  useEffect(() => {
    void api
      .request<PhoneAddresses>("/phone/addresses")
      .then(setAddresses)
      .catch((e) => setError(String(e instanceof Error ? e.message : e)));
  }, []);
  const choices = phoneChoices(addresses, origin);
  useEffect(() => {
    if (!chosen && addresses) setChosen(bestChoice(choices));
  }, [addresses]);
  useEffect(() => {
    if (!minted) return;
    const tick = () => setLeft(Math.max(0, Math.round(minted.expiresAt - Date.now() / 1000)));
    tick();
    const timer = window.setInterval(tick, 1000);
    return () => window.clearInterval(timer);
  }, [minted]);

  async function show() {
    if (!chosen?.available) return;
    setBusy(true);
    setMinted(undefined);
    try {
      if (chosen.kind === "relay") {
        const out = await api.request<{ fragment: string; shell_url?: string; expires_at: number }>("/relay/pair", { method: "POST" });
        setMinted({ url: relayPairURL(out.fragment, out.shell_url, origin), expiresAt: out.expires_at });
      } else {
        const settings = await api.request<{ enabled: boolean; env_forced: boolean }>("/pair/settings");
        if (!settings.enabled) {
          if (settings.env_forced) throw new Error(t("phone.pairingForcedOff"));
          await api.request("/pair/settings", { method: "PUT", body: { enabled: true } });
        }
        const out = await api.request<{ code: string; expires_at: number }>("/pair/mint", { method: "POST" });
        setMinted({ url: directPairURL(chosen.url!, out.code), expiresAt: out.expires_at });
      }
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
    } finally {
      setBusy(false);
    }
  }

  async function enableWiFi() {
    setBusy(true);
    try {
      const result = await api.request<{ url: string }>("/phone/wifi", { method: "POST" });
      setAddresses(await api.request<PhoneAddresses>("/phone/addresses"));
      setChosen({ kind: "lan", url: result.url, available: true, secure: false });
      setMinted(undefined);
    } catch (e) { onNotice(String(e), true); }
    finally { setBusy(false); }
  }

  const noneAvailable = !!addresses && !choices.some((row) => row.available);
  return (
    <Modal className="sheet phone-wizard" id="phone-wizard" aria-label={t("phone.title")} onCancel={onClose}>
      <div className="sheet-head">
        <h2>{t("phone.title")}</h2>
        <button className="x" aria-label={t("phone.close")} data-close onClick={onClose}>
          ✕
        </button>
      </div>
      <p>{t("phone.intro")}</p>
      {error && (
        <p className="subhint error" role="alert">
          {error}
        </p>
      )}
      {!addresses && !error && <p className="subhint">{t("phone.checking")}</p>}
      <div className="phone-choices" role="radiogroup" aria-label={t("phone.choices")}>
        {choices.map((row) => (
          <label key={row.kind} className="phone-choice" data-kind={row.kind} data-available={row.available ? "true" : "false"}>
            <input
              type="radio"
              name="phone-choice"
              value={row.kind}
              disabled={!row.available}
              checked={chosen?.kind === row.kind}
              onChange={() => {
                setChosen(row);
                setMinted(undefined);
              }}
            />
            <span className="phone-choice-body">
              <span className="phone-choice-title">
                {t(`phone.kind.${row.kind}`)}
                {row.url && row.kind !== "relay" && <code>{row.url}</code>}
              </span>
              <span className="phone-choice-note">
                {row.kind === "lan" && row.available && <span aria-hidden="true">⚠ </span>}
                {t(`phone.explain.${row.kind}`)}
              </span>
              {!row.available && row.reason && <span className="phone-choice-why">{t(`phone.why.${row.reason}`)}</span>}
            </span>
          </label>
        ))}
      </div>
      {noneAvailable && (
        <p className="subhint" id="phone-none" role="status">
          {t("phone.none")}
        </p>
      )}
      {addresses?.can_enable_wifi && !addresses.options.some(row => row.kind === "lan" && row.available) && (
        <div className="phone-wifi-enable">
          <p>{t("phone.wifiWarning", undefined, "Both devices must be on this network. Wi-Fi HTTP is unencrypted; enable it only on a trusted network. Access ends when this local runtime stops.")}</p>
          <button type="button" className="b" disabled={busy} onClick={() => void enableWiFi()}>{t("phone.enableWiFi", undefined, "Let my phone connect on this Wi-Fi")}</button>
        </div>
      )}
      <div className="phone-actions">
        <button
          type="button"
          className="b ok"
          id="phone-show-qr"
          disabled={!chosen?.available || busy}
          aria-describedby={!chosen?.available ? "phone-show-why" : undefined}
          onClick={() => void show()}
        >
          {busy ? t("phone.making") : t("phone.show")}
        </button>
        {!chosen?.available && addresses && (
          <span className="subhint" id="phone-show-why">
            {t("phone.showWhy")}
          </span>
        )}
      </div>
      {minted && left > 0 && (
        <div className="pairing-mint phone-qr" data-testid="phone-qr" data-url={minted.url}>
          <QRCode value={minted.url} size={220} />
          <div className="pairing-mint-code">
            <p className="subhint">{t("phone.scan")}</p>
            <p className="subhint">{t("settings.devices.expires", { seconds: left })}</p>
            <PairLinkActions link={minted.url} onNotice={onNotice} />
          </div>
        </div>
      )}
      {minted && left <= 0 && <p className="subhint">{t("settings.devices.expired")}</p>}
    </Modal>
  );
}
