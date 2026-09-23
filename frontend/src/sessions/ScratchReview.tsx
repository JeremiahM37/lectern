import { useCallback, useState } from "react";
import type { SessionsApi } from "./Sessions";

interface ScratchEntry {
  name: string;
  path: string;
  target_id: number;
  target_name: string;
  age_days: number;
  size_kb: number;
  verdict: "live" | "project" | "kept" | "work" | "recent" | "empty";
  reasons: string[];
  sessions: { id: number; name: string; agent: string; can_resume: boolean }[];
}
interface ScratchResult {
  days: number;
  targets: { target_name: string; root: string; error?: string; entries: ScratchEntry[] }[];
  trashed: string[];
  purged: number;
}

const size = (kb: number) => (kb < 1024 ? kb + " KiB" : (kb / 1024).toFixed(kb < 10240 ? 1 : 0) + " MiB");
const age = (days: number) => (days < 1 ? "today" : Math.floor(days) + "d idle");

// Scratch directories pile up on disk: every blank shell and project-less
// session makes one. This is the cleanup review, separate from the Scratch
// terminals list above — it inspects leftover directories, not tracked
// sessions. The server removes the empty ones on its own. What is listed here
// is the rest — directories where something happened and no project claims it —
// because only a person can say whether that was work or noise.
export function ScratchReview({
  api,
  onNotice,
}: {
  api: SessionsApi;
  onNotice: (text: string, error?: boolean) => void;
}) {
  const [result, setResult] = useState<ScratchResult>(),
    [loading, setLoading] = useState(false),
    [busy, setBusy] = useState("");
  // Inspecting runs a script on every machine, so it happens when someone asks
  // to look, not every time the session list refreshes.
  const load = useCallback(() => {
    setLoading(true);
    api
      .request<ScratchResult>("/scratch")
      .then(setResult)
      .catch((error) => onNotice(String(error), true))
      .finally(() => setLoading(false));
  }, [api, onNotice]);
  const entries = (result?.targets || []).flatMap((target) => target.entries);
  const work = entries.filter((entry) => entry.verdict === "work");
  const empty = entries.filter((entry) => entry.verdict === "empty");
  const unreachable = (result?.targets || []).filter((target) => target.error);
  const decide = (entry: ScratchEntry, action: "keep" | "discard") => {
    if (
      action === "discard" &&
      !confirm(
        `Discard ${entry.name}?\n\n${entry.reasons.join("\n")}\n\nIt moves to the scratch trash and can be recovered from there until it is purged.`,
      )
    )
      return;
    setBusy(entry.path);
    api
      .request<null>("/scratch/" + action, {
        method: "POST",
        body: { target_id: entry.target_id, name: entry.name },
      })
      .then(load)
      .catch((error) => onNotice(String(error), true))
      .finally(() => setBusy(""));
  };
  return (
    <details
      className="scratch-review"
      id="scratch-review"
      onToggle={(event) => {
        if (event.currentTarget.open && !result && !loading) load();
      }}
    >
      <summary>Scratch directory cleanup</summary>
      <div className="scratch-body">
        {loading && <p className="sub">Inspecting scratch directories…</p>}
        {result && (
          <>
            <p className="sub" id="scratch-summary">
              {work.length
                ? `${work.length} hold work that no project claims. `
                : "Nothing unclaimed is waiting on you. "}
              {empty.length
                ? `${empty.length} are empty and idle over ${result.days} days; the server removes those on its own.`
                : "No empty ones are due for removal."}
            </p>
            {unreachable.map((target) => (
              <p className="sub" key={target.target_name}>
                {target.target_name} could not be inspected: {target.error}
              </p>
            ))}
            {work.map((entry) => (
              <div className="scratch-row" key={entry.target_id + entry.path} data-scratch={entry.name}>
                <div className="scratch-what">
                  <b>{entry.name}</b>
                  <small>
                    {entry.target_name} · {age(entry.age_days)} · {size(entry.size_kb)}
                    {entry.sessions.length
                      ? " · " + entry.sessions.map((session) => session.name).join(", ")
                      : ""}
                  </small>
                  <small className="scratch-why">{entry.reasons.join(" · ")}</small>
                </div>
                <button
                  className="b"
                  disabled={busy === entry.path}
                  title="Never remove this directory automatically"
                  onClick={() => decide(entry, "keep")}
                >
                  Keep
                </button>
                <button
                  className="b danger"
                  disabled={busy === entry.path}
                  title="Move to the scratch trash"
                  onClick={() => decide(entry, "discard")}
                >
                  Discard
                </button>
              </div>
            ))}
            <div className="scratch-actions">
              <button className="b" disabled={loading} onClick={load}>
                Refresh
              </button>
              {empty.length > 0 && (
                <button
                  className="b"
                  id="scratch-sweep"
                  disabled={loading}
                  onClick={() => {
                    setLoading(true);
                    api
                      .request<ScratchResult>("/scratch/sweep", { method: "POST", body: {} })
                      .then((done) => {
                        onNotice(`Removed ${done.trashed.length} empty scratch workspaces.`);
                        load();
                      })
                      .catch((error) => {
                        setLoading(false);
                        onNotice(String(error), true);
                      });
                  }}
                >
                  Remove the {empty.length} empty ones now
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </details>
  );
}
