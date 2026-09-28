import { useEffect, useState } from "react";
import { Modal } from "./Modal";
import type { Target } from "../types";
import { t, useLocale } from "../i18n";

export interface FolderListing {
  path: string;
  parent?: string;
  home: string;
  project_id?: number;
  folders: { name: string; path: string; git: boolean; project_id?: number }[];
}

export interface PickedFolder {
  targetId: number;
  path: string;
  /** Set when the folder is already a project. */
  projectId?: number;
}

/** A path shown the way a person thinks of it: ~/code/app rather than /home/me/code/app. */
export function tildePath(path: string, home: string): string {
  if (!home || home === "/") return path;
  if (path === home) return "~";
  return path.startsWith(home + "/") ? "~" + path.slice(home.length) : path;
}

/** Which machine to browse first: the one named, else this computer, else the first. */
export function defaultMachine(targets: Target[], preferred?: number): number | undefined {
  if (preferred && targets.some((row) => row.id === preferred)) return preferred;
  return (targets.find((row) => row.kind === "local") || targets.find((row) => row.kind === "mock") || targets[0])?.id;
}

// "Choose a folder" in Start an agent (docs/design/simple-ui.md): browses a
// machine's folders through GET /api/targets/{id}/folders, starting at home.
// Picking a folder that is not a project yet is fine — starting there makes
// it one.
export function FolderPicker({
  request,
  targets,
  initialTarget,
  onPick,
  onEmpty,
  onClose,
}: {
  request<T>(path: string): Promise<T>;
  targets: Target[];
  initialTarget?: number;
  onPick(folder: PickedFolder): void;
  onEmpty(): void;
  onClose(): void;
}) {
  useLocale();
  const [target, setTarget] = useState(() => defaultMachine(targets, initialTarget));
  const [path, setPath] = useState("");
  const [listing, setListing] = useState<FolderListing>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    if (!target) return;
    let live = true;
    setLoading(true);
    setError("");
    request<FolderListing>(`/targets/${target}/folders?path=${encodeURIComponent(path)}`)
      .then((row) => live && setListing(row))
      .catch((e) => live && setError(String(e instanceof Error ? e.message : e)))
      .finally(() => live && setLoading(false));
    return () => {
      live = false;
    };
  }, [request, target, path]);
  const home = listing?.home || "";
  return (
    <Modal className="sheet folder-picker" id="folder-picker" aria-label={t("folder.title")} onCancel={onClose}>
      <div className="sheet-head">
        <h2>{t("folder.title")}</h2>
        <button className="x" aria-label={t("folder.close")} data-close onClick={onClose}>
          ✕
        </button>
      </div>
      {targets.length > 1 && (
        <label className="session-field">
          {t("folder.machine")}
          <select
            id="folder-machine"
            value={target ?? ""}
            onChange={(e) => {
              setTarget(Number(e.target.value));
              setPath("");
            }}
          >
            {targets.map((row) => (
              <option key={row.id} value={row.id}>
                {row.name}
              </option>
            ))}
          </select>
        </label>
      )}
      <div className="folder-bar">
        <button
          type="button"
          className="b"
          id="folder-up"
          disabled={!listing?.parent || loading}
          title={listing?.parent ? undefined : t("folder.topReason")}
          onClick={() => listing?.parent && setPath(listing.parent)}
        >
          {t("folder.up")}
        </button>
        <code className="folder-path" id="folder-path" title={listing?.path}>
          {listing ? tildePath(listing.path, home) : "…"}
        </code>
        {listing && listing.path !== home && (
          <button type="button" className="linkish" onClick={() => setPath("")}>
            {t("folder.home")}
          </button>
        )}
      </div>
      {error && (
        <p className="subhint error" role="alert">
          {error}
        </p>
      )}
      <ul className="folder-list" aria-busy={loading} aria-label={t("folder.list")}>
        {listing?.folders.map((row) => (
          <li key={row.path}>
            <button type="button" className="folder-row" data-folder={row.name} onClick={() => setPath(row.path)}>
              <span aria-hidden="true">📁</span>
              <span className="folder-name">{row.name}</span>
              {row.project_id ? <span className="chip">{t("folder.project")}</span> : row.git ? <span className="chip">git</span> : null}
            </button>
          </li>
        ))}
        {listing && !listing.folders.length && <li className="subhint">{t("folder.noSubfolders")}</li>}
      </ul>
      <div className="folder-actions">
        <button type="button" className="b" id="folder-empty" onClick={onEmpty}>
          {t("folder.useEmpty")}
        </button>
        <button
          type="button"
          className="b ok"
          id="folder-use"
          disabled={!listing || !target || loading}
          onClick={() => listing && target && onPick({ targetId: target, path: listing.path, projectId: listing.project_id })}
        >
          {t("folder.useThis")}
        </button>
      </div>
      {listing && (
        <p className="subhint" id="folder-hint">
          {listing.project_id ? t("folder.isProject") : t("folder.becomesProject")}
        </p>
      )}
    </Modal>
  );
}
