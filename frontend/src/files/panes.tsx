// Workspace pane types for files: a file (editor and viewers), a session's
// explorer, and its project search. They can sit in any split, be dragged,
// saved in layouts and shown on phones like every other pane. The views load
// on first use (PaneViews.tsx). Go to file in the workspace (Ctrl+P by
// default, from the shortcuts registry) opens a file pane for the session in
// front.
import { lazy, Suspense } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../i18n";
import { registerShortcut } from "../shortcuts/dispatch";
import { filesContext, openFilePane, sessionIndex } from "../workspace/files-provider";
import { registerPaneType } from "../workspace/registry";
import type { PaneRef } from "../workspace/layout";
import { QuickOpen } from "./QuickOpen";
import "./files.css";

export { fileRef, filesRef, searchRef } from "./refs";

const FilePane = lazy(() => import("./PaneViews").then((m) => ({ default: m.FilePane })));
const ExplorerPane = lazy(() => import("./PaneViews").then((m) => ({ default: m.ExplorerPane })));
const SearchPane = lazy(() => import("./PaneViews").then((m) => ({ default: m.SearchPane })));

const session = (ref: PaneRef) => (/^[1-9]\d{0,12}$/.test(ref.params.session || "") ? Number(ref.params.session) : 0);
const loading = () => <p className="wb-muted">{t("files.loadingViewer")}</p>;

registerPaneType({
  kind: "file",
  label: t("files.paneFile"),
  icon: "≡",
  check: (ref) => {
    const id = session(ref),
      path = ref.params.path || "";
    // A workspace path, or an absolute or ~/ one outside it (read-only).
    return id && path && !path.startsWith("//") && !path.split("/").includes("..") && ref.id === `file:session:${id}:${path}` ? ref : null;
  },
  popout: (ref) => `/terminal/session/${session(ref)}?open=${encodeURIComponent(ref.params.path || "")}${ref.params.line ? "#L" + ref.params.line : ""}`,
  render: ({ pane, services, close, mobile }) => (
    <Suspense fallback={loading()}>
      <FilePane pane={pane} session={session(pane)} services={services} close={close} mobile={mobile} />
    </Suspense>
  ),
});
registerPaneType({
  kind: "files",
  label: t("files.paneExplorer"),
  icon: "▤",
  check: (ref) => (session(ref) && ref.id === `files:session:${session(ref)}` ? ref : null),
  render: ({ pane, services, mobile }) => (
    <Suspense fallback={loading()}>
      <ExplorerPane pane={pane} session={session(pane)} services={services} mobile={mobile} />
    </Suspense>
  ),
});
registerPaneType({
  kind: "search",
  label: t("files.paneSearch"),
  icon: "⌕",
  check: (ref) => (session(ref) && ref.id === `search:session:${session(ref)}` ? ref : null),
  render: ({ pane, services, mobile, shown }) => (
    <Suspense fallback={loading()}>
      <SearchPane pane={pane} session={session(pane)} services={services} mobile={mobile} shown={shown} />
    </Suspense>
  ),
});

// Go to file over the workspace: the same overlay and ranking as the
// terminal page, for the session whose pane is in front.
let root: Root | undefined;
function closeQuick() {
  root?.unmount();
  root = undefined;
  document.getElementById("workspace-quick-open")?.remove();
}
registerShortcut("workspace.goToFile", () => {
  const current = filesContext();
  if (!current) return false;
  closeQuick();
  const host = document.createElement("div");
  host.id = "workspace-quick-open";
  document.body.append(host);
  root = createRoot(host);
  const render = (state: { index?: { files: string[]; ignored: string[] }; loading: boolean; error: string }) =>
    root?.render(
      <QuickOpen
        index={state.index && { ...state.index, truncated: false, source: "", elapsed_ms: 0 }}
        loading={state.loading}
        error={state.error}
        recent={[]}
        onClose={closeQuick}
        onOpen={(path, line, column) => {
          closeQuick();
          openFilePane(current, path, line, column);
        }}
      />,
    );
  render({ loading: true, error: "" });
  sessionIndex(current.session).then(
    (index) => render({ index, loading: false, error: "" }),
    (error: unknown) => render({ loading: false, error: error instanceof Error ? error.message : String(error) }),
  );
});
