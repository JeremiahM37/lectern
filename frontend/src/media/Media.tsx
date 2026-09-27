import { useEffect, useMemo, useState } from "react";
import { withToken, type createDeckApi } from "../api";
import type { LiveView, Media as MediaRow, Target } from "../types";
import { Live, liveAddress } from "./Live";
import { t, useLocale } from "../i18n";
import "./media.css";

type Api = ReturnType<typeof createDeckApi>;
const LOOPBACK = new Set(["localhost", "127.0.0.1", "0.0.0.0", "[::1]"]);

export const mediaContent = (row: MediaRow, download = false) =>
  withToken(`/api/media/${row.id}/content${download ? "?download=1" : ""}`);

function ago(at: number) {
  const seconds = Math.max(0, Date.now() / 1000 - at);
  if (seconds < 60) return t("board.claims.justNow");
  if (seconds < 3600) return t("board.claims.minutesAgo", { n: Math.floor(seconds / 60) });
  if (seconds < 86400) return t("board.claims.hoursAgo", { n: Math.floor(seconds / 3600) });
  return t("board.media.daysAgo", { n: Math.floor(seconds / 86400) });
}

function bytes(size: number) {
  if (size < 1024) return size + " B";
  if (size < 1 << 20) return (size / 1024).toFixed(1) + " KiB";
  if (size < 1 << 30) return (size / (1 << 20)).toFixed(1) + " MiB";
  return (size / (1 << 30)).toFixed(2) + " GiB";
}

// An agent posts the address it sees — usually loopback on the server. From the
// operator's laptop that address means the laptop, so it is rewritten to the
// host Lectern itself was reached on, which is the same machine.
export function reachable(url: string, here: Location = location) {
  try {
    const parsed = new URL(url);
    const rewritten = LOOPBACK.has(parsed.hostname) && !LOOPBACK.has(here.hostname);
    if (rewritten) parsed.hostname = here.hostname;
    return {
      href: parsed.toString(),
      rewritten,
      // A secure page may link to http but may not frame it.
      embeddable: !(here.protocol === "https:" && parsed.protocol === "http:"),
    };
  } catch {
    return { href: url, rewritten: false, embeddable: false };
  }
}

function TextPreview({ row }: { row: MediaRow }) {
  useLocale();
  const [text, setText] = useState<string>();
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const abort = new AbortController();
    fetch(mediaContent(row), { signal: abort.signal, headers: { Range: "bytes=0-65535" } })
      .then((response) => response.text())
      .then(setText)
      .catch(() => {
        if (!abort.signal.aborted) setFailed(true);
      });
    return () => abort.abort();
  }, [row.id]);
  return (
    <pre className="media-text">
      {text ?? (failed ? t("board.media.loadFailed") : t("board.media.loading"))}
      {row.size > 65536 && (text !== undefined || failed) ? t("board.media.truncated") : ""}
    </pre>
  );
}

// loopbackPort is the port of a link only the posting machine can open.
function loopbackPort(address: string) {
  try {
    const parsed = new URL(address);
    if (!LOOPBACK.has(parsed.hostname)) return 0;
    return Number(parsed.port) || (parsed.protocol === "https:" ? 443 : 80);
  } catch {
    return 0;
  }
}

function Body({
  row,
  exposed,
  onExpose,
}: {
  row: MediaRow;
  exposed?: LiveView;
  onExpose?: (port: number) => void;
}) {
  useLocale();
  const [live, setLive] = useState(false);
  if (row.kind === "link") {
    const port = loopbackPort(row.url);
    // Once the port is forwarded, the link that works is the forwarded one: the
    // posted address only ever meant something on the machine that posted it.
    if (exposed) {
      const parsed = new URL(row.url);
      const href = liveAddress(exposed).base + parsed.pathname + parsed.search + parsed.hash;
      return (
        <div className="media-link">
          <a href={href} target="_blank" rel="noopener noreferrer">
            {href}
          </a>
          <small>
            {t("board.media.postedExposed", { url: row.url })}
          </small>
        </div>
      );
    }
    const target = reachable(row.url);
    return (
      <div className="media-link">
        {port > 0 && onExpose && (
          <button className="b ok media-expose" onClick={() => onExpose(port)}>
            {t("board.media.exposePort", { port })}
          </button>
        )}
        <a href={target.href} target="_blank" rel="noopener noreferrer">
          {target.href}
        </a>
        {target.rewritten && (
          <small>
            {t("board.media.postedRewritten", { url: row.url })}
          </small>
        )}
        {target.embeddable ? (
          <button className="b" aria-expanded={live} onClick={() => setLive(!live)}>
            {live ? t("board.media.hideLivePreview") : t("board.media.showLivePreview")}
          </button>
        ) : (
          <small>{t("board.media.insecureSite")}</small>
        )}
        {live && target.embeddable && (
          <iframe className="media-frame" title={row.title} src={target.href} />
        )}
      </div>
    );
  }
  const src = mediaContent(row);
  if (row.mime.startsWith("video/"))
    // The fragment makes the browser paint a first frame instead of a black box.
    return <video className="media-video" controls preload="metadata" src={src + "#t=0.1"} />;
  if (row.mime.startsWith("image/"))
    return (
      <a href={src} target="_blank" rel="noopener">
        <img className="media-image" loading="lazy" src={src} alt={row.title} />
      </a>
    );
  if (row.mime.startsWith("audio/")) return <audio controls preload="metadata" src={src} />;
  if (row.mime.startsWith("text/html") || row.mime === "application/pdf")
    return (
      <iframe
        className="media-frame"
        title={row.title}
        src={src}
        sandbox={row.mime === "application/pdf" ? undefined : "allow-scripts"}
      />
    );
  if (row.mime.startsWith("text/") || /json|xml|yaml|javascript/.test(row.mime))
    return <TextPeek row={row} />;
  return <p className="media-file">{t("board.media.noPreview", { name: row.name })}</p>;
}

// The file is fetched only once the disclosure opens, so a feed of logs costs
// no requests until someone asks for one.
function TextPeek({ row }: { row: MediaRow }) {
  useLocale();
  const [open, setOpen] = useState(false);
  return (
    <details className="media-peek" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>{t("board.media.previewFile", { name: row.name })}</summary>
      {open && <TextPreview row={row} />}
    </details>
  );
}

export function Media({
  api,
  rows,
  live,
  liveEnabled,
  targets,
  sessionFilter,
  onFilter,
  onChanged,
  onNotice,
}: {
  api: Api;
  rows: MediaRow[];
  live: LiveView[];
  // Off unless the operator turned it on; nothing that needs it is offered then.
  liveEnabled: boolean;
  targets: Target[];
  sessionFilter: number | null;
  onFilter: (sessionID: number | null) => void;
  onChanged: () => void;
  onNotice: (text: string, error?: boolean) => void;
}) {
  const locale = useLocale();
  const posters = useMemo(() => {
    const seen = new Map<number, string>();
    for (const row of rows)
      if (row.session_id != null && !seen.has(row.session_id))
        seen.set(row.session_id, row.session_name || t("board.media.session", { id: row.session_id }));
    return [...seen];
  }, [rows, locale]);
  const shown = sessionFilter == null ? rows : rows.filter((row) => row.session_id === sessionFilter);
  return (
    <section id="media">
      <div className="pane-head">
        <div>
          <h2>{t("board.media.title")}</h2>
          <p className="sub">
            {rows.length
              ? t("board.media.summary", { n: rows.length })
              : t("board.media.nothingPosted")}
          </p>
        </div>
        <label className="media-filter" hidden={!posters.length}>
          {t("board.media.from")}
          <select
            aria-label={t("board.media.showFrom")}
            value={sessionFilter ?? ""}
            onChange={(event) => onFilter(event.target.value ? Number(event.target.value) : null)}
          >
            <option value="">{t("board.media.everySession")}</option>
            {posters.map(([id, name]) => (
              <option key={id} value={id}>
                {name}
              </option>
            ))}
          </select>
        </label>
      </div>
      {liveEnabled && (
        <Live api={api} views={live} targets={targets} onChanged={onChanged} onNotice={onNotice} />
      )}
      {!rows.length && (
        <div className="media-empty">
          <p>
            {t("board.media.emptyBefore")}<code>post_media</code>{t("board.media.emptyAfter")}
          </p>
          <pre>lectern post ./demo.mp4 --title "Checkout flow passing"{"\n"}lectern post http://127.0.0.1:5173 --title "Dev server"</pre>
        </div>
      )}
      <div className="media-feed">
        {shown.map((row) => (
          <article className="media-card" key={row.id} data-media-id={row.id} data-kind={row.kind}>
            <header>
              <h3>{row.title || row.name || row.url}</h3>
              <div className="media-meta">
                {row.session_id != null ? (
                  <button className="chip" onClick={() => onFilter(row.session_id)}>
                    {row.session_name || t("board.media.session", { id: row.session_id })}
                  </button>
                ) : (
                  <span className="chip">{t("board.media.noSession")}</span>
                )}
                <span className="chip">{row.kind === "link" ? t("board.media.link") : bytes(row.size)}</span>
                <span className="media-when">{ago(row.created_at)}</span>
              </div>
            </header>
            {row.note && <p className="media-note">{row.note}</p>}
            <Body
              row={row}
              exposed={live.find(
                (view) =>
                  view.kind === "port" &&
                  view.port === loopbackPort(row.url) &&
                  view.session_id === row.session_id,
              )}
              onExpose={!liveEnabled ? undefined : (port) =>
                void api
                  .request("/live/ports", {
                    method: "POST",
                    body: {
                      port,
                      title: row.title || `localhost:${port}`,
                      ...(row.session_id != null ? { session_id: row.session_id } : {}),
                    },
                  })
                  .then(onChanged)
                  .catch((error) => onNotice(String(error), true))
              }
            />
            <footer>
              {row.kind === "file" && (
                <a className="b" href={mediaContent(row, true)}>
                  {t("board.media.download")}
                </a>
              )}
              <button
                className="b danger"
                aria-label={t("board.media.deleteLabel", { title: row.title || row.name })}
                onClick={() => {
                  if (!confirm(t("board.media.deleteConfirm"))) return;
                  void api
                    .request<null>(`/media/${row.id}`, { method: "DELETE" })
                    .then(onChanged)
                    .catch((error) => onNotice(String(error), true));
                }}
              >
                {t("board.media.delete")}
              </button>
            </footer>
          </article>
        ))}
      </div>
    </section>
  );
}
