import { errorMessage } from "./model";
import type { Snippet } from "./snippets";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import {
  copyClipboard,
  downloadBlob,
  json,
  quote,
  request,
  themes,
  type FileListing,
  type History,
  type Prefs,
  type TerminalInfo,
} from "./model";
export function Dialog({
  id,
  title,
  children,
  onClose,
  actions,
}: {
  id: string;
  title: string;
  children: ReactNode;
  onClose: () => void;
  actions?: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    return () => ref.current?.close();
  }, []);
  return (
    <dialog
      id={id}
      ref={ref}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <div className="dialog-head">
        <h2>{title}</h2>
        {actions}
        <button data-close onClick={onClose}>
          Close
        </button>
      </div>
      {children}
    </dialog>
  );
}
export function Appearance({
  prefs,
  minFont = 10,
  onPrefs,
  onClose,
}: {
  prefs: Prefs;
  minFont?: number;
  onPrefs: (prefs: Prefs) => void;
  onClose: () => void;
}) {
  return (
    <Dialog id="settings-dialog" title="Terminal appearance" onClose={onClose}>
      <label>
        Font size
        <input
          id="font-size"
          type="number"
          min={minFont}
          max={30}
          value={prefs.fontSize}
          onChange={(event) =>
            onPrefs({
              ...prefs,
              fontSize: Math.max(
                minFont,
                Math.min(30, Number(event.target.value) || 15),
              ),
            })
          }
        />
      </label>
      <label>
        Line spacing
        <select
          id="line-height"
          value={prefs.lineHeight}
          onChange={(event) =>
            onPrefs({ ...prefs, lineHeight: Number(event.target.value) })
          }
        >
          {[
            [1, "Compact"],
            [1.15, "Comfortable"],
            [1.3, "Spacious"],
          ].map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <label>
        Theme
        <select
          id="theme"
          value={prefs.theme}
          onChange={(event) => onPrefs({ ...prefs, theme: event.target.value })}
        >
          {Object.keys(themes).map((theme) => (
            <option key={theme} value={theme}>
              {theme[0]?.toUpperCase()}
              {theme.slice(1)}
            </option>
          ))}
        </select>
      </label>
      <p>
        Saved on this device. Keyboard: Ctrl+Shift+F history, Ctrl+Shift+C copy,
        Ctrl+Shift+V paste.
      </p>
    </Dialog>
  );
}
export function HistoryDialog({
  url,
  shell,
  onClose,
}: {
  url: string;
  shell: boolean;
  onClose: () => void;
}) {
  const [data, setData] = useState<History>();
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const [refresh, setRefresh] = useState(0);
  const [error, setError] = useState("");
  const container = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void json<History>(url.replace("/term/", "/api/term/") + "/history", {
      signal: controller.signal,
    })
      .then(setData)
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      });
    return () => controller.abort();
  }, [url, refresh]);
  const ranges = useMemo(() => {
    const text = data?.text || "",
      hay = text.toLowerCase(),
      needle = query.toLowerCase(),
      result: number[] = [];
    if (!needle) return result;
    let from = 0,
      at;
    while ((at = hay.indexOf(needle, from)) !== -1 && result.length < 2000) {
      result.push(at);
      from = at + query.length;
    }
    return result;
  }, [data, query]);
  useEffect(() => {
    container.current
      ?.querySelector("mark.current")
      ?.scrollIntoView({ block: "center" });
  }, [index, ranges]);
  function step(delta: number) {
    if (ranges.length)
      setIndex((old) => (old + delta + ranges.length) % ranges.length);
  }
  let from = 0;
  const highlighted: ReactNode[] = [];
  for (const [i, at] of ranges.entries()) {
    highlighted.push(
      (data?.text || "").slice(from, at),
      <mark key={at} className={i === index ? "current" : ""}>
        {data?.text.slice(at, at + query.length)}
      </mark>,
    );
    from = at + query.length;
  }
  highlighted.push(data?.text.slice(from) || "");
  return (
    <Dialog
      id="history-dialog"
      title="Session history"
      onClose={onClose}
      actions={
        <>
          <button
            id="history-refresh"
            onClick={() => setRefresh((old) => old + 1)}
          >
            Refresh
          </button>
          <button
            id="history-save"
            disabled={!data}
            onClick={() =>
              downloadBlob(
                new Blob([data?.text || ""], { type: "text/plain" }),
                "terminal-history.txt",
              )
            }
          >
            Download
          </button>
        </>
      }
    >
      <div className="history-controls">
        <input
          id="history-query"
          autoFocus
          placeholder="Search full tmux history"
          aria-label="Search full history"
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setIndex(0);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") step(event.shiftKey ? -1 : 1);
          }}
        />
        <button id="history-prev" onClick={() => step(-1)}>
          ↑
        </button>
        <button id="history-next" onClick={() => step(1)}>
          ↓
        </button>
        <span id="history-count">
          {query
            ? ranges.length
              ? `${index + 1} / ${ranges.length}${ranges.length === 2000 ? "+" : ""}`
              : "No matches"
            : ""}
        </span>
      </div>
      <p id="history-note">
        {error ||
          (!data
            ? "Loading history…"
            : `Snapshot of ${shell ? "companion shell" : "agent"} tmux history · up to ${data.limit_lines.toLocaleString()} retained lines${data.truncated ? " · limited to final 8 MiB" : ""}`)}
      </p>
      <pre ref={container} id="history-text">
        {highlighted}
      </pre>
    </Dialog>
  );
}
interface PDFViewport {
  width: number;
  height: number;
}
interface PDFPage {
  getViewport: (options: { scale: number }) => PDFViewport;
  render: (options: {
    canvasContext: CanvasRenderingContext2D;
    viewport: PDFViewport;
    transform: number[];
  }) => { promise: Promise<void>; cancel: () => void };
}
interface PDFDocument {
  numPages: number;
  getPage: (number: number) => Promise<PDFPage>;
  destroy: () => Promise<void>;
}
interface PDFModule {
  GlobalWorkerOptions: { workerSrc: string };
  getDocument: (options: {
    data: Uint8Array;
    standardFontDataUrl: string;
    cMapUrl: string;
    cMapPacked: boolean;
    isEvalSupported: boolean;
  }) => { promise: Promise<PDFDocument>; destroy: () => Promise<void> };
}
function PDFPreview({ blob }: { blob: Blob }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [pdf, setPDF] = useState<PDFDocument>();
  const [page, setPage] = useState(1);
  const [busy, setBusy] = useState(true);
  const [error, setError] = useState("");
  useEffect(() => {
    let closed = false,
      task: ReturnType<PDFModule["getDocument"]> | undefined;
    void (async () => {
      const vendor = "/vendor/pdf.mjs";
      const module = (await import(/* @vite-ignore */ vendor)) as PDFModule;
      if (closed) return;
      module.GlobalWorkerOptions.workerSrc = "/vendor/pdf.worker.mjs";
      task = module.getDocument({
        data: new Uint8Array(await blob.arrayBuffer()),
        standardFontDataUrl: "/vendor/standard_fonts/",
        cMapUrl: "/vendor/cmaps/",
        cMapPacked: true,
        isEvalSupported: false,
      });
      const document = await task.promise;
      if (closed) {
        await document.destroy();
        return;
      }
      setPDF(document);
    })().catch((error) => {
      if (!closed) {
        setError(errorMessage(error));
        setBusy(false);
      }
    });
    return () => {
      closed = true;
      void task?.destroy();
    };
  }, [blob]);
  useEffect(() => {
    if (!pdf || !canvas.current) return;
    let closed = false,
      render: ReturnType<PDFPage["render"]> | undefined;
    setBusy(true);
    void (async () => {
      const source = await pdf.getPage(page),
        element = canvas.current;
      if (closed || !element) return;
      const original = source.getViewport({ scale: 1 }),
        width = Math.max(
          280,
          Math.min(1000, element.parentElement?.clientWidth || 800),
        ),
        viewport = source.getViewport({ scale: width / original.width }),
        ratio = Math.min(devicePixelRatio || 1, 2);
      element.width = Math.floor(viewport.width * ratio);
      element.height = Math.floor(viewport.height * ratio);
      element.style.width = viewport.width + "px";
      const context = element.getContext("2d");
      if (!context) throw new Error("Canvas unavailable");
      render = source.render({
        canvasContext: context,
        viewport,
        transform: [ratio, 0, 0, ratio, 0, 0],
      });
      await render.promise;
    })()
      .catch((error) => {
        if (!closed) setError(errorMessage(error));
      })
      .finally(() => {
        if (!closed) setBusy(false);
      });
    return () => {
      closed = true;
      render?.cancel();
    };
  }, [pdf, page]);
  return (
    <>
      <div className="pdf-controls">
        <button disabled={busy || page <= 1} onClick={() => setPage(page - 1)}>
          Previous page
        </button>
        <span id="pdf-page">
          {error ||
            (!busy && pdf ? `Page ${page} of ${pdf.numPages}` : "Loading PDF…")}
        </span>
        <button
          disabled={busy || !pdf || page >= pdf.numPages}
          onClick={() => setPage(page + 1)}
        >
          Next page
        </button>
      </div>
      <canvas ref={canvas} aria-label="PDF page" />
    </>
  );
}
interface Preview {
  path: string;
  blob: Blob;
  url: string;
  kind: "image" | "pdf" | "text" | "binary";
  text: string;
}
export function WorkspaceFiles({
  base,
  info,
  onClose,
  onInsert,
  initialPath,
}: {
  base: string;
  info: TerminalInfo;
  onClose: () => void;
  onInsert: (text: string) => void;
  initialPath?: string;
}) {
  const [listing, setListing] = useState<FileListing>();
  const [dir, setDir] = useState(".");
  const [refresh, setRefresh] = useState(0);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<Preview>();
  const url = useRef("");
  const generation = useRef(0);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void json<FileListing>(base + "/files?path=" + encodeURIComponent(dir), {
      signal: controller.signal,
    })
      .then(setListing)
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      });
    return () => controller.abort();
  }, [base, dir, refresh]);
  useEffect(
    () => () => {
      generation.current++;
      URL.revokeObjectURL(url.current);
    },
    [],
  );
  async function open(path: string) {
    const current = ++generation.current;
    try {
      const response = await request(
          base + "/file?path=" + encodeURIComponent(path),
        ),
        blob = await response.blob();
      if (current !== generation.current) return;
      const ext = path.split(".").pop()?.toLowerCase() || "";
      const images: Record<string, string> = {
        png: "image/png",
        jpg: "image/jpeg",
        jpeg: "image/jpeg",
        gif: "image/gif",
        webp: "image/webp",
        avif: "image/avif",
      };
      const kind = images[ext]
        ? "image"
        : ext === "pdf"
          ? "pdf"
          : new Uint8Array(await blob.slice(0, 8192).arrayBuffer()).includes(0)
            ? "binary"
            : "text";
      const text =
        kind === "text"
          ? (blob.size > 1024 * 1024
              ? "Preview limited to first 1 MiB. Download for the full file.\n\n"
              : "") + (await blob.slice(0, 1024 * 1024).text())
          : "";
      if (current !== generation.current) return;
      URL.revokeObjectURL(url.current);
      url.current = URL.createObjectURL(
        new Blob([blob], {
          type:
            images[ext] ||
            (ext === "pdf" ? "application/pdf" : "application/octet-stream"),
        }),
      );
      setPreview({ path, blob, url: url.current, kind, text });
    } catch (error) {
      if (current === generation.current) setError(errorMessage(error));
    }
  }
  useEffect(() => {
    if (initialPath) void open(initialPath);
  }, [initialPath]);
  function insert(path: string) {
    try {
      onInsert(quote(info.workdir + "/" + path) + " ");
      onClose();
    } catch (error) {
      setError(errorMessage(error));
    }
  }
  return (
    <>
      <Dialog
        id="files-dialog"
        title="Workspace files"
        onClose={onClose}
        actions={
          <button
            id="files-refresh"
            onClick={() => setRefresh((old) => old + 1)}
          >
            Refresh
          </button>
        }
      >
        <div className="file-location">
          <button
            id="files-up"
            disabled={dir === "."}
            onClick={() => setDir(dir.split("/").slice(0, -1).join("/") || ".")}
          >
            ↑ Parent
          </button>
          <span id="files-path">{listing?.path || dir}</span>
        </div>
        {error && <p role="alert">{error}</p>}
        <div id="file-list">
          {listing?.entries.map((file) => (
            <div className="file-row" key={file.path}>
              <button
                onClick={() =>
                  file.directory ? setDir(file.path) : void open(file.path)
                }
              >
                {file.directory ? "▸ " : ""}
                {file.name}
              </button>
              <span>
                {file.directory
                  ? "folder"
                  : `${Math.ceil(file.size / 1024)} KiB`}
              </span>
              {!file.directory && (
                <button onClick={() => insert(file.path)}>Insert path</button>
              )}
            </div>
          ))}
          {listing && !listing.entries.length && "This folder is empty."}
        </div>
        <p>
          Up to 500 entries per folder · previews and downloads up to 25 MiB
        </p>
      </Dialog>
      {preview && (
        <Dialog
          id="preview-dialog"
          title={preview.path.split("/").pop() || "Preview"}
          onClose={() => {
            setPreview(undefined);
            URL.revokeObjectURL(url.current);
            url.current = "";
          }}
          actions={
            <>
              <button id="preview-insert" onClick={() => insert(preview.path)}>
                Insert path
              </button>
              <a
                id="preview-download"
                href={preview.url}
                download={preview.path.split("/").pop()}
              >
                Download
              </a>
            </>
          }
        >
          <div id="preview-body">
            {preview.kind === "image" ? (
              <img src={preview.url} alt={preview.path} />
            ) : preview.kind === "pdf" ? (
              <PDFPreview blob={preview.blob} />
            ) : preview.kind === "text" ? (
              <pre>{preview.text}</pre>
            ) : (
              "Binary file. Use Download to open it on your device."
            )}
          </div>
        </Dialog>
      )}
    </>
  );
}
export function Desktop({
  info,
  onClose,
  onNotice,
}: {
  info: TerminalInfo;
  onClose: () => void;
  onNotice: (text: string) => void;
}) {
  return (
    <Dialog id="desktop-dialog" title="Open in your terminal" onClose={onClose}>
      <p>
        Attach to this same session in your default terminal. Closing either
        terminal leaves the session running.
      </p>
      <a id="desktop-open" className="button primary" href={info.desktop_uri}>
        Open in terminal
      </a>
      <h3>First-time setup</h3>
      <div className="desktop-platforms">
        <section>
          <span className="platform-label">Linux</span>
          <h3>Your default terminal</h3>
          <p>Download the launcher, then run:</p>
          <code>bash setup-lectern-terminal.sh</code>
          <a href="/desktop/setup-lectern-terminal.sh" download>
            Download Linux setup
          </a>
        </section>
        <section>
          <span className="platform-label">Windows</span>
          <h3>Your default terminal</h3>
          <p>Download the launcher, then run in PowerShell:</p>
          <code>
            powershell -ExecutionPolicy Bypass -File .\setup-lectern.ps1
          </code>
          <a href="/desktop/setup-lectern.ps1" download>
            Download Windows setup
          </a>
        </section>
      </div>
      <p>
        Both use your <code>lectern</code> SSH alias. Your terminal theme and
        existing sessions stay intact.
      </p>
      <a href="/desktop/README.txt" target="_blank" rel="noopener">
        Connection and setup instructions ↗
      </a>
      <details className="manual-connection">
        <summary>Manage Lectern entirely from a terminal</summary>
        <p>
          Install the terminal client, then run <code>lectern</code>.
          Sessions, tasks, routines, settings and context uploads are available
          without the web UI.
        </p>
        <p>
          <a href="/desktop/install-lectern-cli.sh" download>
            Linux client installer
          </a>{" "}
          ·{" "}
          <a href="/desktop/install-lectern-cli.ps1" download>
            Windows client installer
          </a>
        </p>
        <code id="cli-install-command">
          {/Win/i.test(navigator.platform)
            ? "powershell -ExecutionPolicy Bypass -File .\\install-lectern-cli.ps1 -Server lectern"
            : "bash install-lectern-cli.sh --server lectern --api " +
              quote(location.origin)}
        </code>
      </details>
      <details className="manual-connection">
        <summary>Connect from an already-open terminal</summary>
        <p>Run this in your terminal:</p>
        <textarea
          id="desktop-command"
          readOnly
          rows={3}
          aria-label="Manual SSH command"
          value={info.desktop_command}
        />
        <button
          id="desktop-copy"
          onClick={() => {
            void copyClipboard(info.desktop_command)
              .then(() => onNotice("SSH command copied."))
              .catch((error) => onNotice(errorMessage(error)));
          }}
        >
          Copy command
        </button>
      </details>
    </Dialog>
  );
}

// Snippets: tap one to send it, or manage the list.
export function Snippets({
  snippets,
  onSend,
  onChange,
  onClose,
}: {
  snippets: Snippet[];
  onSend: (snippet: Snippet) => void;
  onChange: (snippets: Snippet[]) => void;
  onClose: () => void;
}) {
  const [text, setText] = useState(""),
    [enter, setEnter] = useState(true),
    [editing, setEditing] = useState(false);
  return (
    <Dialog
      id="snippets-dialog"
      title="Snippets"
      onClose={onClose}
      actions={
        <button id="snippets-edit" aria-pressed={editing} onClick={() => setEditing(!editing)}>
          {editing ? "Done" : "Edit"}
        </button>
      }
    >
      <div className="snippet-list">
        {snippets.map((snippet, index) => (
          <div className="snippet-row" key={index + snippet.text}>
            <button
              className="snippet-send"
              disabled={editing}
              onClick={() => {
                onSend(snippet);
                onClose();
              }}
            >
              <code>{snippet.text}</code>
              {snippet.enter && <span aria-label="then Enter">⏎</span>}
            </button>
            {editing && (
              <button
                className="snippet-remove"
                aria-label={"Remove " + snippet.text}
                onClick={() => onChange(snippets.filter((_, other) => other !== index))}
              >
                ×
              </button>
            )}
          </div>
        ))}
        {!snippets.length && <p>No snippets yet. Add the replies you type most.</p>}
      </div>
      <form
        className="snippet-add"
        onSubmit={(event) => {
          event.preventDefault();
          if (!text) return;
          onChange([...snippets, { text, enter }]);
          setText("");
        }}
      >
        <input
          id="snippet-text"
          aria-label="New snippet"
          placeholder="New snippet"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          value={text}
          onChange={(event) => setText(event.target.value)}
        />
        <label className="snippet-enter">
          <input type="checkbox" checked={enter} onChange={(event) => setEnter(event.target.checked)} />⏎
        </label>
        <button id="snippet-add" disabled={!text}>
          Add
        </button>
      </form>
    </Dialog>
  );
}
