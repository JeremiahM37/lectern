import { useEffect, useMemo, useState } from "react";
import { withToken, type createDeckApi } from "../api";
import type { LiveView, Media as MediaRow, Target } from "../types";
import { Live, liveAddress } from "./Live";
import "./media.css";

type Api = ReturnType<typeof createDeckApi>;
const LOOPBACK = new Set(["localhost", "127.0.0.1", "0.0.0.0", "[::1]"]);

export const mediaContent = (row: MediaRow, download = false) =>
  withToken(`/api/media/${row.id}/content${download ? "?download=1" : ""}`);

function ago(at: number) {
  const seconds = Math.max(0, Date.now() / 1000 - at);
  if (seconds < 60) return "just now";
  if (seconds < 3600) return Math.floor(seconds / 60) + "m ago";
  if (seconds < 86400) return Math.floor(seconds / 3600) + "h ago";
  return Math.floor(seconds / 86400) + "d ago";
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
  const [text, setText] = useState<string>();
  useEffect(() => {
    const abort = new AbortController();
    fetch(mediaContent(row), { signal: abort.signal, headers: { Range: "bytes=0-65535" } })
      .then((response) => response.text())
      .then(setText)
      .catch(() => {
        if (!abort.signal.aborted) setText("Could not load this file.");
      });
    return () => abort.abort();
  }, [row.id]);
  return (
    <pre className="media-text">
      {text ?? "Loading…"}
      {row.size > 65536 && text !== undefined ? "\n… truncated; download for the rest" : ""}
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
            Posted as {row.url}, which only that machine can open. It is exposed above until you
            stop it.
          </small>
        </div>
      );
    }
    const target = reachable(row.url);
    return (
      <div className="media-link">
        {port > 0 && onExpose && (
          <button className="b ok media-expose" onClick={() => onExpose(port)}>
            Expose localhost:{port} so this device can open it
          </button>
        )}
        <a href={target.href} target="_blank" rel="noopener noreferrer">
          {target.href}
        </a>
        {target.rewritten && (
          <small>
            Posted as {row.url}. It opens on this server's address, so the site has
            to listen on 0.0.0.0 rather than on loopback alone.
          </small>
        )}
        {target.embeddable ? (
          <button className="b" aria-expanded={live} onClick={() => setLive(!live)}>
            {live ? "Hide live preview" : "Show live preview"}
          </button>
        ) : (
          <small>This page is secure and the site is not, so it opens in a new tab.</small>
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
  return <p className="media-file">{row.name} cannot be previewed here; download it to open it.</p>;
}

// The file is fetched only once the disclosure opens, so a feed of logs costs
// no requests until someone asks for one.
function TextPeek({ row }: { row: MediaRow }) {
  const [open, setOpen] = useState(false);
  return (
    <details className="media-peek" onToggle={(event) => setOpen(event.currentTarget.open)}>
      <summary>Preview {row.name}</summary>
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
  const posters = useMemo(() => {
    const seen = new Map<number, string>();
    for (const row of rows)
      if (row.session_id != null && !seen.has(row.session_id))
        seen.set(row.session_id, row.session_name || "Session " + row.session_id);
    return [...seen];
  }, [rows]);
  const shown = sessionFilter == null ? rows : rows.filter((row) => row.session_id === sessionFilter);
  return (
    <section id="media">
      <div className="pane-head">
        <div>
          <h2>Media</h2>
          <p className="sub">
            {rows.length
              ? `${rows.length} posted · recordings, files and live sites your agents want you to see.`
              : "Nothing posted yet."}
          </p>
        </div>
        <label className="media-filter" hidden={!posters.length}>
          From
          <select
            aria-label="Show media from"
            value={sessionFilter ?? ""}
            onChange={(event) => onFilter(event.target.value ? Number(event.target.value) : null)}
          >
            <option value="">Every session</option>
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
            Agents post here with the <code>post_media</code> tool — ask one to “record a demo and
            post it”. From a shell, inside a session or not:
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
                    {row.session_name || "Session " + row.session_id}
                  </button>
                ) : (
                  <span className="chip">no session</span>
                )}
                <span className="chip">{row.kind === "link" ? "link" : bytes(row.size)}</span>
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
                  Download
                </a>
              )}
              <button
                className="b danger"
                aria-label={"Delete " + (row.title || row.name)}
                onClick={() => {
                  if (!confirm("Delete this post? The file is removed from Lectern.")) return;
                  void api
                    .request<null>(`/media/${row.id}`, { method: "DELETE" })
                    .then(onChanged)
                    .catch((error) => onNotice(String(error), true));
                }}
              >
                Delete
              </button>
            </footer>
          </article>
        ))}
      </div>
    </section>
  );
}
