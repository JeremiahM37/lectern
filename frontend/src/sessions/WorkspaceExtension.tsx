import { useCallback, useEffect, useRef, useState } from "react";
import { t, useLocale } from "../i18n";
import type { InteractiveWorkspace, Project, SessionView } from "../types";
import { Modal } from "./Modal";
import type { SessionsApi } from "./Sessions";
interface Operation {
  id: number;
  state: string;
  cancel_requested?: boolean;
  error?: string;
}
const active = (op?: Operation) =>
  !!op && ["running", "recovering"].includes(op.state);
export function WorkspaceExtension({
  api,
  session,
  onClose,
  onChanged,
  onNotice,
}: {
  api: SessionsApi;
  session: SessionView;
  onClose(): void;
  onChanged(): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [projects, setProjects] = useState<Project[]>([]),
    [workspace, setWorkspace] = useState<InteractiveWorkspace>(),
    [operation, setOperation] = useState<Operation>(),
    [project, setProject] = useState(""),
    [base, setBase] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  const generation = useRef(0),
    controller = useRef<AbortController | null>(null),
    mounted = useRef(true),
    operationRef = useRef<Operation>(undefined);
  const root = `/sessions/${session.id}/worktree`;
  const refresh = useCallback(async () => {
    const current = ++generation.current;
    controller.current?.abort();
    const abort = new AbortController();
    controller.current = abort;
    try {
      const [p, w, ops] = await Promise.all([
        api.request<Project[]>("/projects", { signal: abort.signal }),
        api.request<InteractiveWorkspace>(root, { signal: abort.signal }),
        api.request<Operation[]>(root + "/operations", {
          signal: abort.signal,
        }),
      ]);
      if (!mounted.current || current !== generation.current) return;
      setProjects(p);
      setWorkspace(w);
      setOperation(ops[0]);
      operationRef.current = ops[0];
      setError("");
    } catch (e) {
      if (!abort.signal.aborted && mounted.current)
        setError(e instanceof Error ? e.message : String(e));
    }
  }, [api, root]);
  useEffect(() => {
    mounted.current = true;
    void refresh();
    const timer = window.setInterval(() => {
      if (active(operationRef.current) && document.visibilityState !== "hidden")
        void refresh();
    }, 1500);
    return () => {
      mounted.current = false;
      clearInterval(timer);
      controller.current?.abort();
    };
  }, [refresh]);
  const repositories = workspace?.repositories || [];
  const available = projects.filter(
    (p) =>
      p.target_id === session.target_id &&
      !repositories.some(
        (r) => r.project_id === p.id || r.worktree.repo === p.repo_path,
      ),
  );
  useEffect(() => {
    if (!available.some((p) => String(p.id) === project))
      setProject(available[0] ? String(available[0].id) : "");
  }, [projects, workspace, project]);
  const ready =
    workspace?.state === "ready" &&
    repositories.every((r) => r.worktree.state === "ready");
  async function mutate(kind: "add" | "cancel" | "recover") {
    if (busy || (kind === "add" ? !project : !operation)) return;
    setBusy(true);
    setError("");
    try {
      const op = await api.request<Operation>(
        kind === "add"
          ? root + "/repositories"
          : `${root}/operations/${operation!.id}/${kind}`,
        {
          method: "POST",
          body:
            kind === "add"
              ? { project_id: Number(project), base: base.trim() }
              : {},
        },
      );
      if (mounted.current) {
        setOperation(op);
        operationRef.current = op;
        await refresh();
      }
      onChanged();
    } catch (e) {
      if (mounted.current) setError(e instanceof Error ? e.message : String(e));
      else onNotice(String(e), true);
    } finally {
      if (mounted.current) setBusy(false);
    }
  }
  const status =
    error ||
    (!workspace
      ? t("sessions.workspaceExtension.loading")
      : operation
        ? `${t("sessions.workspaceExtension.operation", { id: operation.id, state: operation.state })}${operation.cancel_requested ? " · " + t("sessions.workspaceExtension.cancelRequested") : ""}${operation.error ? " · " + operation.error : ""}`
        : ready
          ? available.length
            ? t("sessions.workspaceExtension.ready")
            : t("sessions.workspaceExtension.noOthers")
          : t("sessions.workspaceExtension.needsRecovery"));
  return (
    <Modal
      className="launch-profiles"
      aria-label={t("sessions.workspaceExtension.title")}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <header>
        <h2>{t("sessions.workspaceExtension.title")}</h2>
        <button type="button" onClick={onClose}>
          {t("sessions.workspaceExtension.close")}
        </button>
      </header>
      <p>
        {t("sessions.workspaceExtension.intro")}
      </p>
      <pre
        className="we-progress"
        style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}
      >
        {repositories
          .map(
            (r) =>
              `${r.name}: ${r.worktree.state}\n${r.worktree.setup_output || r.worktree.error || ""}`,
          )
          .join("\n")}
      </pre>
      {ready && !active(operation) && available.length > 0 && (
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void mutate("add");
          }}
        >
          <label>
            {t("sessions.workspaceExtension.project")}
            <select
              aria-label={t("sessions.workspaceExtension.project")}
              required
              value={project}
              onChange={(e) => setProject(e.target.value)}
            >
              {available.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            {t("sessions.workspaceExtension.base")}
            <input
              maxLength={512}
              value={base}
              onChange={(e) => setBase(e.target.value)}
              placeholder={t("sessions.workspaceExtension.basePlaceholder")}
            />
          </label>
          <p className="we-hook">
            {available.find((p) => String(p.id) === project)?.setup_cmd ||
              t("sessions.workspaceExtension.noSetup")}
          </p>
          <button type="submit" disabled={busy || !project}>
            {t("sessions.workspaceExtension.add")}
          </button>
        </form>
      )}
      <p className="we-status" role="status">
        {status}
      </p>
      <div className="lp-buttons">
        <button type="button" disabled={busy} onClick={() => void refresh()}>
          {t("sessions.workspaceExtension.refresh")}
        </button>
        {active(operation) && (
          <button
            type="button"
            disabled={busy}
            onClick={() => void mutate("cancel")}
          >
            {t("sessions.workspaceExtension.cancel")}
          </button>
        )}
        {operation?.state === "recovering" && (
          <button
            type="button"
            disabled={busy}
            onClick={() => void mutate("recover")}
          >
            {t("sessions.workspaceExtension.recover")}
          </button>
        )}
      </div>
    </Modal>
  );
}
