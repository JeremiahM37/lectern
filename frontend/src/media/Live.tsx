import { useState } from "react";
import type { createDeckApi } from "../api";
import type { LiveView, Target } from "../types";
import { t, useLocale } from "../i18n";

type Api = ReturnType<typeof createDeckApi>;

// A forward is plain TCP on its own port, so it is always http on the host
// Lectern itself was reached at. A secure page may link to that but may not
// frame it, which is the one case where the view opens in its own tab instead.
export function liveAddress(view: LiveView, here: Location = location) {
  const host = here.hostname.includes(":") ? `[${here.hostname}]` : here.hostname;
  return { base: `http://${host}:${view.listen_port}`, embeddable: here.protocol !== "https:" };
}

function remaining(view: LiveView) {
  const minutes = Math.max(0, Math.round((view.expires_at - Date.now() / 1000) / 60));
  return minutes >= 90 ? t("board.media.hoursLeft", { n: Math.round(minutes / 60) }) : t("board.media.minutesLeft", { n: minutes });
}

function LiveCard({
  view,
  api,
  onChanged,
  onNotice,
}: {
  view: LiveView;
  api: Api;
  onChanged: () => void;
  onNotice: (text: string, error?: boolean) => void;
}) {
  useLocale();
  const [control, setControl] = useState(false),
    [show, setShow] = useState(view.kind === "desktop"),
    [address, setAddress] = useState(""),
    [busy, setBusy] = useState(false);
  const { base, embeddable } = liveAddress(view);
  const display = view.detail?.display || "";
  // noVNC reads these from the query: connect at once, fit the frame, and only
  // pass input through when the operator has asked to take over.
  const desktopURL = `${base}/vnc.html?autoconnect=1&reconnect=1&resize=scale&path=websockify&view_only=${control ? 0 : 1}`;
  const target = view.kind === "desktop" ? desktopURL : base + "/";
  return (
    <article className="media-card live-card" data-live-id={view.id} data-kind={view.kind}>
      <header>
        <h3>{view.title}</h3>
        <div className="media-meta">
          <span className="chip live-chip" title={t("board.media.exposedTitle")}>
            {t("board.media.exposed")}
          </span>
          <span className="chip">{view.target_name}</span>
          <span className="chip">
            {view.kind === "desktop" ? t("board.media.desktopDisplay", { display }) : "localhost:" + view.port}
          </span>
          <span className="media-when">{remaining(view)}</span>
        </div>
      </header>
      {view.kind === "desktop" && (
        <p className="media-note">
          {t("board.media.displayBefore")}<code>DISPLAY={display}</code>{t("board.media.displayAfter")}{" "}
          <button
            className="live-copy"
            onClick={() =>
              void navigator.clipboard
                ?.writeText(`DISPLAY=${display}`)
                .then(() => onNotice(t("board.media.copied", { text: `DISPLAY=${display}` })))
            }
          >
            {t("board.media.copy")}
          </button>
          {view.detail?.browser_error ? " · " + view.detail.browser_error : ""}
        </p>
      )}
      {embeddable ? (
        show && (
          <iframe
            className={view.kind === "desktop" ? "media-frame live-desktop" : "media-frame"}
            title={view.title}
            // Remounting is how the control switch takes effect: noVNC decides
            // whether to pass input through when it connects.
            key={target}
            src={target}
            allow="clipboard-read; clipboard-write"
          />
        )
      ) : (
        <small className="media-file">
          {t("board.media.insecurePort")}
        </small>
      )}
      {view.kind === "desktop" && (
        <form
          className="live-address"
          onSubmit={(event) => {
            event.preventDefault();
            if (!address.trim()) return;
            setBusy(true);
            void api
              .request(`/live/${view.id}/browser`, { method: "POST", body: { url: address.trim() } })
              .then(() => setAddress(""))
              .catch((error) => onNotice(String(error), true))
              .finally(() => setBusy(false));
          }}
        >
          <input
            aria-label={t("board.media.openAddressLabel")}
            placeholder={t("board.media.openAddressPlaceholder")}
            value={address}
            onChange={(event) => setAddress(event.target.value)}
          />
          <button className="b" disabled={busy || !address.trim()}>
            {t("board.media.open")}
          </button>
        </form>
      )}
      <footer>
        {view.kind === "desktop" && embeddable && (
          <button className="b" aria-pressed={control} onClick={() => setControl(!control)}>
            {control ? t("board.media.watchOnly") : t("board.media.takeControl")}
          </button>
        )}
        {view.kind === "port" && embeddable && (
          <button className="b" aria-expanded={show} onClick={() => setShow(!show)}>
            {show ? t("board.media.hidePreview") : t("board.media.showPreview")}
          </button>
        )}
        <a className="b" href={view.kind === "desktop" ? desktopURL.replace("view_only=1", "view_only=0") : target} target="_blank" rel="noopener noreferrer">
          {t("board.media.openNewTab")}
        </a>
        <button
          className="b danger"
          aria-label={t("board.media.stopLabel", { title: view.title })}
          onClick={() =>
            void api
              .request<null>(`/live/${view.id}`, { method: "DELETE" })
              .then(onChanged)
              .catch((error) => onNotice(String(error), true))
          }
        >
          {t("board.media.stop")}
        </button>
      </footer>
    </article>
  );
}

export function Live({
  api,
  views,
  targets,
  onChanged,
  onNotice,
}: {
  api: Api;
  views: LiveView[];
  targets: Target[];
  onChanged: () => void;
  onNotice: (text: string, error?: boolean) => void;
}) {
  useLocale();
  const [machine, setMachine] = useState(""),
    [port, setPort] = useState(""),
    [busy, setBusy] = useState("");
  const open = (kind: "desktops" | "ports", body: Record<string, number | string>) => {
    setBusy(kind);
    void api
      .request<LiveView>("/live/" + kind, {
        method: "POST",
        body: { ...body, ...(machine ? { target_id: Number(machine) } : {}) },
      })
      .then(() => {
        setPort("");
        onChanged();
      })
      .catch((error) => onNotice(String(error), true))
      .finally(() => setBusy(""));
  };
  return (
    <section id="live" aria-label={t("board.media.liveViews")}>
      <div className="live-bar">
        <label>
          {t("board.media.on")}
          <select aria-label={t("board.media.machine")} value={machine} onChange={(event) => setMachine(event.target.value)}>
            <option value="">{t("board.media.defaultMachine")}</option>
            {targets.map((target) => (
              <option key={target.id} value={target.id}>
                {target.name}
              </option>
            ))}
          </select>
        </label>
        <button
          className="b"
          id="live-desktop"
          aria-label={t("board.media.liveDesktop")}
          disabled={!!busy}
          title={t("board.media.liveDesktopTitle")}
          onClick={() => open("desktops", { title: "Live desktop" })}
        >
          {busy === "desktops" ? (
            t("board.media.starting")
          ) : (
            <>
              ＋<span className="wide-only">{t("board.media.addLiveWide")}</span>{t("board.media.addDesktop")}
            </>
          )}
        </button>
        <form
          className="live-expose"
          onSubmit={(event) => {
            event.preventDefault();
            if (Number(port) > 0) open("ports", { port: Number(port) });
          }}
        >
          <input
            aria-label={t("board.media.portLabel")}
            inputMode="numeric"
            placeholder={t("board.media.portPlaceholder")}
            title={t("board.media.portTitle")}
            value={port}
            onChange={(event) => setPort(event.target.value.replace(/\D/g, "").slice(0, 5))}
          />
          <button className="b" id="live-expose" disabled={!!busy || !(Number(port) > 0)}>
            {busy === "ports" ? t("board.media.exposing") : t("board.media.expose")}
          </button>
        </form>
      </div>
      {views.length > 0 && (
        <div className="media-feed live-feed">
          {views.map((view) => (
            <LiveCard key={view.id} view={view} api={api} onChanged={onChanged} onNotice={onNotice} />
          ))}
        </div>
      )}
    </section>
  );
}
