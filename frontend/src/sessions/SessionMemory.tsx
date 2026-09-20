import { useState } from "react";
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
const BASIS: Record<string, string> = {
  managed: "its own provisioned note — an exact link",
  configured: "paths the operator named",
  guessed: "guessed from the project's name",
  all: "the whole store",
  manual: "nothing automatically; agents look things up themselves",
  off: "nothing; project memory is off",
};
interface Activity {
  session: string;
  provider: string;
  status: "ready" | "empty" | "unavailable" | "disabled";
  message?: string;
  counts: Record<string, number>;
  changes: Change[];
}

// What this session's agent wrote to the memory store, read back by the key the
// two share. It used to be invisible: AgentDeck knew what it wrote itself at a
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
          message: "AgentDeck could not be reached.",
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
      <summary>Memory</summary>
      {link && link.status !== "disabled" && (
        <div className="session-memory-link" data-status={link.status}>
          <p className="sub">
            <b>Project memory {link.status}.</b> This project reads{" "}
            {BASIS[link.link.basis] || link.link.basis}.
            {link.status === "unlinked" &&
              " None of those notes exists, so sessions here start with no project memory and nothing says so."}
          </p>
          {link.link.paths.length > 0 && (
            <ul>
              {link.link.paths.map((entry) => (
                <li key={entry.path} data-exists={entry.exists}>
                  {entry.exists ? "✓" : "✗"} <code>{entry.path}</code>
                  {entry.notes > 1 ? ` · ${entry.notes} notes` : ""}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
      {activity && <p className="session-memory-heading">Written by this session</p>}
      {loading && <p className="sub">Asking the memory store…</p>}
      {activity?.status === "disabled" && <p className="sub">No memory provider is configured.</p>}
      {activity?.status === "unavailable" && (
        <p className="sub">
          The memory store could not answer, so this is not “nothing written”. {activity.message}
        </p>
      )}
      {activity?.status === "empty" && (
        <p className="sub">
          Nothing is recorded under <code>{activity.session}</code>. An agent started before
          sessions carried this key, or one whose memory server was not given it, writes under no
          session at all.
        </p>
      )}
      {activity?.status === "ready" && (
        <>
          <p className="sub">
            {Object.entries(activity.counts)
              .filter(([, count]) => count > 0)
              .map(([kind, count]) => `${count} ${kind}`)
              .join(" · ")}{" "}
            under <code>{activity.session}</code>
          </p>
          <ul className="session-memory-list">
            {activity.changes.map((change, index) => (
              <li key={index} data-kind={change.kind}>
                <b>{change.kind}</b> {change.text}
                {change.replaced_text && <small>was: {change.replaced_text}</small>}
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
          Refresh
        </button>
      )}
    </details>
  );
}
