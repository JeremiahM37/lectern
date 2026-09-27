// The workspace's files next to its terminal: an explorer, tabs of open files
// with an editor and viewers, Quick Open and project search. Everything is
// read and written on the session's target through the file API; see
// docs/files.md for the layout and the rules.
import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { copyClipboard, downloadBlob, errorMessage, quote, type TerminalInfo } from "../terminal/model";
import { ConflictError, FileApi, type Entry, type GitKind, type Hit, type Index } from "./api";
import { fileLink, lineHash, parseLineHash, readDeepLink, type LineTarget } from "./deeplink";
import { CodeEditor, loadMonaco, type EditorHandle } from "./Editor";
import { Explorer, PATH_TYPE } from "./Explorer";
import { languageFor, mimeFor, rendered, viewKind, type ViewKind } from "./formats";
import { FileIcon } from "./icons";
import { ImageView, PdfView } from "./media";
import { QuickOpen } from "./QuickOpen";
import { SearchPanel } from "./SearchPanel";
import "./files.css";

const MarkdownView = lazy(() => import("./viewers").then((m) => ({ default: m.MarkdownView })));
const MarkdownFragment = lazy(() => import("./viewers").then((m) => ({ default: m.MarkdownFragment })));
const HtmlView = lazy(() => import("./viewers").then((m) => ({ default: m.HtmlView })));
const MermaidView = lazy(() => import("./viewers").then((m) => ({ default: m.MermaidView })));
const CsvTable = lazy(() => import("./viewers").then((m) => ({ default: m.CsvTable })));
const NotebookView = lazy(() => import("./viewers").then((m) => ({ default: m.NotebookView })));

/** Files larger than this open read-only, showing the first MiB. */
const EDIT_LIMIT = 10 * 1024 * 1024;
const PREVIEW_LIMIT = 1024 * 1024;

export interface FileRequest {
  path: string;
  target?: LineTarget;
  nonce: number;
}

type Mode = "view" | "split" | "source";

interface Doc {
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

interface Settings {
  autosave: boolean;
  minimap: boolean;
  wordWrap: boolean;
  editor: "auto" | "full" | "simple";
  explorerWidth: number;
  editorShare: number;
}

const SETTINGS_KEY = "lec-files-settings";
function loadSettings(): Settings {
  const fallback: Settings = { autosave: false, minimap: true, wordWrap: false, editor: "auto", explorerWidth: 280, editorShare: 55 };
  try {
    return { ...fallback, ...JSON.parse(localStorage.getItem(SETTINGS_KEY) || "{}") };
  } catch {
    return fallback;
  }
}

function useMedia(query: string) {
  const [matches, setMatches] = useState(() => matchMedia(query).matches);
  useEffect(() => {
    const list = matchMedia(query);
    const change = () => setMatches(list.matches);
    list.addEventListener("change", change);
    return () => list.removeEventListener("change", change);
  }, [query]);
  return matches;
}

const nameOf = (path: string) => path.slice(path.lastIndexOf("/") + 1);
const dirOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "");

export function Workbench({
  base,
  info,
  open,
  request,
  command,
  onOpenChange,
  onInsert,
  onNotice,
}: {
  base: string;
  info: TerminalInfo;
  open: boolean;
  request?: FileRequest;
  command?: { name: "quick" | "search"; nonce: number };
  onOpenChange: (open: boolean) => void;
  onInsert: (text: string) => void;
  onNotice: (text: string) => void;
}) {
  const api = useMemo(() => new FileApi(base), [base]);
  const phone = useMedia("(max-width: 900px)");
  const coarse = useMedia("(pointer: coarse)");
  const [settings, setSettings] = useState(loadSettings);
  const full = settings.editor === "full" || (settings.editor === "auto" && !phone && !coarse);
  const [side, setSide] = useState<"files" | "search">("files");
  const [searchFocus, setSearchFocus] = useState(0);
  const [docs, setDocs] = useState<Doc[]>([]);
  const [active, setActive] = useState<string>();
  const [maximized, setMaximized] = useState(false);
  const [quick, setQuick] = useState(false);
  const [index, setIndex] = useState<{ data?: Index; at: number; loading: boolean; error: string }>({ at: 0, loading: false, error: "" });
  const [status, setStatus] = useState<Record<string, GitKind>>({});
  const [revision, setRevision] = useState(0);
  const [reveal, setReveal] = useState<{ path: string; nonce: number }>();
  const [message, setMessage] = useState<{ text: string; error: boolean }>();
  const [diff, setDiff] = useState<{ path: string; disk: string; mine: string }>();
  const [menu, setMenu] = useState(false);
  const editorRef = useRef<EditorHandle>(null);
  const docsRef = useRef(docs);
  docsRef.current = docs;
  // Editor text as typed, ahead of React's next render: a save started by a
  // keystroke must include the keystrokes just before it.
  const typed = useRef(new Map<string, string>());
  const recentKey = "lec-files-recent:" + base;
  const [recent, setRecent] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem(recentKey) || "[]") as string[];
    } catch {
      return [];
    }
  });
  useEffect(() => {
    try {
      localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings));
    } catch {}
  }, [settings]);
  const notice = useCallback((text: string, error = false) => {
    setMessage({ text, error });
  }, []);
  useEffect(() => {
    if (!message || message.error) return;
    const timer = window.setTimeout(() => setMessage(undefined), 4000);
    return () => window.clearTimeout(timer);
  }, [message]);
  const update = useCallback((path: string, change: Partial<Doc> | ((doc: Doc) => Partial<Doc>)) => {
    setDocs((old) => old.map((doc) => (doc.path === path ? { ...doc, ...(typeof change === "function" ? change(doc) : change) } : doc)));
  }, []);
  const doc = docs.find((item) => item.path === active);

  // ---- Quick Open's index: fetched once, reused, refreshed in the background.
  const indexRef = useRef(index);
  indexRef.current = index;
  const ensureIndex = useCallback(
    (force = false) => {
      const current = indexRef.current;
      if (current.loading || (!force && current.data && Date.now() - current.at < 20000)) return;
      setIndex((old) => ({ ...old, loading: true, error: "" }));
      api.index().then(
        (data) => setIndex({ data, at: Date.now(), loading: false, error: "" }),
        (error) => setIndex((old) => ({ ...old, loading: false, error: errorMessage(error) })),
      );
    },
    [api],
  );
  // Warm the index shortly after the terminal loads, so the first Ctrl+P is instant.
  useEffect(() => {
    const timer = window.setTimeout(() => ensureIndex(), 1500);
    return () => window.clearTimeout(timer);
  }, [ensureIndex]);

  // ---- git status colours, refreshed with the explorer.
  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    const load = () => {
      if (document.hidden) return;
      api.gitStatus(controller.signal).then(
        (result) => setStatus((old) => (JSON.stringify(old) === JSON.stringify(result.status) ? old : result.status)),
        () => {},
      );
    };
    load();
    const timer = window.setInterval(load, 5000);
    return () => {
      controller.abort();
      window.clearInterval(timer);
    };
  }, [api, open, revision]);

  // ---- opening files
  const openFile = useCallback(
    async (path: string, target?: LineTarget, preferSource = false) => {
      onOpenChange(true);
      setActive(path);
      setRecent((old) => {
        const next = [path, ...old.filter((item) => item !== path)].slice(0, 20);
        try {
          localStorage.setItem(recentKey, JSON.stringify(next));
        } catch {}
        return next;
      });
      const existing = docsRef.current.find((item) => item.path === path);
      if (existing) {
        if (target)
          update(path, (old) => ({
            target,
            targetKey: old.targetKey + 1,
            mode: rendered(old.kind) && old.mode === "view" ? (phone ? "source" : old.kind === "markdown" ? "split" : "source") : old.mode,
          }));
        return;
      }
      setDocs((old) => [
        ...old,
        { path, kind: "code", loading: true, url: "", text: "", buffer: "", sha: "", revision: 0, readOnly: true, truncated: false, mode: "source", editing: false, scripts: false, target, targetKey: 1, saving: false },
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
        update(path, { kind, loading: false, blob: opened.blob, url, text, buffer: text, sha: opened.sha256, readOnly: truncated, truncated, mode });
      } catch (error) {
        update(path, { loading: false, error: errorMessage(error) });
      }
    },
    [api, onOpenChange, phone, recentKey, update],
  );

  // Terminal links and deep links arrive as requests.
  useEffect(() => {
    if (request) void openFile(request.path, request.target);
  }, [request?.nonce]);
  useEffect(() => {
    const link = readDeepLink(location.search, location.hash);
    if (link) void openFile(link.path, link.target);
  }, []);

  // The file menu closes on Escape or a click elsewhere, like the explorer's.
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

  // Toolbar commands from the terminal page.
  useEffect(() => {
    if (!command) return;
    if (command.name === "quick") {
      ensureIndex();
      setQuick(true);
    } else {
      onOpenChange(true);
      setSide("search");
      setSearchFocus((value) => value + 1);
    }
  }, [command?.nonce]);

  // Following a #L link on this page moves the open file to that line.
  useEffect(() => {
    const change = () => {
      const target = parseLineHash(location.hash);
      const path = new URLSearchParams(location.search).get("open");
      if (target && path) void openFile(path, target);
    };
    window.addEventListener("hashchange", change);
    return () => window.removeEventListener("hashchange", change);
  }, [openFile]);

  // The address bar follows the open file and linked line, so a copied URL
  // (or a reload) returns here.
  useEffect(() => {
    const url = new URL(location.href);
    if (open && doc) {
      url.searchParams.set("open", doc.path);
      url.hash = doc.target ? lineHash(doc.target) : "";
    } else {
      url.searchParams.delete("open");
      if (/^#L\d/i.test(url.hash)) url.hash = "";
    }
    if (url.href !== location.href) history.replaceState(history.state, "", url);
  }, [open, doc?.path, doc?.target]);

  const closeDoc = useCallback(
    (path: string) => {
      const closing = docsRef.current.find((item) => item.path === path);
      if (closing && closing.buffer !== closing.text && !window.confirm(`Discard unsaved changes to ${nameOf(path)}?`)) return;
      if (closing?.url) URL.revokeObjectURL(closing.url);
      typed.current.delete(path);
      void loadMonaco().then((module) => module.releaseModel(path), () => {});
      const remaining = docsRef.current.filter((item) => item.path !== path);
      setDocs(remaining);
      if (active === path) setActive(remaining.at(-1)?.path);
    },
    [active],
  );

  // ---- saving, with conflict detection against the hash we opened
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
        setRevision((value) => value + 1);
        notice(`Saved ${nameOf(path)}`);
      } catch (error) {
        if (error instanceof ConflictError) update(path, { saving: false, conflict: { sha: error.sha256, message: error.message } });
        else update(path, { saving: false, error: errorMessage(error) });
      }
    },
    [api, notice, update],
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

  // Autosave after a pause in typing, never over a conflict.
  useEffect(() => {
    if (!settings.autosave || !doc || doc.readOnly || doc.buffer === doc.text || doc.conflict || doc.saving) return;
    const timer = window.setTimeout(() => void save(doc.path), 1000);
    return () => window.clearTimeout(timer);
  }, [settings.autosave, doc?.buffer, doc?.conflict, doc?.saving]);

  // Watch the open file on disk: an unedited file follows the disk; an edited
  // one says the disk changed and offers to compare.
  useEffect(() => {
    if (!open || !doc || doc.loading || !doc.sha) return;
    const path = doc.path;
    const check = async () => {
      if (document.hidden) return;
      const current = docsRef.current.find((item) => item.path === path);
      if (!current || current.saving) return;
      try {
        const stat = await api.stat(path);
        const now = docsRef.current.find((item) => item.path === path);
        if (!now || now.saving) return;
        if (!stat.exists) update(path, { disk: "deleted" });
        else if (stat.sha256 && stat.sha256 !== now.sha) {
          if (now.buffer === now.text && !now.truncated) {
            await reload(path);
            notice(`${nameOf(path)} changed on disk and was reloaded`);
          } else update(path, { disk: "changed" });
        } else if (now.disk) update(path, { disk: undefined });
      } catch {}
    };
    const timer = window.setInterval(() => void check(), 4000);
    window.addEventListener("focus", check);
    return () => {
      window.clearInterval(timer);
      window.removeEventListener("focus", check);
    };
  }, [open, doc?.path, doc?.sha, doc?.loading]);

  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => {
      if (docsRef.current.some((item) => item.buffer !== item.text)) event.preventDefault();
    };
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, []);

  // ---- shortcuts. Ctrl+P belongs to the shell while the terminal has focus
  // (it is readline's previous-line key); there, Ctrl+Shift+P or Cmd+P opens
  // Go to file.
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      const target = event.target instanceof Element ? event.target : null;
      const inTerminal = !!target?.closest(".terminal-host");
      const mod = event.ctrlKey || event.metaKey;
      const letter = event.key.toLowerCase();
      if (mod && !event.altKey && letter === "p" && (event.metaKey || event.shiftKey || !inTerminal)) {
        event.preventDefault();
        event.stopPropagation();
        ensureIndex();
        setQuick(true);
      } else if (mod && event.shiftKey && letter === "f" && !inTerminal && target?.closest(".wb-panel,.wb-editor")) {
        event.preventDefault();
        event.stopPropagation();
        onOpenChange(true);
        setSide("search");
        setSearchFocus((value) => value + 1);
      } else if (mod && event.shiftKey && letter === "e" && !inTerminal) {
        event.preventDefault();
        event.stopPropagation();
        onOpenChange(!open);
      }
    };
    window.addEventListener("keydown", key, true);
    return () => window.removeEventListener("keydown", key, true);
  }, [ensureIndex, onOpenChange, open]);

  // Dropping an explorer entry on a terminal pastes its path, never Enter.
  useEffect(() => {
    const over = (event: DragEvent) => {
      if (event.dataTransfer?.types.includes(PATH_TYPE) && event.target instanceof Element && event.target.closest(".pane")) event.preventDefault();
    };
    const drop = (event: DragEvent) => {
      const path = event.dataTransfer?.getData(PATH_TYPE);
      if (!path || !(event.target instanceof Element) || !event.target.closest(".pane")) return;
      event.preventDefault();
      event.stopPropagation();
      try {
        onInsert(quote(info.workdir + "/" + path) + " ");
        onNotice("Path inserted: " + path);
      } catch (error) {
        onNotice(errorMessage(error));
      }
    };
    document.addEventListener("dragover", over, true);
    document.addEventListener("drop", drop, true);
    return () => {
      document.removeEventListener("dragover", over, true);
      document.removeEventListener("drop", drop, true);
    };
  }, [info.workdir, onInsert, onNotice]);

  const insertPath = (path: string) => {
    try {
      onInsert(quote(info.workdir + "/" + path) + " ");
      if (phone) onOpenChange(false);
    } catch (error) {
      notice(errorMessage(error), true);
    }
  };
  const download = async (entry: Entry) => {
    try {
      if (entry.directory) {
        notice(`Preparing ${entry.path === "." ? "the workspace" : entry.name}…`);
        downloadBlob(await api.archive(entry.path), (entry.path === "." ? nameOf(info.workdir) || "workspace" : entry.name) + ".zip");
      } else downloadBlob((await api.open(entry.path)).blob, entry.name);
    } catch (error) {
      notice("Download failed: " + errorMessage(error), true);
    }
  };
  const upload = async (folder: string, files: File[]) => {
    for (const file of files) {
      const path = (folder === "." ? "" : folder + "/") + file.name;
      try {
        await api.save(path, file, "absent");
      } catch (error) {
        if (error instanceof ConflictError) {
          if (!window.confirm(`${path} already exists. Replace it?`)) continue;
          await api.save(path, file, "any");
        } else throw error;
      }
    }
    notice(`Added ${files.length === 1 ? files[0]!.name : files.length + " files"} to ${folder === "." ? "the workspace" : folder}`);
    setRevision((value) => value + 1);
  };
  // [[name]] links resolve against the file index; a name the cached index
  // does not know is looked up again, in case the file is new.
  const openWiki = async (name: string) => {
    const lower = name.toLowerCase();
    const find = (files: string[]) =>
      files.find((path) => path.toLowerCase() === lower) ||
      files.find((path) => nameOf(path).toLowerCase() === lower + ".md") ||
      files.find((path) => nameOf(path).toLowerCase().replace(/\.[^.]+$/, "") === lower);
    let match = find(indexRef.current.data?.files || []);
    if (!match) {
      try {
        const data = await api.index();
        setIndex({ data, at: Date.now(), loading: false, error: "" });
        match = find(data.files);
      } catch (error) {
        return notice(errorMessage(error), true);
      }
    }
    if (match) void openFile(match);
    else notice(`No file named ${name} in this workspace`, true);
  };
  const openHit = (hit: Hit) => {
    void openFile(hit.path, { line: hit.line, column: hit.column }, true);
    if (phone) setSide("files");
  };

  function viewFor(item: Doc): ReactNode {
    if (item.loading) return <p className="wb-muted">Opening {nameOf(item.path)}…</p>;
    if (item.error && !item.blob) return <p className="wb-error" role="alert">{item.error}</p>;
    const positionKey = base + ":" + item.path;
    if (item.kind === "image" || (item.kind === "svg" && item.mode === "view")) return <ImageView url={item.url} name={item.path} />;
    if (item.kind === "pdf") return <PdfView blob={item.blob!} positionKey={positionKey} />;
    if (item.kind === "binary")
      return (
        <p className="wb-muted">
          Binary file ({(item.blob?.size || 0).toLocaleString()} bytes). Use Download to open it on your device.
        </p>
      );
    const markdown = (text: string) => <MarkdownFragment text={text} />;
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
        onChange={(value) => {
          typed.current.set(item.path, value);
          update(item.path, { buffer: value });
        }}
        onSave={() => void save(item.path)}
        onLine={(target) => update(item.path, { target })}
        onScrollLine={item.kind === "markdown" ? (line) => update(item.path, { syncLine: line }) : undefined}
        onFallback={(reason) => {
          notice("The full editor could not load (" + reason + "); using the simple view.", true);
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
              onOpenPath={(path) => void openFile(path)}
              onWiki={(name) => void openWiki(name)}
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
          return <NotebookView text={item.buffer} markdown={markdown} />;
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

  const dirty = (item: Doc) => item.buffer !== item.text;
  const setMode = (item: Doc, mode: Mode) => update(item.path, { mode, editing: mode !== "view" ? true : item.editing });
  const markdownTools = doc && doc.kind === "markdown" && doc.mode !== "view" && !doc.readOnly && (full || doc.editing);
  const title = nameOf(info.workdir) || info.workdir;

  const layout = {
    "--wb-explorer": settings.explorerWidth + "px",
    "--wb-editor": settings.editorShare + "%",
  } as React.CSSProperties;
  const drag = (event: React.PointerEvent, which: "explorer" | "editor") => {
    event.preventDefault();
    const row = (event.currentTarget as HTMLElement).closest("#workspace-row")?.getBoundingClientRect();
    if (!row) return;
    const move = (e: PointerEvent) => {
      if (which === "explorer") setSettings((old) => ({ ...old, explorerWidth: Math.round(Math.max(180, Math.min(600, row.right - e.clientX))) }));
      else {
        const explorer = open ? settings.explorerWidth : 0;
        const share = ((row.right - explorer - e.clientX) / (row.width - explorer)) * 100;
        setSettings((old) => ({ ...old, editorShare: Math.round(Math.max(25, Math.min(80, share))) }));
      }
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.classList.remove("wb-resizing");
    };
    document.body.classList.add("wb-resizing");
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
  };

  return (
    <>
      {open && doc && (
        <section id="preview-dialog" className={"wb-editor" + (maximized ? " maximized" : "")} style={layout} aria-label={"File " + doc.path}>
          <div className="wb-resize" role="separator" aria-label="Resize the editor" onPointerDown={(event) => drag(event, "editor")} />
          <div className="wb-tabs" role="tablist" aria-label="Open files">
            {docs.map((item) => (
              <div key={item.path} className={"wb-tab" + (item.path === active ? " active" : "")}>
                <span
                  role="tab"
                  tabIndex={0}
                  aria-selected={item.path === active}
                  title={item.path}
                  onClick={() => setActive(item.path)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") setActive(item.path);
                  }}
                  onAuxClick={(event) => {
                    if (event.button === 1) closeDoc(item.path);
                  }}
                >
                  {nameOf(item.path)}
                  {dirty(item) ? " ●" : ""}
                </span>
                <button className="wb-tab-close" aria-label={"Close tab " + nameOf(item.path)} onClick={() => closeDoc(item.path)}>
                  ×
                </button>
              </div>
            ))}
          </div>
          <div className="wb-doc-head">
            <button className="wb-back" aria-label="Back to files" onClick={() => setActive(undefined)} hidden={!phone}>
              ‹
            </button>
            <span className="wb-doc-path" title={doc.path}>
              {doc.path}
            </span>
            <span className="wb-doc-state" role="status">
              {doc.saving ? "Saving…" : doc.readOnly && doc.truncated ? "Read-only: first 1 MiB" : dirty(doc) ? "Unsaved" : ""}
            </span>
            {rendered(doc.kind) && !doc.loading && (
              <div className="wb-modes" role="group" aria-label="View">
                <button aria-pressed={doc.mode === "view"} onClick={() => setMode(doc, "view")}>
                  Preview
                </button>
                {doc.kind === "markdown" && !phone && (
                  <button aria-pressed={doc.mode === "split"} onClick={() => setMode(doc, "split")}>
                    Split
                  </button>
                )}
                <button aria-pressed={doc.mode === "source"} onClick={() => setMode(doc, "source")}>
                  {doc.kind === "markdown" ? "Edit" : "Source"}
                </button>
              </div>
            )}
            {!full && !doc.readOnly && doc.mode === "source" && !["image", "pdf", "binary"].includes(doc.kind) && (
              <button id="file-edit" aria-pressed={doc.editing} onClick={() => update(doc.path, { editing: !doc.editing })}>
                {doc.editing ? "Done" : "Edit"}
              </button>
            )}
            {!doc.readOnly && !["image", "pdf", "binary"].includes(doc.kind) && (
              <button id="file-save" className="primary" disabled={!dirty(doc) || doc.saving} onClick={() => void save(doc.path)} title="Save (Ctrl+S)">
                Save
              </button>
            )}
            <a id="preview-download" className="button wb-icon-button" href={doc.url || undefined} download={nameOf(doc.path)} aria-label="Download" title="Download">
              <FileIcon name="download" />
            </a>
            <details className="wb-doc-menu" open={menu} onToggle={(event) => setMenu(event.currentTarget.open)}>
              <summary aria-label="File actions" title="File actions">
                ⋯
              </summary>
              <div className="wb-menu" role="menu" onClick={(event) => (event.target as Element).closest("button,a") && setMenu(false)}>
                <button id="preview-insert" role="menuitem" onClick={() => insertPath(doc.path)}>
                  Insert path in terminal
                </button>
                <button role="menuitem" onClick={() => void copyClipboard(doc.path).then(() => notice("Path copied"), (error) => notice(errorMessage(error), true))}>
                  Copy path
                </button>
                <button
                  role="menuitem"
                  onClick={() =>
                    void copyClipboard(fileLink(location, doc.path, doc.target)).then(
                      () => notice(doc.target ? `Link to line ${doc.target.line} copied` : "Link copied"),
                      (error) => notice(errorMessage(error), true),
                    )
                  }
                >
                  Copy link{doc.target ? " to line " + doc.target.line : ""}
                </button>
                <button role="menuitem" onClick={() => setReveal({ path: doc.path, nonce: Date.now() })}>
                  Show in explorer
                </button>
                <button role="menuitem" onClick={() => void reload(doc.path)} disabled={doc.loading}>
                  Reload from disk
                </button>
                {doc.kind === "html" && (
                  <button role="menuitem" aria-pressed={doc.scripts} onClick={() => update(doc.path, { scripts: !doc.scripts })}>
                    {doc.scripts ? "Block scripts" : "Allow scripts (no network)"}
                  </button>
                )}
                <label className="wb-check" role="menuitemcheckbox" aria-checked={settings.autosave}>
                  <input type="checkbox" checked={settings.autosave} onChange={() => setSettings({ ...settings, autosave: !settings.autosave })} /> Autosave
                </label>
                <label className="wb-check" role="menuitemcheckbox" aria-checked={settings.wordWrap}>
                  <input type="checkbox" checked={settings.wordWrap} onChange={() => setSettings({ ...settings, wordWrap: !settings.wordWrap })} /> Word wrap
                </label>
                {full && (
                  <label className="wb-check" role="menuitemcheckbox" aria-checked={settings.minimap}>
                    <input type="checkbox" checked={settings.minimap} onChange={() => setSettings({ ...settings, minimap: !settings.minimap })} /> Minimap
                  </label>
                )}
                <button role="menuitem" onClick={() => setSettings({ ...settings, editor: full ? "simple" : "full" })}>
                  {full ? "Use the simple editor" : "Use the full editor"}
                </button>
                {!phone && (
                  <button role="menuitem" onClick={() => setMaximized(!maximized)}>
                    {maximized ? "Show terminal beside the editor" : "Expand editor over the terminal"}
                  </button>
                )}
              </div>
            </details>
            <button data-close className="wb-close" aria-label="Close file" title="Close file" onClick={() => closeDoc(doc.path)}>
              <FileIcon name="close" />
            </button>
          </div>
          {markdownTools && <MarkdownToolbar editor={editorRef} />}
          {(doc.conflict || doc.disk || (doc.error && doc.blob)) && (
            <div className="wb-banner" role="alert">
              {doc.conflict ? (
                <span>Not saved: {doc.conflict.message}.</span>
              ) : doc.disk === "deleted" ? (
                <span>This file was deleted or moved on disk. Save to write it again.</span>
              ) : doc.disk === "changed" ? (
                <span>This file changed on disk while you were editing.</span>
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
                    Compare
                  </button>
                  <button onClick={() => void save(doc.path, "any")}>Keep mine</button>
                  <button onClick={() => void reload(doc.path)}>Use disk version</button>
                </>
              )}
              {doc.disk === "deleted" && !doc.conflict && <button onClick={() => void save(doc.path, "absent")}>Save again</button>}
              {doc.error && !doc.conflict && !doc.disk && <button onClick={() => update(doc.path, { error: undefined })}>Dismiss</button>}
            </div>
          )}
          <div id="preview-body" className={"wb-body kind-" + doc.kind}>
            <Suspense fallback={<p className="wb-muted">Loading viewer…</p>}>{viewFor(doc)}</Suspense>
          </div>
        </section>
      )}
      <aside id="files-dialog" className={"wb-panel" + (open && doc && phone ? " covered" : "")} hidden={!open} style={layout} aria-label="Workspace files">
        <div className="wb-resize" role="separator" aria-label="Resize the file explorer" onPointerDown={(event) => drag(event, "explorer")} />
        <div className="dialog-head wb-panel-head">
          <div className="wb-side-tabs" role="tablist" aria-label="Files and search">
            <button role="tab" aria-selected={side === "files"} onClick={() => setSide("files")}>
              <FileIcon name="files" /> Files
            </button>
            <button
              role="tab"
              aria-selected={side === "search"}
              id="files-search-tab"
              onClick={() => {
                setSide("search");
                setSearchFocus((value) => value + 1);
              }}
            >
              <FileIcon name="search" /> Search
            </button>
          </div>
          <button
            id="files-quick-open"
            aria-label="Go to file"
            title="Go to file (Ctrl+P; Ctrl+Shift+P in the terminal)"
            onClick={() => {
              ensureIndex();
              setQuick(true);
            }}
          >
            <FileIcon name="goto" />
          </button>
          <button data-close aria-label="Close files" title="Close files (Ctrl+Shift+E)" onClick={() => onOpenChange(false)}>
            <FileIcon name="close" />
          </button>
        </div>
        {message && (
          <p className={"wb-message" + (message.error ? " error" : "")} role={message.error ? "alert" : "status"} onClick={() => setMessage(undefined)}>
            {message.text}
          </p>
        )}
        <div hidden={side !== "files"} className="wb-side-body">
          <Explorer
            api={api}
            storageKey={"lec-files-open:" + base}
            title={title}
            active={active}
            reveal={reveal}
            revision={revision}
            visible={open && side === "files"}
            status={status}
            actions={{
              open: (path) => void openFile(path),
              insert: insertPath,
              copyPath: (path) => void copyClipboard(path).then(() => notice("Path copied"), (error) => notice(errorMessage(error), true)),
              download: (entry) => void download(entry),
              notice,
              changed: (path, to) => {
                setRevision((value) => value + 1);
                ensureIndex(true);
                if (to)
                  setDocs((old) =>
                    old.map((item) => (item.path === path || item.path.startsWith(path + "/") ? { ...item, path: to + item.path.slice(path.length) } : item)),
                  );
                if (to && active && (active === path || active.startsWith(path + "/"))) setActive(to + active.slice(path.length));
              },
              upload,
            }}
          />
        </div>
        <div hidden={side !== "search"} className="wb-side-body">
          <SearchPanel api={api} visible={open && side === "search"} focusKey={searchFocus} onOpen={openHit} />
        </div>
      </aside>
      {quick && (
        <QuickOpen
          index={index.data}
          loading={index.loading}
          error={index.error}
          recent={recent}
          onClose={() => setQuick(false)}
          onOpen={(path, line, column) => {
            setQuick(false);
            void openFile(path, line ? { line, column } : undefined);
          }}
        />
      )}
      {diff && <DiffView diff={diff} onClose={() => setDiff(undefined)} />}
    </>
  );
}

function MarkdownToolbar({ editor }: { editor: React.RefObject<EditorHandle | null> }) {
  const tools: [string, string, () => void][] = [
    ["B", "Bold", () => editor.current?.wrap("**", "**", "bold text")],
    ["I", "Italic", () => editor.current?.wrap("_", "_", "italic text")],
    ["H", "Heading", () => editor.current?.linePrefix("## ")],
    ["`", "Inline code", () => editor.current?.wrap("`", "`", "code")],
    ["Link", "Link", () => editor.current?.wrap("[", "](https://)", "link text")],
    ["•", "Bulleted list", () => editor.current?.linePrefix("- ")],
    ["1.", "Numbered list", () => editor.current?.linePrefix("1. ")],
    ["☐", "Task list", () => editor.current?.linePrefix("- [ ] ")],
    ["❝", "Quote", () => editor.current?.linePrefix("> ")],
    ["Table", "Table", () => editor.current?.insert("\n| Column | Column |\n| --- | --- |\n| Cell | Cell |\n")],
    ["{ }", "Code block", () => editor.current?.wrap("\n```\n", "\n```\n", "code")],
    ["Diagram", "Mermaid diagram", () => editor.current?.insert("\n```mermaid\nflowchart LR\n  A --> B\n```\n")],
  ];
  return (
    <div className="wb-md-tools" role="toolbar" aria-label="Formatting">
      {tools.map(([label, name, action]) => (
        <button key={name} aria-label={name} title={name} onMouseDown={(event) => event.preventDefault()} onClick={action}>
          {label}
        </button>
      ))}
      <span className="wb-md-hint">Type / at the start of a line for blocks</span>
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
        instance = module.createDiff(host.current, diff.disk, diff.mine, languageFor(diff.path), "dark");
      },
      (reason: unknown) => setError(errorMessage(reason)),
    );
    return () => {
      disposed = true;
      instance?.dispose();
    };
  }, [diff]);
  return (
    <div className="wb-quick-backdrop" onPointerDown={(event) => event.target === event.currentTarget && onClose()}>
      <div className="wb-diff" role="dialog" aria-label={"Changes on disk for " + diff.path}>
        <div className="dialog-head">
          <h2>On disk (left) and your edits (right): {nameOf(diff.path)}</h2>
          <button onClick={onClose}>Close</button>
        </div>
        {error ? <p className="wb-error">{error}</p> : <div ref={host} className="wb-diff-host" />}
      </div>
    </div>
  );
}
