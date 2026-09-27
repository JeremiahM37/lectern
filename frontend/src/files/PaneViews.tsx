// The file pane views for the workspace (registered in panes.tsx). A file
// pane holds one file; the explorer and search panes open files as panes
// beside themselves. Each follows its session's target through the same file
// API, document handling (docs.tsx) and watch as the terminal page.
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t } from "../i18n";
import type { PaneRef } from "../workspace/layout";
import type { PaneServices } from "../workspace/registry";
import { FileApi, type GitKind } from "./api";
import { DocPanel, dirOf, useFileDocs, useMedia, useSettings } from "./docs";
import { Explorer } from "./Explorer";
import { fileRef } from "./refs";
import { SearchPanel } from "./SearchPanel";
import { useFileWatch } from "./watch";
import { resolveWiki, useExplorerActions } from "./Workbench";

const apiFor = (session: number) => new FileApi(`/api/term/session/${session}`);

export function FilePane({ pane, session, services, close, mobile }: { pane: PaneRef; session: number; services: PaneServices; close: () => void; mobile: boolean }) {
  const api = useMemo(() => apiFor(session), [session]);
  const coarse = useMedia("(pointer: coarse)");
  const [settings, setSettings] = useSettings();
  const full = settings.editor === "full" || (settings.editor === "auto" && !mobile && !coarse);
  const docs = useFileDocs({ api, phone: mobile, notice: services.notice, autosave: settings.autosave });
  const path = pane.params.path || "";
  const line = Number(pane.params.line) || 0;
  useEffect(() => {
    void docs.openFile(path, line ? { line, column: Number(pane.params.column) || undefined } : undefined, !!line);
  }, [path, pane.params.at]);
  useFileWatch(api, [dirOf(path) || "."], !!docs.doc, () => void docs.checkDisk());
  const openBeside = (target: string, at?: number) => services.openPane(fileRef(session, target, at), { beside: pane.id, edge: mobile ? "center" : "right" });
  return (
    <div className="wb-pane">
      <DocPanel
        docs={docs}
        api={api}
        positionPrefix={`/api/term/session/${session}`}
        phone={mobile}
        full={full}
        settings={settings}
        setSettings={setSettings}
        notice={services.notice}
        showTabs={false}
        onOpenPath={(target, at) => openBeside(target, at?.line)}
        onWiki={(name) =>
          void resolveWiki(api, name, undefined, () => {}).then(
            (match) => (match ? openBeside(match) : services.notice(t("files.noWiki", { name }), true)),
            () => services.notice(t("files.noWiki", { name }), true),
          )
        }
        onClosed={close}
        linkBase={{ origin: location.origin, pathname: `/terminal/session/${session}` }}
      />
    </div>
  );
}

export function ExplorerPane({ pane, session, services, mobile }: { pane: PaneRef; session: number; services: PaneServices; mobile: boolean }) {
  const api = useMemo(() => apiFor(session), [session]);
  const [status, setStatus] = useState<Record<string, GitKind>>({});
  const [revision, setRevision] = useState(0);
  const [expanded, setExpanded] = useState<string[]>(["."]);
  const lastOpened = useRef<string>("");
  const changed = useCallback(() => setRevision((value) => value + 1), []);
  useEffect(() => {
    api.gitStatus().then((result) => setStatus(result.status), () => {});
  }, [api, revision]);
  useFileWatch(api, expanded, true, changed);
  const actions = useExplorerActions(api, services.notice, changed, pane.title);
  // Files open beside the explorer, then as tabs next to the last one opened.
  const open = (path: string) => {
    const ref = fileRef(session, path);
    const beside = lastOpened.current && lastOpened.current !== ref.id ? lastOpened.current : "";
    services.openPane(ref, beside ? { beside, edge: "center" } : { beside: pane.id, edge: mobile ? "center" : "right" });
    lastOpened.current = ref.id;
  };
  return (
    <div className="wb-pane wb-panel wb-pane-explorer">
      <Explorer
        api={api}
        storageKey={"lec-files-open:pane:" + session}
        title={services.sessions.find((row) => row.id === session)?.name || pane.title}
        revision={revision}
        status={status}
        onExpanded={setExpanded}
        actions={{ ...actions, open, changed: () => changed() }}
      />
    </div>
  );
}

export function SearchPane({ pane, session, services, mobile, shown }: { pane: PaneRef; session: number; services: PaneServices; mobile: boolean; shown: boolean }) {
  const api = useMemo(() => apiFor(session), [session]);
  return (
    <div className="wb-pane wb-panel wb-pane-search">
      <SearchPanel
        api={api}
        visible={shown}
        focusKey={0}
        onOpen={(hit) => services.openPane(fileRef(session, hit.path, hit.line, hit.column), { beside: pane.id, edge: mobile ? "center" : "right" })}
      />
    </div>
  );
}
