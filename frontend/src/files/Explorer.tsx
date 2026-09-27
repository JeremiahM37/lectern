import { t } from "../i18n";
import { useCallback, useEffect, useRef, useState } from "react";
import { errorMessage } from "../terminal/model";
import { statusOf, type Entry, type FileApi, type GitKind } from "./api";
import { FileIcon } from "./icons";
import { sortEntries } from "./natsort";

/** Drag data a workspace entry carries: dropped on a terminal it inserts the path. */
export const PATH_TYPE = "application/x-lectern-path";

const LETTER: Record<GitKind, string> = {
  modified: "M",
  added: "A",
  deleted: "D",
  renamed: "R",
  untracked: "U",
  ignored: "",
  conflict: "!",
};

export interface ExplorerActions {
  open: (path: string) => void;
  insert?: (path: string) => void;
  copyPath: (path: string) => void;
  download: (entry: Entry) => void;
  notice: (text: string, error?: boolean) => void;
  changed: (path: string, to?: string) => void;
  upload: (folder: string, files: File[]) => Promise<void>;
}

type Listing = { entries: Entry[] } | { error: string };

interface Menu {
  entry: Entry;
  x: number;
  y: number;
}

interface Draft {
  parent: string;
  kind: "file" | "folder";
}

const parentOf = (path: string) => (path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : ".");
const join = (dir: string, name: string) => (dir === "." ? name : dir + "/" + name);

export function Explorer({
  api,
  storageKey,
  title,
  active,
  reveal,
  revision,
  status,
  actions,
  onExpanded,
}: {
  api: FileApi;
  storageKey: string;
  title: string;
  active?: string;
  reveal?: { path: string; nonce: number };
  revision: number;
  status: Record<string, GitKind>;
  actions: ExplorerActions;
  // The folders shown open, which the file watch follows.
  onExpanded?: (dirs: string[]) => void;
}) {
  const [listings, setListings] = useState<Record<string, Listing>>({});
  const [expanded, setExpanded] = useState<string[]>(() => {
    try {
      const saved = JSON.parse(sessionStorage.getItem(storageKey) || "[]") as string[];
      return saved.includes(".") ? saved : [".", ...saved];
    } catch {
      return ["."];
    }
  });
  const [menu, setMenu] = useState<Menu>();
  const [renaming, setRenaming] = useState<string>();
  const [draft, setDraft] = useState<Draft>();
  const [dropTarget, setDropTarget] = useState<string>();
  const [selected, setSelected] = useState<string>();
  const signatures = useRef<Record<string, string>>({});
  useEffect(() => {
    try {
      sessionStorage.setItem(storageKey, JSON.stringify(expanded));
    } catch {}
    onExpanded?.(expanded);
  }, [expanded, storageKey]);
  const load = useCallback(
    async (dir: string, signal?: AbortSignal) => {
      try {
        const listing = await api.list(dir, signal);
        const entries = sortEntries(listing.entries);
        const signature = JSON.stringify(entries);
        // Polling returns the same listing most of the time; keep React quiet.
        if (signatures.current[dir] === signature) return;
        signatures.current[dir] = signature;
        setListings((old) => ({ ...old, [dir]: { entries } }));
      } catch (error) {
        if (signal?.aborted) return;
        delete signatures.current[dir];
        setListings((old) => ({ ...old, [dir]: { error: errorMessage(error) } }));
      }
    },
    [api],
  );
  const refreshAll = useCallback(
    (signal?: AbortSignal) => {
      // Only folders that are open and still exist; the rest reload when opened.
      for (const dir of expanded.slice(0, 24)) void load(dir, signal);
    },
    [expanded, load],
  );
  useEffect(() => {
    const controller = new AbortController();
    refreshAll(controller.signal);
    return () => controller.abort();
  }, [revision, refreshAll]);
  useEffect(() => {
    if (!reveal) return;
    const parts = reveal.path.split("/");
    const ancestors = parts.slice(0, -1).map((_, index) => parts.slice(0, index + 1).join("/"));
    setExpanded((old) => [...new Set([...old, ...ancestors])]);
    setSelected(reveal.path);
    window.setTimeout(() => document.querySelector(`[data-path="${CSS.escape(reveal.path)}"]`)?.scrollIntoView({ block: "nearest" }), 200);
  }, [reveal?.nonce]);
  useEffect(() => {
    if (!menu) return;
    const close = (event: Event) => {
      if (event instanceof KeyboardEvent && event.key !== "Escape") return;
      if (event.target instanceof Element && event.target.closest(".wb-menu")) return;
      setMenu(undefined);
    };
    document.addEventListener("pointerdown", close, true);
    document.addEventListener("keydown", close, true);
    return () => {
      document.removeEventListener("pointerdown", close, true);
      document.removeEventListener("keydown", close, true);
    };
  }, [menu]);
  const toggle = (dir: string) => {
    if (expanded.includes(dir)) setExpanded(expanded.filter((path) => path !== dir && !path.startsWith(dir + "/")));
    else {
      setExpanded([...expanded, dir]);
      if (!listings[dir]) void load(dir);
    }
  };
  async function run(label: string, work: () => Promise<unknown>) {
    try {
      await work();
    } catch (error) {
      actions.notice(`${label}: ${errorMessage(error)}`, true);
    }
  }
  async function reload(...dirs: string[]) {
    for (const dir of new Set(dirs)) {
      delete signatures.current[dir];
      await load(dir);
    }
  }
  async function create(parent: string, kind: Draft["kind"], name: string) {
    setDraft(undefined);
    if (!name.trim()) return;
    const path = join(parent, name.trim().replace(/^\/+/, ""));
    await run(kind === "file" ? t("files.couldNotCreateFile") : t("files.couldNotCreateFolder"), async () => {
      await api.op(kind === "file" ? "create" : "mkdir", path);
      await reload(parent, parentOf(path));
      actions.changed(path);
      if (kind === "file") actions.open(path);
    });
  }
  async function move(from: string, to: string) {
    if (!to || to === from) return;
    await run(t("files.couldNotMove"), async () => {
      const result = await api.op("rename", from, to);
      await reload(parentOf(from), parentOf(result.path));
      if (expanded.some((dir) => dir === from || dir.startsWith(from + "/")))
        setExpanded((old) => old.map((dir) => (dir === from || dir.startsWith(from + "/") ? result.path + dir.slice(from.length) : dir)));
      actions.changed(from, result.path);
      actions.notice(t("files.moved", { path: result.path }));
    });
  }
  async function remove(entry: Entry) {
    const what = entry.directory ? t("files.deleteFolderWhat", { path: entry.path }) : entry.path;
    if (!window.confirm(t("files.deleteConfirm", { what }))) return;
    await run(t("files.couldNotDelete"), async () => {
      await api.op("delete", entry.path);
      await reload(parentOf(entry.path));
      actions.changed(entry.path);
      actions.notice(t("files.deleted", { path: entry.path }));
    });
  }
  const startDraft = (parent: string, kind: Draft["kind"]) => {
    if (parent !== "." && !expanded.includes(parent)) toggle(parent);
    setDraft({ parent, kind });
  };
  function menuItems(entry: Entry): [string, () => void][] {
    const items: [string, () => void][] = [];
    if (!entry.directory) items.push([t("files.open"), () => actions.open(entry.path)], ...(actions.insert ? [[t("files.insertPath"), () => actions.insert?.(entry.path)] as [string, () => void]] : []));
    else items.push([t("files.newFileHere"), () => startDraft(entry.path, "file")], [t("files.newFolderHere"), () => startDraft(entry.path, "folder")]);
    items.push(
      [t("files.copyPath"), () => actions.copyPath(entry.path)],
      [t("files.rename"), () => setRenaming(entry.path)],
      [
        t("files.moveTo"),
        () => {
          const to = window.prompt(t("files.movePrompt", { path: entry.path }), entry.path);
          if (to) void move(entry.path, to.trim().replace(/^\/+/, ""));
        },
      ],
      [entry.directory ? t("files.downloadFolder") : t("files.download"), () => actions.download(entry)],
      [t("files.delete"), () => void remove(entry)],
    );
    return items;
  }
  function rows(dir: string, depth: number): React.ReactNode[] {
    const listing = listings[dir];
    const out: React.ReactNode[] = [];
    if (draft?.parent === dir)
      out.push(
        <div className="wb-row wb-draft" key={"draft:" + dir} style={{ paddingLeft: 8 + depth * 14 }}>
          <span className="wb-icon" aria-hidden>
            {draft.kind === "folder" ? "▸" : "·"}
          </span>
          <input
            autoFocus
            aria-label={draft.kind === "file" ? t("files.newFileName") : t("files.newFolderName")}
            placeholder={draft.kind === "file" ? "name.ext" : "folder"}
            onKeyDown={(event) => {
              if (event.key === "Enter") void create(dir, draft.kind, event.currentTarget.value);
              if (event.key === "Escape") setDraft(undefined);
            }}
            onBlur={(event) => void create(dir, draft.kind, event.currentTarget.value)}
          />
        </div>,
      );
    if (!listing) {
      out.push(
        <div className="wb-row wb-muted" key={"loading:" + dir} style={{ paddingLeft: 22 + depth * 14 }}>
          {t("files.loading")}
        </div>,
      );
      return out;
    }
    if ("error" in listing) {
      out.push(
        <div className="wb-row wb-error" role="alert" key={"error:" + dir} style={{ paddingLeft: 22 + depth * 14 }}>
          {listing.error}
        </div>,
      );
      return out;
    }
    if (!listing.entries.length && draft?.parent !== dir)
      out.push(
        <div className="wb-row wb-muted" key={"empty:" + dir} style={{ paddingLeft: 22 + depth * 14 }}>
          {dir === "." ? t("files.emptyWorkspace") : t("files.emptyFolder")}
        </div>,
      );
    for (const entry of listing.entries) {
      const open = entry.directory && expanded.includes(entry.path);
      const git = statusOf(status, entry.path, entry.directory);
      out.push(
        <div
          key={entry.path}
          data-path={entry.path}
          className={
            "wb-row" +
            (entry.path === active ? " active" : "") +
            (entry.path === selected ? " selected" : "") +
            (dropTarget === entry.path ? " drop" : "") +
            (git ? " git-" + git : "")
          }
          style={{ paddingLeft: 8 + depth * 14 }}
          draggable={renaming !== entry.path}
          onDragStart={(event) => {
            event.dataTransfer.setData(PATH_TYPE, entry.path);
            event.dataTransfer.setData("text/plain", entry.path);
            event.dataTransfer.effectAllowed = "copyMove";
          }}
          onDragOver={(event) => {
            const types = Array.from(event.dataTransfer.types);
            if (!entry.directory || !(types.includes(PATH_TYPE) || types.includes("Files"))) return;
            event.preventDefault();
            event.stopPropagation();
            setDropTarget(entry.path);
          }}
          onDragLeave={() => setDropTarget((old) => (old === entry.path ? undefined : old))}
          onDrop={(event) => {
            if (!entry.directory) return;
            event.preventDefault();
            event.stopPropagation();
            setDropTarget(undefined);
            const from = event.dataTransfer.getData(PATH_TYPE);
            const files = Array.from(event.dataTransfer.files);
            if (from) void move(from, join(entry.path, from.slice(from.lastIndexOf("/") + 1)));
            else if (files.length) void run(t("files.couldNotUpload"), () => actions.upload(entry.path, files).then(() => reload(entry.path)));
          }}
          onContextMenu={(event) => {
            event.preventDefault();
            setMenu({ entry, x: event.clientX, y: event.clientY });
          }}
        >
          {renaming === entry.path ? (
            <input
              autoFocus
              aria-label={t("files.newName", { name: entry.name })}
              defaultValue={entry.name}
              onFocus={(event) => {
                const dot = entry.directory ? -1 : entry.name.lastIndexOf(".");
                event.currentTarget.setSelectionRange(0, dot > 0 ? dot : entry.name.length);
              }}
              onKeyDown={(event) => {
                if (event.key === "Enter") {
                  setRenaming(undefined);
                  const name = event.currentTarget.value.trim();
                  if (name && name !== entry.name) void move(entry.path, join(parentOf(entry.path), name));
                }
                if (event.key === "Escape") setRenaming(undefined);
              }}
              onBlur={() => setRenaming(undefined)}
            />
          ) : (
            <>
              <span className="wb-icon" aria-hidden>
                {entry.directory ? (open ? "▾" : "▸") : ""}
              </span>
              <button
                className="wb-name"
                title={entry.path + (entry.link ? ` (${t("files.link")})` : "")}
                aria-expanded={entry.directory ? open : undefined}
                onClick={() => {
                  setSelected(entry.path);
                  if (entry.directory) toggle(entry.path);
                  else actions.open(entry.path);
                }}
                onKeyDown={(event) => {
                  if (event.key === "F2") {
                    event.preventDefault();
                    setRenaming(entry.path);
                  }
                  if (event.key === "Delete") {
                    event.preventDefault();
                    void remove(entry);
                  }
                }}
              >
                {entry.name}
              </button>
              {git && git !== "ignored" && (
                <span className="wb-git" title={git}>
                  {LETTER[git]}
                </span>
              )}
              <button
                className="wb-more"
                aria-label={t("files.actionsFor", { name: entry.name })}
                title={t("files.actions")}
                onClick={(event) => {
                  const box = event.currentTarget.getBoundingClientRect();
                  setMenu({ entry, x: box.right, y: box.bottom });
                }}
              >
                ⋯
              </button>
            </>
          )}
        </div>,
      );
      if (open) out.push(...rows(entry.path, depth + 1));
    }
    return out;
  }
  const rootEntry: Entry = { name: title, path: ".", directory: true, size: 0 };
  return (
    <div className="wb-explorer">
      <div className="wb-side-tools" role="toolbar" aria-label={t("files.explorer")}>
        <span className="wb-root" title={title}>
          {title}
        </span>
        <button aria-label={t("files.newFile")} title={t("files.newFile")} onClick={() => startDraft(selectedFolder(selected, listings), "file")}>
          <FileIcon name="file-plus" />
        </button>
        <button aria-label={t("files.newFolder")} title={t("files.newFolder")} onClick={() => startDraft(selectedFolder(selected, listings), "folder")}>
          <FileIcon name="folder-plus" />
        </button>
        <button
          id="files-refresh"
          aria-label={t("files.refresh")}
          title={t("files.refresh")}
          onClick={() => {
            signatures.current = {};
            refreshAll();
            actions.changed(".");
          }}
        >
          <FileIcon name="refresh" />
        </button>
        <button aria-label={t("files.collapse")} title={t("files.collapse")} onClick={() => setExpanded(["."])}>
          <FileIcon name="collapse" />
        </button>
        <button aria-label={t("files.downloadWorkspace")} title={t("files.downloadWorkspace")} onClick={() => actions.download(rootEntry)}>
          <FileIcon name="download" />
        </button>
      </div>
      <div
        id="file-list"
        className="wb-tree"
        onDragOver={(event) => {
          const types = Array.from(event.dataTransfer.types);
          if (types.includes(PATH_TYPE) || types.includes("Files")) {
            event.preventDefault();
            event.stopPropagation();
          }
        }}
        onDrop={(event) => {
          // Dropped on empty space: the workspace root.
          event.preventDefault();
          event.stopPropagation();
          const from = event.dataTransfer.getData(PATH_TYPE);
          const files = Array.from(event.dataTransfer.files);
          if (from && from.includes("/")) void move(from, from.slice(from.lastIndexOf("/") + 1));
          else if (files.length) void run(t("files.couldNotUpload"), () => actions.upload(".", files).then(() => reload(".")));
        }}
      >
        {rows(".", 0)}
      </div>
      {menu && (
        <div className="wb-menu" role="menu" style={{ left: Math.min(menu.x, window.innerWidth - 220), top: Math.min(menu.y, window.innerHeight - 320) }}>
          <p className="wb-menu-title">{menu.entry.path}</p>
          {menuItems(menu.entry).map(([label, action]) => (
            <button
              key={label}
              role="menuitem"
              className={label === t("files.delete") ? "danger" : undefined}
              onClick={() => {
                setMenu(undefined);
                action();
              }}
            >
              {label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function selectedFolder(selected: string | undefined, listings: Record<string, Listing>): string {
  if (!selected) return ".";
  if (listings[selected]) return selected;
  return parentOf(selected);
}
