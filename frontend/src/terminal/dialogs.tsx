import { errorMessage } from "./model";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { t, useLocale } from "../i18n";
import { usePref } from "../prefs/store";
import { allTerminalThemes } from "../theme/terminal-themes";
import { saveTerminalPrefs, THEMES_KEY, useTerminalPrefs } from "../theme/terminal-prefs";
import { cleanQuickCommands, GLOBAL_KEY, projectKey, quickId, readGlobal, writeScope, type QuickCommand, type QuickScope } from "../quick/commands";
const NO_THEMES: unknown[] = [];
import {
  copyClipboard,
  downloadBlob,
  json,
  quote,
  request,
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
  useLocale();
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
          {t("terminal.close")}
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
  useLocale();
  // The colour scheme and spacing follow the person to every device; the
  // font size is this device's own.
  const person = useTerminalPrefs();
  const [custom] = usePref<unknown>(THEMES_KEY, NO_THEMES);
  const list = allTerminalThemes(custom);
  return (
    <Dialog id="settings-dialog" title={t("terminal.appearance")} onClose={onClose}>
      <label>
        {t("terminal.fontSize")}
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
        {t("settings.workspace.lineHeight")}
        <select
          id="line-height"
          value={prefs.lineHeight}
          onChange={(event) => {
            onPrefs({ ...prefs, lineHeight: Number(event.target.value) });
            saveTerminalPrefs({ lineHeight: Number(event.target.value) });
          }}
        >
          {[
            [1, t("settings.workspace.compact")],
            [1.15, t("settings.workspace.comfortable")],
            [1.3, t("settings.workspace.spacious")],
          ].map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <label>
        {t("terminal.theme")}
        <select
          id="theme"
          value={prefs.theme}
          onChange={(event) => {
            onPrefs({ ...prefs, theme: event.target.value });
            saveTerminalPrefs({ theme: event.target.value });
          }}
        >
          <optgroup label={t("terminal.darkThemes")}>
            {list.filter((row) => !row.light).map((row) => <option key={row.id} value={row.id}>{row.name}</option>)}
          </optgroup>
          <optgroup label={t("terminal.lightThemes")}>
            {list.filter((row) => row.light).map((row) => <option key={row.id} value={row.id}>{row.name}</option>)}
          </optgroup>
        </select>
      </label>
      <label className="appearance-check">
        <input type="checkbox" checked={person.osc52} onChange={(event) => saveTerminalPrefs({ osc52: event.target.checked })} />
        {t("settings.workspace.osc52")}
      </label>
      <p>
        {t("terminal.appearanceHint")}{" "}
        <a href="/#targets" target="_top">{t("terminal.moreThemes")}</a>
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
  useLocale();
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
      title={t("terminalPage.history.title")}
      onClose={onClose}
      actions={
        <>
          <button
            id="history-refresh"
            onClick={() => setRefresh((old) => old + 1)}
          >
            {t("terminalPage.common.refresh")}
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
            {t("terminalPage.common.download")}
          </button>
        </>
      }
    >
      <div className="history-controls">
        <input
          id="history-query"
          autoFocus
          placeholder={t("terminalPage.history.placeholder")}
          aria-label={t("terminalPage.history.searchLabel")}
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
              : t("terminal.noMatches")
            : ""}
        </span>
      </div>
      <p id="history-note">
        {error ||
          (!data
            ? t("terminalPage.history.loading")
            : t(shell ? "terminalPage.history.snapshotShell" : "terminalPage.history.snapshotAgent", { lines: data.limit_lines.toLocaleString() }) +
              (data.truncated ? t("terminalPage.history.truncated") : ""))}
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
      if (!context) throw new Error(t("terminalPage.pdf.canvasUnavailable"));
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
          {t("terminalPage.pdf.previous")}
        </button>
        <span id="pdf-page">
          {error ||
            (!busy && pdf ? t("terminalPage.pdf.pageOf", { page, pages: pdf.numPages }) : t("terminalPage.pdf.loading"))}
        </span>
        <button
          disabled={busy || !pdf || page >= pdf.numPages}
          onClick={() => setPage(page + 1)}
        >
          {t("terminalPage.pdf.next")}
        </button>
      </div>
      <canvas ref={canvas} aria-label={t("terminalPage.pdf.pageLabel")} />
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
  useLocale();
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
              ? t("terminalPage.files.previewLimited") + "\n\n"
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
        title={t("terminalPage.files.title")}
        onClose={onClose}
        actions={
          <button
            id="files-refresh"
            onClick={() => setRefresh((old) => old + 1)}
          >
            {t("terminalPage.common.refresh")}
          </button>
        }
      >
        <div className="file-location">
          <button
            id="files-up"
            disabled={dir === "."}
            onClick={() => setDir(dir.split("/").slice(0, -1).join("/") || ".")}
          >
            {t("terminalPage.files.parent")}
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
                  ? t("terminalPage.files.folder")
                  : t("terminalPage.files.size", { size: Math.ceil(file.size / 1024) })}
              </span>
              {!file.directory && (
                <button onClick={() => insert(file.path)}>{t("terminalPage.files.insertPath")}</button>
              )}
            </div>
          ))}
          {listing && !listing.entries.length && t("terminalPage.files.empty")}
        </div>
        <p>
          {t("terminalPage.files.limits")}
        </p>
      </Dialog>
      {preview && (
        <Dialog
          id="preview-dialog"
          title={preview.path.split("/").pop() || t("terminalPage.files.preview")}
          onClose={() => {
            setPreview(undefined);
            URL.revokeObjectURL(url.current);
            url.current = "";
          }}
          actions={
            <>
              <button id="preview-insert" onClick={() => insert(preview.path)}>
                {t("terminalPage.files.insertPath")}
              </button>
              <a
                id="preview-download"
                href={preview.url}
                download={preview.path.split("/").pop()}
              >
                {t("terminalPage.common.download")}
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
              t("terminalPage.files.binary")
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
  useLocale();
  return (
    <Dialog id="desktop-dialog" title={t("terminalPage.desktop.title")} onClose={onClose}>
      <p>
        {t("terminalPage.desktop.intro")}
      </p>
      <a id="desktop-open" className="button primary" href={info.desktop_uri}>
        {t("terminalPage.tools.openInTerminal")}
      </a>
      <h3>{t("terminalPage.desktop.firstTime")}</h3>
      <div className="desktop-platforms">
        <section>
          <span className="platform-label">Linux</span>
          <h3>{t("terminalPage.desktop.defaultTerminal")}</h3>
          <p>{t("terminalPage.desktop.runLinux")}</p>
          <code>bash setup-lectern-terminal.sh</code>
          <a href="/desktop/setup-lectern-terminal.sh" download>
            {t("terminalPage.desktop.downloadLinux")}
          </a>
        </section>
        <section>
          <span className="platform-label">Windows</span>
          <h3>{t("terminalPage.desktop.defaultTerminal")}</h3>
          <p>{t("terminalPage.desktop.runWindows")}</p>
          <code>
            powershell -ExecutionPolicy Bypass -File .\setup-lectern.ps1
          </code>
          <a href="/desktop/setup-lectern.ps1" download>
            {t("terminalPage.desktop.downloadWindows")}
          </a>
        </section>
      </div>
      <p>
        {t("terminalPage.desktop.aliasBefore")} <code>lectern</code> {t("terminalPage.desktop.aliasAfter")}
      </p>
      <a href="/desktop/README.txt" target="_blank" rel="noopener">
        {t("terminalPage.desktop.instructions")}
      </a>
      <details className="manual-connection">
        <summary>{t("terminalPage.desktop.cliSummary")}</summary>
        <p>
          {t("terminalPage.desktop.cliBefore")} <code>lectern</code>{t("terminalPage.desktop.cliAfter")}
        </p>
        <p>
          <a href="/desktop/install-lectern-cli.sh" download>
            {t("terminalPage.desktop.linuxInstaller")}
          </a>{" "}
          ·{" "}
          <a href="/desktop/install-lectern-cli.ps1" download>
            {t("terminalPage.desktop.windowsInstaller")}
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
        <summary>{t("terminalPage.desktop.manualSummary")}</summary>
        <p>{t("terminalPage.desktop.runThis")}</p>
        <textarea
          id="desktop-command"
          readOnly
          rows={3}
          aria-label={t("terminalPage.desktop.manualLabel")}
          value={info.desktop_command}
        />
        <button
          id="desktop-copy"
          onClick={() => {
            void copyClipboard(info.desktop_command)
              .then(() => onNotice(t("terminalPage.desktop.copied")))
              .catch((error) => onNotice(errorMessage(error)));
          }}
        >
          {t("terminalPage.desktop.copyCommand")}
        </button>
      </details>
    </Dialog>
  );
}

// Snippets: tap one to send it, or manage the list.
// Quick commands for the key bar: this project's own first, then the ones
// kept for everywhere. Both lists live on the server (quick/commands.ts), so
// a reply saved on the phone is on the desk too.
export function Snippets({
  projectId,
  onSend,
  onClose,
}: {
  projectId: number | null;
  onSend: (command: QuickCommand) => void;
  onClose: () => void;
}) {
  useLocale();
  const [globalRaw] = usePref<unknown>(GLOBAL_KEY, undefined);
  const [projectRaw] = usePref<unknown>(projectId ? projectKey(projectId) : "quick-commands:none", NO_THEMES);
  const everywhere = globalRaw === undefined ? readGlobal() : cleanQuickCommands(globalRaw);
  const project = projectId ? cleanQuickCommands(projectRaw) : [];
  const [text, setText] = useState(""),
    [enter, setEnter] = useState(true),
    [scope, setScope] = useState<"global" | "project">("global"),
    [editing, setEditing] = useState(false);
  const groups: { scope: QuickScope; title: string; rows: QuickCommand[] }[] = [
    ...(projectId ? [{ scope: { kind: "project", id: projectId } as QuickScope, title: t("quick.thisProject"), rows: project }] : []),
    { scope: { kind: "global" }, title: t("quick.everywhere"), rows: everywhere },
  ];
  const numbered = [...project, ...everywhere];
  return (
    <Dialog
      id="snippets-dialog"
      title={t("quick.title")}
      onClose={onClose}
      actions={
        <button id="snippets-edit" aria-pressed={editing} onClick={() => setEditing(!editing)}>
          {editing ? t("quick.done") : t("quick.edit")}
        </button>
      }
    >
      <div className="snippet-list">
        {groups.map((group) => (
          (group.rows.length > 0 || projectId) && (
            <div className="snippet-group" key={group.title}>
              {projectId && <h3 className="snippet-group-title">{group.title}</h3>}
              {group.rows.map((command) => {
                const number = numbered.indexOf(command) + 1;
                return (
                  <div className="snippet-row" key={command.id}>
                    <button
                      className="snippet-send"
                      disabled={editing}
                      title={number <= 9 ? t("quick.numberHint", { number }) : undefined}
                      onClick={() => {
                        onSend(command);
                        onClose();
                      }}
                    >
                      {command.label && <span className="snippet-label">{command.label}</span>}
                      <code>{command.text}</code>
                      {command.enter && <span aria-label={t("quick.thenEnter")}>⏎</span>}
                    </button>
                    {editing && (
                      <button
                        className="snippet-remove"
                        aria-label={t("settings.remove", { name: command.label || command.text })}
                        onClick={() => writeScope(group.scope, group.rows.filter((row) => row.id !== command.id))}
                      >
                        ×
                      </button>
                    )}
                  </div>
                );
              })}
              {projectId && !group.rows.length && <p className="snippet-empty">{t("quick.noneHere")}</p>}
            </div>
          )
        ))}
        {!numbered.length && <p>{t("quick.empty")}</p>}
      </div>
      <form
        className="snippet-add"
        onSubmit={(event) => {
          event.preventDefault();
          if (!text) return;
          const target: QuickScope = scope === "project" && projectId ? { kind: "project", id: projectId } : { kind: "global" };
          const rows = target.kind === "project" ? project : everywhere;
          writeScope(target, [...rows, { id: quickId(), label: "", text, enter }]);
          setText("");
        }}
      >
        <input
          id="snippet-text"
          aria-label={t("quick.new")}
          placeholder={t("quick.new")}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          value={text}
          onChange={(event) => setText(event.target.value)}
        />
        <label className="snippet-enter">
          <input type="checkbox" checked={enter} onChange={(event) => setEnter(event.target.checked)} />⏎
        </label>
        {projectId && (
          <select aria-label={t("quick.saveFor")} value={scope} onChange={(event) => setScope(event.target.value === "project" ? "project" : "global")}>
            <option value="global">{t("quick.everywhere")}</option>
            <option value="project">{t("quick.thisProject")}</option>
          </select>
        )}
        <button id="snippet-add" disabled={!text}>
          {t("settings.add")}
        </button>
      </form>
    </Dialog>
  );
}
