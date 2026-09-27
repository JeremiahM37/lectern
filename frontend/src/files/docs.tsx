// Open files: loading, editing, saving with conflict detection, following the
// disk, and the panel that shows one of them with its viewers. Shared by the
// terminal page's file panel (Workbench.tsx) and the workspace's file panes
// (PaneViews.tsx).
import { lazy, Suspense, useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { t } from "../i18n";
import { copyClipboard, errorMessage } from "../terminal/model";
import { ConflictError, isOutside, type FileApi } from "./api";
import { fileLink, type LineTarget } from "./deeplink";
import { CodeEditor, loadMonaco, type EditorHandle } from "./Editor";
import { languageFor, mimeFor, rendered, viewKind, type ViewKind } from "./formats";
import { FileIcon } from "./icons";
import { ImageView, PdfView } from "./media";

const MarkdownView = lazy(() => import("./viewers").then((m) => ({ default: m.MarkdownView })));
const MarkdownFragment = lazy(() => import("./viewers").then((m) => ({ default: m.MarkdownFragment })));
const HtmlView = lazy(() => import("./viewers").then((m) => ({ default: m.HtmlView })));
const MermaidView = lazy(() => import("./viewers").then((m) => ({ default: m.MermaidView })));
const CsvTable = lazy(() => import("./viewers").then((m) => ({ default: m.CsvTable })));
const NotebookView = lazy(() => import("./viewers").then((m) => ({ default: m.NotebookView })));
const RichMarkdown = lazy(() => import("./RichMarkdown"));

/** Files larger than this open read-only, showing the first MiB. */
const EDIT_LIMIT = 10 * 1024 * 1024;
const PREVIEW_LIMIT = 1024 * 1024;

export type Mode = "view" | "rich" | "split" | "source";

export interface Doc {
  path: string;
  kind: ViewKind;
  loading: boolean;
  blob?: Blob;
  url: string;
  text: string; // what is on disk, as last read or saved
  buffer: string; // what the editor holds
  sha: string;
  revision: number; // bumped when the buffer is replaced from disk
  readOnly: boolean;
  // Outside the workspace: always read-only, and not followed on disk.
  outside?: boolean;
  truncated: boolean;
  mode: Mode;
  editing: boolean;
  scripts: boolean;
  target?: LineTarget;
  targetKey: number;
  syncLine?: number;
  saving: boolean;
  error?: string;
  conflict?: { sha: string | null; message: string };
  disk?: "changed" | "deleted";
}

export interface Settings {
  autosave: boolean;
  minimap: boolean;
  wordWrap: boolean;
  editor: "auto" | "full" | "simple";
  explorerWidth: number;
  editorShare: number;
}

const SETTINGS_KEY = "lec-files-settings";
export function loadSettings(): Settings {
  const fallback: Settings = { autosave: false, minimap: true, wordWrap: false, editor: "auto", explorerWidth: 280, editorShare: 55 };
  try {
    return { ...fallback, ...JSON.parse(localStorage.getItem(SETTINGS_KEY) || "{}") };
  } catch {
    return fallback;
  }
}

export function useSettings() {
  const [settings, setSettings] = useState(loadSettings);
  useEffect(() => {
    try {
      localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings));
    } catch {}
  }, [settings]);
  return [settings, setSettings] as const;
}

export function useMedia(query: string) {
  const [matches, setMatches] = useState(() => matchMedia(query).matches);
  useEffect(() => {
    const list = matchMedia(query);
    const change = () => setMatches(list.matches);
    list.addEventListener("change", change);
    return () => list.removeEventListener("change", change);
  }, [query]);
  return matches;
}

export const nameOf = (path: string) => path.slice(path.lastIndexOf("/") + 1);
export const dirOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");

export interface DocsOptions {
  api: FileApi;
  phone: boolean;
  notice: (text: string, error?: boolean) => void;
  autosave: boolean;
  onSaved?: () => void;
}

export function useFileDocs({ api, phone, notice, autosave, onSaved }: DocsOptions) {
  const [docs, setDocs] = useState<Doc[]>([]);
  const [active, setActive] = useState<string>();
  const docsRef = useRef(docs);
  docsRef.current = docs;
  // Editor text as typed, ahead of React's next render: a save started by a
  // keystroke must include the keystrokes just before it.
  const typed = useRef(new Map<string, string>());
  const update = useCallback((path: string, change: Partial<Doc> | ((doc: Doc) => Partial<Doc>)) => {
    setDocs((old) => old.map((doc) => (doc.path === path ? { ...doc, ...(typeof change === "function" ? change(doc) : change) } : doc)));
  }, []);
  const doc = docs.find((item) => item.path === active);

  const openFile = useCallback(
    async (path: string, target?: LineTarget, preferSource = false) => {
      setActive(path);
      const existing = docsRef.current.find((item) => item.path === path);
      if (existing) {
        if (target)
          update(path, (old) => ({
            target,
            targetKey: old.targetKey + 1,
            mode: rendered(old.kind) && (old.mode === "view" || old.mode === "rich") ? (phone || old.kind !== "markdown" ? "source" : "split") : old.mode,
          }));
        return;
      }
      setDocs((old) => [
        ...old,
        { path, kind: "code", loading: true, url: "", text: "", buffer: "", sha: "", revision: 0, readOnly: true, outside: isOutside(path), truncated: false, mode: "source", editing: false, scripts: false, target, targetKey: 1, saving: false },
      ]);
      try {
        const opened = await api.open(path);
        const head = new Uint8Array(await opened.blob.slice(0, 8192).arrayBuffer());
        const kind = viewKind(path, head);
        const textual = !["image", "pdf", "binary"].includes(kind);
        const truncated = textual && opened.blob.size > EDIT_LIMIT;
        const text = textual ? await (truncated ? opened.blob.slice(0, PREVIEW_LIMIT) : opened.blob).text() : "";
        const url = URL.createObjectURL(new Blob([opened.blob], { type: mimeFor(path) }));
        const wantsSource = !!target || preferSource;
        const mode: Mode = !rendered(kind) ? "source" : wantsSource ? (phone || kind !== "markdown" ? "source" : "split") : "view";
        if (!docsRef.current.some((item) => item.path === path)) return URL.revokeObjectURL(url);
        update(path, { kind, loading: false, blob: opened.blob, url, text, buffer: text, sha: opened.sha256, readOnly: truncated || isOutside(path), truncated, mode });
      } catch (error) {
        // Say which path was tried: a link may have named something else.
        update(path, { loading: false, error: t("files.link.failed", { path, message: errorMessage(error) }) });
      }
    },
    [api, phone, update],
  );

  const closeDoc = useCallback((path: string) => {
    const closing = docsRef.current.find((item) => item.path === path);
    if (closing && closing.buffer !== closing.text && !window.confirm(t("files.discard", { name: nameOf(path) }))) return false;
    if (closing?.url) URL.revokeObjectURL(closing.url);
    typed.current.delete(path);
    void loadMonaco().then((module) => module.releaseModel(path), () => {});
    const remaining = docsRef.current.filter((item) => item.path !== path);
    setDocs(remaining);
    setActive((current) => (current === path ? remaining.at(-1)?.path : current));
    return true;
  }, []);

  const save = useCallback(
    async (path: string, base?: string) => {
      const current = docsRef.current.find((item) => item.path === path);
      const body = typed.current.get(path) ?? current?.buffer;
      if (!current || body === undefined || current.saving || current.readOnly || (!base && body === current.text)) return;
      update(path, { saving: true, error: undefined });
      try {
        const saved = await api.save(path, body, base || current.sha || "absent");
        update(path, (old) => {
          if (old.url) URL.revokeObjectURL(old.url);
          const blob = new Blob([body]);
          return { saving: false, text: body, sha: saved.sha256, conflict: undefined, disk: undefined, blob, url: URL.createObjectURL(new Blob([blob], { type: mimeFor(path) })) };
        });
        onSaved?.();
        notice(t("files.saved", { name: nameOf(path) }));
      } catch (error) {
        if (error instanceof ConflictError) update(path, { saving: false, conflict: { sha: error.sha256, message: error.message } });
        else update(path, { saving: false, error: errorMessage(error) });
      }
    },
    [api, notice, onSaved, update],
  );

  const reload = useCallback(
    async (path: string) => {
      try {
        const opened = await api.open(path);
        const text = await opened.blob.slice(0, EDIT_LIMIT).text();
        typed.current.delete(path);
        update(path, (old) => {
          if (old.url) URL.revokeObjectURL(old.url);
          return {
            blob: opened.blob,
            url: URL.createObjectURL(new Blob([opened.blob], { type: mimeFor(path) })),
            text,
            buffer: text,
            sha: opened.sha256,
            revision: old.revision + 1,
            conflict: undefined,
            disk: undefined,
            error: undefined,
          };
        });
      } catch (error) {
        update(path, { error: errorMessage(error) });
      }
    },
    [api, update],
  );

  // Compare open files with the disk: an unedited file follows the disk; an
  // edited one says the disk changed and offers to compare. Run when the
  // watch reports a change, and when the page comes back.
  const checkDisk = useCallback(async () => {
    for (const current of docsRef.current.slice(0, 20)) {
      if (current.loading || current.saving || !current.sha || current.outside) continue;
      const path = current.path;
      try {
        const stat = await api.stat(path);
        const now = docsRef.current.find((item) => item.path === path);
        if (!now || now.saving) continue;
        if (!stat.exists) update(path, { disk: "deleted" });
        else if (stat.sha256 && stat.sha256 !== now.sha) {
          if (now.buffer === now.text && !now.truncated) {
            await reload(path);
            notice(t("files.reloaded", { name: nameOf(path) }));
          } else update(path, { disk: "changed" });
        } else if (now.disk) update(path, { disk: undefined });
      } catch {}
    }
  }, [api, notice, reload, update]);
  useEffect(() => {
    const focus = () => void checkDisk();
    window.addEventListener("focus", focus);
    return () => window.removeEventListener("focus", focus);
  }, [checkDisk]);

  // Autosave after a pause in typing, never over a conflict.
  useEffect(() => {
    if (!autosave || !doc || doc.readOnly || doc.buffer === doc.text || doc.conflict || doc.saving) return;
    const timer = window.setTimeout(() => void save(doc.path), 1000);
    return () => window.clearTimeout(timer);
  }, [autosave, doc?.buffer, doc?.conflict, doc?.saving]);

  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => {
      if (docsRef.current.some((item) => item.buffer !== item.text)) event.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, []);

  // A rename or move in the explorer carries open files along.
  const remap = useCallback((from: string, to: string) => {
    const moved = (path: string) => (path === from || path.startsWith(from + "/") ? to + path.slice(from.length) : path);
    setDocs((old) => old.map((item) => ({ ...item, path: moved(item.path) })));
    setActive((current) => current && moved(current));
  }, []);

  return { docs, active, setActive, doc, update, openFile, closeDoc, save, reload, checkDisk, remap, typed };
}

export type FileDocs = ReturnType<typeof useFileDocs>;

export interface DocPanelProps {
  docs: FileDocs;
  api: FileApi;
  positionPrefix: string;
  phone: boolean;
  full: boolean;
  settings: Settings;
  setSettings: (update: (old: Settings) => Settings) => void;
  notice: (text: string, error?: boolean) => void;
  showTabs: boolean;
  sectionId?: string;
  className?: string;
  style?: CSSProperties;
  before?: ReactNode;
  // What the surrounding surface offers; each is hidden when absent.
  onBack?: () => void;
  onInsert?: (path: string) => void;
  onReveal?: (path: string) => void;
  onOpenPath: (path: string, target?: LineTarget) => void;
  onWiki: (name: string) => void;
  onClosed?: (path: string) => void;
  linkBase?: { origin: string; pathname: string };
  extraMenu?: ReactNode;
}

export function DocPanel(props: DocPanelProps) {
  const { docs, api, phone, full, settings, setSettings, notice } = props;
  const { doc, update } = docs;
  const editorRef = useRef<EditorHandle>(null);
  const [menu, setMenu] = useState(false);
  const [diff, setDiff] = useState<{ path: string; disk: string; mine: string }>();
  useEffect(() => {
    if (!menu) return;
    const close = (event: Event) => {
      if (event instanceof KeyboardEvent ? event.key !== "Escape" : event.target instanceof Element && event.target.closest(".wb-doc-menu")) return;
      setMenu(false);
    };
    document.addEventListener("pointerdown", close, true);
    document.addEventListener("keydown", close, true);
    return () => {
      document.removeEventListener("pointerdown", close, true);
      document.removeEventListener("keydown", close, true);
    };
  }, [menu]);
  if (!doc) return null;
  const dirty = (item: { buffer: string; text: string }) => item.buffer !== item.text;
  const close = (path: string) => {
    if (docs.closeDoc(path)) props.onClosed?.(path);
  };
  const setMode = (mode: Mode) => update(doc.path, { mode, editing: mode !== "view" ? true : doc.editing });
  const markdownTools = doc.kind === "markdown" && (doc.mode === "split" || doc.mode === "source") && !doc.readOnly && (full || doc.editing);
  const textual = !["image", "pdf", "binary"].includes(doc.kind);

  function viewFor(item: Doc): ReactNode {
    if (item.loading) return <p className="wb-muted">{t("files.opening", { name: nameOf(item.path) })}</p>;
    if (item.error && !item.blob) return <p className="wb-error" role="alert">{item.error}</p>;
    if (item.kind === "image" || (item.kind === "svg" && item.mode === "view")) return <ImageView url={item.url} name={item.path} />;
    if (item.kind === "pdf") return <PdfView blob={item.blob!} positionKey={props.positionPrefix + ":" + item.path} />;
    if (item.kind === "binary") return <p className="wb-muted">{t("files.binary", { size: (item.blob?.size || 0).toLocaleString() })}</p>;
    const onChange = (value: string) => {
      docs.typed.current.set(item.path, value);
      update(item.path, { buffer: value });
    };
    if (item.mode === "rich")
      return (
        <div className="wb-md-scroll wb-rich-scroll">
          <RichMarkdown key={item.path} value={item.buffer} revision={item.revision} readOnly={item.readOnly} onChange={onChange} onSave={() => void docs.save(item.path)} />
        </div>
      );
    const editor = (
      <CodeEditor
        ref={editorRef}
        path={item.path}
        value={item.buffer}
        revision={item.revision}
        readOnly={item.readOnly || (!full && !item.editing)}
        full={full}
        fontSize={phone ? 12 : 13}
        minimap={settings.minimap && !phone}
        wordWrap={settings.wordWrap || item.kind === "markdown" || (phone && !full)}
        target={item.target}
        targetKey={item.targetKey}
        onChange={onChange}
        onSave={() => void docs.save(item.path)}
        onLine={(target) => update(item.path, { target })}
        onScrollLine={item.kind === "markdown" ? (line) => update(item.path, { syncLine: line }) : undefined}
        onFallback={(reason) => {
          notice(t("files.editorFallback", { reason }), true);
          setSettings((old) => ({ ...old, editor: "simple" }));
        }}
      />
    );
    if (item.mode === "source") return editor;
    const preview = (() => {
      switch (item.kind) {
        case "markdown":
          return (
            <MarkdownView
              text={item.buffer}
              dir={dirOf(item.path)}
              toc={item.mode === "view"}
              syncLine={item.mode === "split" ? item.syncLine : undefined}
              onOpenPath={(path) => props.onOpenPath(path)}
              onWiki={props.onWiki}
              onSourceLine={
                item.mode === "split"
                  ? (line) => editorRef.current?.reveal(line)
                  : (line) => update(item.path, (old) => ({ mode: phone ? "source" : "split", target: { line }, targetKey: old.targetKey + 1 }))
              }
              loadImage={async (path) => URL.createObjectURL(new Blob([(await api.open(path)).blob], { type: mimeFor(path) }))}
            />
          );
        case "html":
          return <HtmlView text={item.buffer} scripts={item.scripts} />;
        case "mermaid":
          return <MermaidView text={item.buffer} />;
        case "csv":
          return <CsvTable text={item.buffer} path={item.path} />;
        case "notebook":
          return <NotebookView text={item.buffer} markdown={(text) => <MarkdownFragment text={text} />} />;
      }
      return null;
    })();
    if (item.mode === "split")
      return (
        <div className="wb-split">
          <div className="wb-split-source">{editor}</div>
          <div className="wb-split-preview wb-md-scroll">{preview}</div>
        </div>
      );
    return <div className="wb-md-scroll">{preview}</div>;
  }

  return (
    <section id={props.sectionId} className={"wb-editor " + (props.className || "")} style={props.style} data-back-overlay={props.sectionId ? "20" : undefined} aria-label={t("files.fileLabel", { path: doc.path })}>
      {props.before}
      {props.showTabs && (
        <div className="wb-tabs" role="tablist" aria-label={t("files.openFiles")}>
          {docs.docs.map((item) => (
            <div key={item.path} className={"wb-tab" + (item.path === docs.active ? " active" : "")}>
              <span
                role="tab"
                tabIndex={0}
                aria-selected={item.path === docs.active}
                title={item.path}
                onClick={() => docs.setActive(item.path)}
                onKeyDown={(event) => {
                  if (event.key === "Enter" || event.key === " ") docs.setActive(item.path);
                }}
                onAuxClick={(event) => {
                  if (event.button === 1) close(item.path);
                }}
              >
                {nameOf(item.path)}
                {dirty(item) ? " ●" : ""}
              </span>
              <button className="wb-tab-close" aria-label={t("files.closeTab", { name: nameOf(item.path) })} onClick={() => close(item.path)}>
                ×
              </button>
            </div>
          ))}
        </div>
      )}
      <div className="wb-doc-head">
        {props.onBack && (
          <button className="wb-back" aria-label={t("files.back")} onClick={props.onBack} hidden={!phone}>
            ‹
          </button>
        )}
        <span className="wb-doc-path" title={doc.path}>
          {doc.path}
        </span>
        <span className="wb-doc-state" role="status">
          {doc.saving ? t("files.saving") : doc.outside ? t("files.outsideReadOnly") : doc.readOnly && doc.truncated ? t("files.readOnlyTruncated") : dirty(doc) ? t("files.unsaved") : ""}
        </span>
        {rendered(doc.kind) && !doc.loading && (
          <div className="wb-modes" role="group" aria-label={t("files.view")}>
            <button aria-pressed={doc.mode === "view"} onClick={() => setMode("view")}>
              {t("files.preview")}
            </button>
            {doc.kind === "markdown" && !doc.readOnly && (
              <button aria-pressed={doc.mode === "rich"} onClick={() => setMode("rich")}>
                {t("files.rich")}
              </button>
            )}
            {doc.kind === "markdown" && !phone && (
              <button aria-pressed={doc.mode === "split"} onClick={() => setMode("split")}>
                {t("files.split")}
              </button>
            )}
            <button aria-pressed={doc.mode === "source"} onClick={() => setMode("source")}>
              {doc.kind === "markdown" && !doc.readOnly ? t("files.edit") : t("files.source")}
            </button>
          </div>
        )}
        {!full && !doc.readOnly && doc.mode === "source" && textual && (
          <button id="file-edit" aria-pressed={doc.editing} onClick={() => update(doc.path, { editing: !doc.editing })}>
            {doc.editing ? t("files.done") : t("files.edit")}
          </button>
        )}
        {!doc.readOnly && textual && (
          <button id="file-save" className="primary" disabled={!dirty(doc) || doc.saving} onClick={() => void docs.save(doc.path)} title={t("files.saveHint")}>
            {t("files.save")}
          </button>
        )}
        <a id="preview-download" className="button wb-icon-button" href={doc.url || undefined} download={nameOf(doc.path)} aria-label={t("files.download")} title={t("files.download")}>
          <FileIcon name="download" />
        </a>
        <details className="wb-doc-menu" data-back-close open={menu} onToggle={(event) => setMenu(event.currentTarget.open)}>
          <summary aria-label={t("files.actions")} title={t("files.actions")}>
            ⋯
          </summary>
          <div className="wb-menu" role="menu" onClick={(event) => (event.target as Element).closest("button,a") && setMenu(false)}>
            {props.onInsert && (
              <button id="preview-insert" role="menuitem" onClick={() => props.onInsert?.(doc.path)}>
                {t("files.insertPath")}
              </button>
            )}
            <button role="menuitem" onClick={() => void copyClipboard(doc.path).then(() => notice(t("files.pathCopied")), (error) => notice(errorMessage(error), true))}>
              {t("files.copyPath")}
            </button>
            {props.linkBase && (
              <button
                role="menuitem"
                onClick={() =>
                  void copyClipboard(fileLink(props.linkBase!, doc.path, doc.target)).then(
                    () => notice(doc.target ? t("files.lineLinkCopied", { line: doc.target.line }) : t("files.linkCopied")),
                    (error) => notice(errorMessage(error), true),
                  )
                }
              >
                {doc.target ? t("files.copyLinkLine", { line: doc.target.line }) : t("files.copyLink")}
              </button>
            )}
            {props.onReveal && !doc.outside && (
              <button role="menuitem" onClick={() => props.onReveal?.(doc.path)}>
                {t("files.showInExplorer")}
              </button>
            )}
            <button role="menuitem" onClick={() => void docs.reload(doc.path)} disabled={doc.loading}>
              {t("files.reload")}
            </button>
            {doc.kind === "html" && (
              <button role="menuitem" aria-pressed={doc.scripts} onClick={() => update(doc.path, { scripts: !doc.scripts })}>
                {doc.scripts ? t("files.blockScripts") : t("files.allowScripts")}
              </button>
            )}
            <label className="wb-check" role="menuitemcheckbox" aria-checked={settings.autosave}>
              <input type="checkbox" checked={settings.autosave} onChange={() => setSettings((old) => ({ ...old, autosave: !old.autosave }))} /> {t("files.autosave")}
            </label>
            <label className="wb-check" role="menuitemcheckbox" aria-checked={settings.wordWrap}>
              <input type="checkbox" checked={settings.wordWrap} onChange={() => setSettings((old) => ({ ...old, wordWrap: !old.wordWrap }))} /> {t("files.wordWrap")}
            </label>
            {full && (
              <label className="wb-check" role="menuitemcheckbox" aria-checked={settings.minimap}>
                <input type="checkbox" checked={settings.minimap} onChange={() => setSettings((old) => ({ ...old, minimap: !old.minimap }))} /> {t("files.minimap")}
              </label>
            )}
            <button role="menuitem" onClick={() => setSettings((old) => ({ ...old, editor: full ? "simple" : "full" }))}>
              {full ? t("files.useSimple") : t("files.useFull")}
            </button>
            {props.extraMenu}
          </div>
        </details>
        <button data-close className="wb-close" aria-label={t("files.closeFile")} title={t("files.closeFile")} onClick={() => close(doc.path)}>
          <FileIcon name="close" />
        </button>
      </div>
      {markdownTools && <MarkdownToolbar editor={editorRef} />}
      {doc.mode === "rich" && <p className="wb-rich-note">{t("files.richNote")}</p>}
      {(doc.conflict || doc.disk || (doc.error && doc.blob)) && (
        <div className="wb-banner" role="alert">
          {doc.conflict ? (
            <span>{t("files.notSaved", { message: doc.conflict.message })}</span>
          ) : doc.disk === "deleted" ? (
            <span>{t("files.deletedOnDisk")}</span>
          ) : doc.disk === "changed" ? (
            <span>{t("files.changedOnDisk")}</span>
          ) : (
            <span>{doc.error}</span>
          )}
          {(doc.conflict || doc.disk === "changed") && (
            <>
              <button
                onClick={() =>
                  void api.open(doc.path).then(
                    async (opened) => setDiff({ path: doc.path, disk: await opened.blob.text(), mine: doc.buffer }),
                    (error) => notice(errorMessage(error), true),
                  )
                }
              >
                {t("files.compare")}
              </button>
              <button onClick={() => void docs.save(doc.path, "any")}>{t("files.keepMine")}</button>
              <button onClick={() => void docs.reload(doc.path)}>{t("files.useDisk")}</button>
            </>
          )}
          {doc.disk === "deleted" && !doc.conflict && <button onClick={() => void docs.save(doc.path, "absent")}>{t("files.saveAgain")}</button>}
          {doc.error && !doc.conflict && !doc.disk && <button onClick={() => update(doc.path, { error: undefined })}>{t("files.dismiss")}</button>}
        </div>
      )}
      <div id={props.sectionId ? "preview-body" : undefined} className={"wb-body kind-" + doc.kind}>
        <Suspense fallback={<p className="wb-muted">{t("files.loadingViewer")}</p>}>{viewFor(doc)}</Suspense>
      </div>
      {diff && <DiffView diff={diff} onClose={() => setDiff(undefined)} />}
    </section>
  );
}

function MarkdownToolbar({ editor }: { editor: React.RefObject<EditorHandle | null> }) {
  const tools: [string, string, () => void][] = [
    ["B", t("files.bold"), () => editor.current?.wrap("**", "**", "bold text")],
    ["I", t("files.italic"), () => editor.current?.wrap("_", "_", "italic text")],
    ["H", t("files.heading"), () => editor.current?.linePrefix("## ")],
    ["`", t("files.inlineCode"), () => editor.current?.wrap("`", "`", "code")],
    [t("files.linkButton"), t("files.linkButton"), () => editor.current?.wrap("[", "](https://)", "link text")],
    ["•", t("files.bulleted"), () => editor.current?.linePrefix("- ")],
    ["1.", t("files.numbered"), () => editor.current?.linePrefix("1. ")],
    ["☐", t("files.task"), () => editor.current?.linePrefix("- [ ] ")],
    ["❝", t("files.quote"), () => editor.current?.linePrefix("> ")],
    ["▦", t("files.table"), () => editor.current?.insert("\n| Column | Column |\n| --- | --- |\n| Cell | Cell |\n")],
    ["{ }", t("files.codeBlock"), () => editor.current?.wrap("\n```\n", "\n```\n", "code")],
    ["⇄", t("files.diagram"), () => editor.current?.insert("\n```mermaid\nflowchart LR\n  A --> B\n```\n")],
  ];
  return (
    <div className="wb-md-tools" role="toolbar" aria-label={t("files.formatting")}>
      {tools.map(([label, name, action]) => (
        <button key={name} aria-label={name} title={name} onMouseDown={(event) => event.preventDefault()} onClick={action}>
          {label}
        </button>
      ))}
      <span className="wb-md-hint">{t("files.slashHint")}</span>
    </div>
  );
}

function DiffView({ diff, onClose }: { diff: { path: string; disk: string; mine: string }; onClose: () => void }) {
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let disposed = false,
      instance: { dispose(): void } | undefined;
    void loadMonaco().then(
      (module) => {
        if (disposed || !host.current) return;
        instance = module.createDiff(host.current, diff.disk, diff.mine, languageFor(diff.path));
      },
      (reason: unknown) => setError(errorMessage(reason)),
    );
    return () => {
      disposed = true;
      instance?.dispose();
    };
  }, [diff]);
  return (
    <div className="wb-quick-backdrop" data-back-overlay="40" onPointerDown={(event) => event.target === event.currentTarget && onClose()}>
      <div className="wb-diff" role="dialog" aria-label={t("files.diffLabel", { path: diff.path })}>
        <div className="dialog-head">
          <h2>{t("files.diffTitle", { name: nameOf(diff.path) })}</h2>
          <button data-close onClick={onClose}>{t("files.closeButton")}</button>
        </div>
        {error ? <p className="wb-error">{error}</p> : <div ref={host} className="wb-diff-host" />}
      </div>
    </div>
  );
}
