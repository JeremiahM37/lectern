import { useState } from "react";
import { t, useLocale } from "../i18n";
import type { SessionsApi } from "./Sessions";

interface Change {
  kind: "learned" | "changed" | "retracted" | "expired";
  at: string;
  text: string;
  path: string;
  agent: string;
  topic: string;
  replaced_text?: string;
}
interface ProjectLink {
  status: "linked" | "unlinked" | "unavailable" | "disabled";
  topic: string;
  link: { mode: string; basis: string; paths: { path: string; exists: boolean; notes: number }[] };
}
const BASIS = (): Record<string, string> => ({
  managed: t("sessions.memory.basis.managed"),
  configured: t("sessions.memory.basis.configured"),
  guessed: t("sessions.memory.basis.guessed"),
  all: t("sessions.memory.basis.all"),
  manual: t("sessions.memory.basis.manual"),
  off: t("sessions.memory.basis.off"),
});
interface Activity {
  session: string;
  provider: string;
  status: "ready" | "empty" | "unavailable" | "disabled";
  message?: string;
  counts: Record<string, number>;
  changes: Change[];
}

// What this session's agent wrote to the memory store, read back by the key the
// two share. It used to be invisible: Lectern knew what it wrote itself at a
// handoff and nothing of what the agent remembered on its own. The status is
// spelled out because an empty list has three causes, and only one of them is
// "the agent wrote nothing".
export function SessionMemory({
  api,
  sessionId,
  projectId,
}: {
  api: SessionsApi;
  sessionId: number;
  projectId: number | null;
}) {
  useLocale();
  const [activity, setActivity] = useState<Activity>(),
    [link, setLink] = useState<ProjectLink>(),
    [loading, setLoading] = useState(false);
  // Asked for when someone opens it: one request per look, not one per card per
  // refresh of the session list.
  const load = () => {
    setLoading(true);
    if (projectId != null)
      api
        .request<ProjectLink>(`/projects/${projectId}/memory`)
        .then(setLink)
        .catch(() => setLink(undefined));
    api
      .request<Activity>(`/sessions/${sessionId}/memory`)
      .then(setActivity)
      .catch(() =>
        setActivity({
          session: "",
          provider: "",
          status: "unavailable",
          message: t("sessions.memory.unreachable"),
          counts: {},
          changes: [],
        }),
      )
      .finally(() => setLoading(false));
  };
  return (
    <details
      className="session-memory"
      onToggle={(event) => {
        if (event.currentTarget.open && !activity && !loading) load();
      }}
    >
      <summary>{t("sessions.memory.summary")}</summary>
      {link && link.status !== "disabled" && (
        <div className="session-memory-link" data-status={link.status}>
          <p className="sub">
            <b>{t("sessions.memory.projectStatus", { status: link.status })}</b>{" "}
            {t("sessions.memory.reads", { basis: BASIS()[link.link.basis] || link.link.basis })}
            {link.status === "unlinked" &&
              " " + t("sessions.memory.unlinkedNote")}
          </p>
          {link.link.paths.length > 0 && (
            <ul>
              {link.link.paths.map((entry) => (
                <li key={entry.path} data-exists={entry.exists}>
                  {entry.exists ? "✓" : "✗"} <code>{entry.path}</code>
                  {entry.notes > 1 ? " · " + t("sessions.memory.notes", { n: entry.notes }) : ""}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {activity && <p className="session-memory-heading">{t("sessions.memory.writtenHeading")}</p>}
      {loading && <p className="sub">{t("sessions.memory.asking")}</p>}
      {activity?.status === "disabled" && <p className="sub">{t("sessions.memory.noProvider")}</p>}
      {activity?.status === "unavailable" && (
        <p className="sub">
          {t("sessions.memory.unavailable")} {activity.message}
        </p>
      )}
      {activity?.status === "empty" && (
        <p className="sub">
          {t("sessions.memory.emptyBefore")}{" "}<code>{activity.session}</code>{t("sessions.memory.emptyAfter")}
        </p>
      )}
      {activity?.status === "ready" && (
        <>
          <p className="sub">
            {Object.entries(activity.counts)
              .filter(([, count]) => count > 0)
              .map(([kind, count]) => t("sessions.memory.kindCount", { n: count, kind }))
              .join(" · ")}{" "}
            {t("sessions.memory.under")}{" "}<code>{activity.session}</code>
          </p>
          <ul className="session-memory-list">
            {activity.changes.map((change, index) => (
              <li key={index} data-kind={change.kind}>
                <b>{change.kind}</b> {change.text}
                {change.replaced_text && <small>{t("sessions.memory.was", { text: change.replaced_text })}</small>}
                <small>
                  {change.agent}
                  {change.path ? " · " + change.path : ""}
                </small>
              </li>
            ))}
          </ul>
        </>
      )}
      {activity && (
        <button className="b" disabled={loading} onClick={load}>
          {t("sessions.memory.refresh")}
        </button>
      )}
    </details>
  );
}
