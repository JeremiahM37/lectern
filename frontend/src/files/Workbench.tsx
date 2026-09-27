// The workspace's files next to its terminal: an explorer, tabs of open files
// with an editor and viewers, Quick Open and project search. Everything is
// read and written on the session's target through the file API; see
// docs/files.md for the layout and the rules. Open files are handled by
// docs.tsx, which the workspace's file panes share.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../i18n";
import { chordsFor, useShortcuts } from "../shortcuts/dispatch";
import { copyClipboard, downloadBlob, errorMessage, quote, type TerminalInfo } from "../terminal/model";
import { ConflictError, FileApi, type Entry, type GitKind, type Hit, type Index } from "./api";
import { lineHash, parseLineHash, readDeepLink, type LineTarget } from "./deeplink";
import { DocPanel, dirOf, nameOf, useFileDocs, useMedia, useSettings } from "./docs";
import { Explorer, PATH_TYPE } from "./Explorer";
import { FileIcon } from "./icons";
import { QuickOpen } from "./QuickOpen";
import { SearchPanel } from "./SearchPanel";
import { useFileWatch } from "./watch";
import "./files.css";

export interface FileRequest {
  path: string;
  target?: LineTarget;
  nonce: number;
}

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
  const [settings, setSettings] = useSettings();
  const full = settings.editor === "full" || (settings.editor === "auto" && !phone && !coarse);
  const [side, setSide] = useState<"files" | "search">("files");
  const [searchFocus, setSearchFocus] = useState(0);
  const [maximized, setMaximized] = useState(false);
  const [quick, setQuick] = useState(false);
  const [index, setIndex] = useState<{ data?: Index; at: number; loading: boolean; error: string }>({ at: 0, loading: false, error: "" });
  const [status, setStatus] = useState<Record<string, GitKind>>({});
  const [revision, setRevision] = useState(0);
  const [expanded, setExpanded] = useState<string[]>(["."]);
  const [reveal, setReveal] = useState<{ path: string; nonce: number }>();
  const [message, setMessage] = useState<{ text: string; error: boolean }>();
  const recentKey = "lec-files-recent:" + base;
  const [recent, setRecent] = useState<string[]>(() => {
    try {
      return JSON.parse(localStorage.getItem(recentKey) || "[]") as string[];
    } catch {
      return [];
    }
  });
  const notice = useCallback((text: string, error = false) => {
    setMessage({ text, error });
  }, []);
  useEffect(() => {
    if (!message || message.error) return;
    const timer = window.setTimeout(() => setMessage(undefined), 4000);
    return () => window.clearTimeout(timer);
  }, [message]);
  const docs = useFileDocs({ api, phone, notice, autosave: settings.autosave, onSaved: () => setRevision((value) => value + 1) });
  const doc = docs.doc;

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
  // Warm the index shortly after the terminal loads, so the first Go to file is instant.
  useEffect(() => {
    const timer = window.setTimeout(() => ensureIndex(), 1500);
    return () => window.clearTimeout(timer);
  }, [ensureIndex]);

  // ---- git status colours, reloaded whenever the workspace changes.
  const loadStatus = useCallback(() => {
    api.gitStatus().then(
      (result) => setStatus((old) => (JSON.stringify(old) === JSON.stringify(result.status) ? old : result.status)),
      () => {},
    );
  }, [api]);
  useEffect(() => {
    if (open) loadStatus();
  }, [open, revision, loadStatus]);
  // The explorer stays mounted while the panel is closed; opening it reads
  // the folders again rather than showing what was there before.
  useEffect(() => {
    if (open) setRevision((value) => value + 1);
  }, [open]);

  // ---- live: the target says when a shown folder or an open file changes.
  const watchDirs = useMemo(() => [...expanded, ...docs.docs.map((item) => dirOf(item.path) || ".")], [expanded, docs.docs]);
  const watchMode = useFileWatch(api, watchDirs, open || docs.docs.length > 0, () => {
    setRevision((value) => value + 1);
    void docs.checkDisk();
  });

  // ---- opening files
  const openFile = useCallback(
    (path: string, target?: LineTarget, preferSource = false) => {
      onOpenChange(true);
      setRecent((old) => {
        const next = [path, ...old.filter((item) => item !== path)].slice(0, 20);
        try {
          localStorage.setItem(recentKey, JSON.stringify(next));
        } catch {}
        return next;
      });
      return docs.openFile(path, target, preferSource);
    },
    [docs.openFile, onOpenChange, recentKey],
  );
  useEffect(() => {
    if (request) void openFile(request.path, request.target);
  }, [request?.nonce]);
  useEffect(() => {
    const link = readDeepLink(location.search, location.hash);
    if (link) void openFile(link.path, link.target);
  }, []);
  const showQuick = () => {
    ensureIndex();
    setQuick(true);
  };
  const showSearch = () => {
    onOpenChange(true);
    setSide("search");
    setSearchFocus((value) => value + 1);
  };
  useEffect(() => {
    if (!command) return;
    if (command.name === "quick") showQuick();
    else showSearch();
  }, [command?.nonce]);

  // Keys come from the shortcuts registry (Settings → Shortcuts can remap
  // them). Ctrl+P stays the shell's while the terminal itself has focus.
  useShortcuts({
    "files.goToFile": (event) => {
      if (event && event.ctrlKey && !event.altKey && !event.metaKey && event.target instanceof Element && event.target.closest(".terminal-host")) return false;
      showQuick();
    },
    "files.search": () => showSearch(),
  });

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
        onNotice(t("files.pathInserted", { path }));
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
  const actions = useExplorerActions(api, notice, () => setRevision((value) => value + 1), nameOf(info.workdir) || "workspace");
  // [[name]] links resolve against the file index; a name the cached index
  // does not know is looked up again, in case the file is new.
  const openWiki = async (name: string) => {
    const match = await resolveWiki(api, name, indexRef.current.data?.files, (data) => setIndex({ data, at: Date.now(), loading: false, error: "" }));
    if (match) void openFile(match);
    else notice(t("files.noWiki", { name }), true);
  };
  const openHit = (hit: Hit) => {
    void openFile(hit.path, { line: hit.line, column: hit.column }, true);
    if (phone) setSide("files");
  };

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
  const chord = chordsFor("files.goToFile")[0] || "";

  return (
    <>
      {open && doc && (
        <DocPanel
          docs={docs}
          api={api}
          positionPrefix={base}
          phone={phone}
          full={full}
          settings={settings}
          setSettings={setSettings}
          notice={notice}
          showTabs
          sectionId="preview-dialog"
          className={maximized ? "maximized" : ""}
          style={layout}
          before={<div className="wb-resize" role="separator" aria-label={t("files.resizeEditor")} onPointerDown={(event) => drag(event, "editor")} />}
          onBack={() => docs.setActive(undefined)}
          onInsert={insertPath}
          onReveal={(path) => setReveal({ path, nonce: Date.now() })}
          onOpenPath={(path, target) => void openFile(path, target)}
          onWiki={(name) => void openWiki(name)}
          linkBase={location}
          extraMenu={
            !phone && (
              <button role="menuitem" onClick={() => setMaximized(!maximized)}>
                {maximized ? t("files.unexpand") : t("files.expand")}
              </button>
            )
          }
        />
      )}
      <aside id="files-dialog" className="wb-panel" data-back-overlay="10" hidden={!open} style={layout} aria-label={t("files.title")} data-watch={watchMode}>
        <div className="wb-resize" role="separator" aria-label={t("files.resizeExplorer")} onPointerDown={(event) => drag(event, "explorer")} />
        <div className="dialog-head wb-panel-head">
          <div className="wb-side-tabs" role="tablist" aria-label={t("files.title")}>
            <button role="tab" aria-selected={side === "files"} onClick={() => setSide("files")}>
              <FileIcon name="files" /> {t("files.tabFiles")}
            </button>
            <button role="tab" aria-selected={side === "search"} id="files-search-tab" onClick={showSearch}>
              <FileIcon name="search" /> {t("files.tabSearch")}
            </button>
          </div>
          <span className={"wb-live " + watchMode} title={watchMode === "fallback" ? t("files.watchPolling") : t("files.watchLive")} aria-hidden />
          <button id="files-quick-open" aria-label={t("files.goToFile")} title={t("files.goToFileHint", { chord })} onClick={showQuick}>
            <FileIcon name="goto" />
          </button>
          <button data-close aria-label={t("files.close")} title={t("files.close")} onClick={() => onOpenChange(false)}>
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
            active={docs.active}
            reveal={reveal}
            revision={revision}
            status={status}
            onExpanded={setExpanded}
            actions={{
              ...actions,
              open: (path) => void openFile(path),
              insert: insertPath,
              changed: (path, to) => {
                setRevision((value) => value + 1);
                ensureIndex(true);
                if (to) docs.remap(path, to);
              },
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
    </>
  );
}

/** Explorer actions that do not depend on where the explorer is shown. */
export function useExplorerActions(api: FileApi, notice: (text: string, error?: boolean) => void, changed: () => void, workspaceName: string) {
  const download = async (entry: Entry) => {
    try {
      if (entry.directory) {
        notice(t("files.preparing", { name: entry.path === "." ? t("files.theWorkspace") : entry.name }));
        downloadBlob(await api.archive(entry.path), (entry.path === "." ? workspaceName : entry.name) + ".zip");
      } else downloadBlob((await api.open(entry.path)).blob, entry.name);
    } catch (error) {
      notice(t("files.downloadFailed", { message: errorMessage(error) }), true);
    }
  };
  const upload = async (folder: string, files: File[]) => {
    for (const file of files) {
      const path = (folder === "." ? "" : folder + "/") + file.name;
      try {
        await api.save(path, file, "absent");
      } catch (error) {
        if (error instanceof ConflictError) {
          if (!window.confirm(t("files.replace", { path }))) continue;
          await api.save(path, file, "any");
        } else throw error;
      }
    }
    notice(t("files.added", { what: files.length === 1 ? files[0]!.name : t("files.nFiles", { count: files.length }), folder: folder === "." ? t("files.theWorkspace") : folder }));
    changed();
  };
  return {
    download: (entry: Entry) => void download(entry),
    upload,
    notice,
    copyPath: (path: string) => void copyClipboard(path).then(() => notice(t("files.pathCopied")), (error) => notice(errorMessage(error), true)),
  };
}

export async function resolveWiki(api: FileApi, name: string, known: string[] | undefined, fresh: (index: Index) => void): Promise<string | undefined> {
  const lower = name.toLowerCase();
  const find = (files: string[]) =>
    files.find((path) => path.toLowerCase() === lower) ||
    files.find((path) => nameOf(path).toLowerCase() === lower + ".md") ||
    files.find((path) => nameOf(path).toLowerCase().replace(/\.[^.]+$/, "") === lower);
  const match = find(known || []);
  if (match) return match;
  const data = await api.index();
  fresh(data);
  return find(data.files);
}
