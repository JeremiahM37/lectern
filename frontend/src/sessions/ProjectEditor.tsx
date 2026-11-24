import { useState } from "react";
import type { Project, SessionView } from "../types";
import type { SessionsApi } from "./Sessions";
import { Modal } from "./Modal";
import { t, useLocale } from "../i18n";

// ProjectEditor files a session under a project, or under none. It only
// changes which project the session belongs to; the agent keeps working in
// the folder it is already in.
export function ProjectEditor({
  api,
  session,
  projects,
  onClose,
  onSaved,
  onNotice,
}: {
  api: SessionsApi;
  session: SessionView;
  projects: Project[];
  onClose: () => void;
  onSaved: () => void;
  onNotice: (text: string, error?: boolean) => void;
}) {
  useLocale();
  const [choice, setChoice] = useState(session.project_id ? String(session.project_id) : ""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  const sorted = [...projects].sort((a, b) => a.name.localeCompare(b.name));
  return (
    <Modal id="sheet" className="sheet" aria-label={t("sessions.projectEditor.title")} onCancel={onClose}>
      <div className="sheet-head">
        <h2>{t("sessions.projectEditor.title")}</h2>
        <button className="x" aria-label={t("sessions.projectEditor.close")} data-close onClick={onClose}>
          ✕
        </button>
      </div>
      <p>{session.name}</p>
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          try {
            await api.request(`/sessions/${session.id}`, {
              method: "PATCH",
              body: { project_id: choice ? Number(choice) : null },
            });
            const project = sorted.find((p) => String(p.id) === choice);
            onNotice(
              project
                ? t("sessions.projectEditor.moved", { project: project.name })
                : t("sessions.projectEditor.removed"),
            );
            onSaved();
            onClose();
          } catch (error) {
            setError(String(error));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label className="f" htmlFor="sp-project">
          {t("sessions.projectEditor.project")}
        </label>
        <select
          className="f"
          id="sp-project"
          value={choice}
          onChange={(event) => setChoice(event.target.value)}
          autoFocus
        >
          <option value="">{t("sessions.projectEditor.none")}</option>
          {sorted.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
              {p.repo_path ? ` — ${p.repo_path}` : ""}
            </option>
          ))}
        </select>
        <p className="subhint">{t("sessions.projectEditor.hint")}</p>
        <p id="sp-error" role="status">
          {error}
        </p>
        <button className="b ok" id="sp-save" disabled={busy}>
          {t("sessions.projectEditor.save")}
        </button>
      </form>
    </Modal>
  );
}
