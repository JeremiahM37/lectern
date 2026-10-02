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

// The private runtime started by `lectern up` can also listen on this
// computer's Wi-Fi address on request (cmd/lectern/localruntime/phone.go);
// a hosted server has no such route and answers 404.
interface LocalPhone {
  open: boolean;
  url?: string;
  lan_address?: string;
  pair_url?: string;
  expires_at?: number;
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
  const [localPhone, setLocalPhone] = useState<LocalPhone>();
  const origin = typeof window !== "undefined" ? window.location.origin : "";
  useEffect(() => {
    void api
      .request<PhoneAddresses>("/phone/addresses")
      .then(setAddresses)
      .catch((e) => setError(String(e instanceof Error ? e.message : e)));
    void api
      .request<LocalPhone>("/local/phone")
      .then(setLocalPhone)
      .catch(() => undefined);
  }, []);
  const choices = withLocalPhone(phoneChoices(addresses, origin), localPhone);
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
      if (chosen.kind === "lan" && localPhone) {
        await openLocalPhone();
        return;
      }
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

  // One click: the same runtime starts listening on the Wi-Fi address, and
  // the answer carries a one-time pairing link for the QR code.
  async function openLocalPhone() {
    const out = await api.request<LocalPhone>("/local/phone", { method: "POST" });
    setLocalPhone(out);
    setChosen({ kind: "lan", url: out.url, available: true });
    if (out.pair_url && out.expires_at) setMinted({ url: out.pair_url, expiresAt: out.expires_at });
  }
  async function lanOpen() {
    setBusy(true);
    setMinted(undefined);
    try {
      await openLocalPhone();
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
    } finally {
      setBusy(false);
    }
  }
  async function lanStop() {
    try {
      setLocalPhone(await api.request<LocalPhone>("/local/phone", { method: "DELETE" }));
      setMinted(undefined);
      setChosen(undefined);
      onNotice(t("phone.lanStopped"));
    } catch (e) {
      onNotice(String(e instanceof Error ? e.message : e), true);
    }
  }

  const noneAvailable = !!addresses && !choices.some((row) => row.available) && !localPhone?.lan_address;
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
              {!row.available && row.reason && !(row.kind === "lan" && localPhone) && <span className="phone-choice-why">{t(`phone.why.${row.reason}`)}</span>}
              {row.kind === "lan" && localPhone && !localPhone.open && localPhone.lan_address && (
                <span className="phone-choice-why">
                  {t("phone.lanClosed")}{" "}
                  <button type="button" className="b ok" id="phone-lan-open" disabled={busy} onClick={() => void lanOpen()}>
                    {t("phone.lanOpen")}
                  </button>
                </span>
              )}
              {row.kind === "lan" && localPhone?.open && (
                <span className="phone-choice-why" id="phone-lan-warning">
                  {t("phone.lanWarning")}{" "}
                  <button type="button" className="b" id="phone-lan-stop" onClick={() => void lanStop()}>
                    {t("phone.lanStop")}
                  </button>
                </span>
              )}
            </span>
          </label>
        ))}
      </div>
      {noneAvailable && (
        <p className="subhint" id="phone-none" role="status">
          {t("phone.none")}
        </p>
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

// withLocalPhone marks the Wi-Fi choice available once the private runtime is
// listening there, at the address it actually opened.
export function withLocalPhone(choices: PhoneOption[], local: LocalPhone | undefined): PhoneOption[] {
  if (!local?.open || !local.url) return choices;
  return choices.map((row) => (row.kind === "lan" ? { kind: "lan", url: local.url, available: true } : row));
}
