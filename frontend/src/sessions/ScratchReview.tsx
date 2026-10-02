import { useCallback, useEffect, useRef, useState } from "react";
import { t, useLocale } from "../i18n";
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

const size = (kb: number) =>
  kb < 1024
    ? t("sessions.scratchReview.kib", { size: kb })
    : t("sessions.scratchReview.mib", { size: (kb / 1024).toFixed(kb < 10240 ? 1 : 0) });
const age = (days: number) =>
  days < 1 ? t("sessions.scratchReview.today") : t("sessions.scratchReview.idleDays", { days: Math.floor(days) });

// Scratch directories pile up on disk: every blank shell and project-less
// session makes one. This is the cleanup review, separate from the Scratch
// terminals list above — it inspects leftover directories, not tracked
// sessions. The server removes the empty ones on its own. What is listed here
// is the rest — directories where something happened and no project claims it —
// because only a person can say whether that was work or noise.
export function ScratchReview({
  api,
  onNotice,
  startOpen,
}: {
  api: SessionsApi;
  onNotice: (text: string, error?: boolean) => void;
  /** Asked for from the Sessions ⋯ menu: open (and inspect) straight away. */
  startOpen?: boolean;
}) {
  useLocale();
  const ref = useRef<HTMLDetailsElement>(null);
  useEffect(() => {
    if (startOpen && ref.current) {
      ref.current.open = true;
      ref.current.scrollIntoView({ block: "nearest" });
    }
  }, [startOpen]);
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
        t("sessions.scratchReview.confirmDiscard", { name: entry.name, reasons: entry.reasons.join("\n") }),
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
      ref={ref}
      className="scratch-review"
      id="scratch-review"
      onToggle={(event) => {
        if (event.currentTarget.open && !result && !loading) load();
      }}
    >
      <summary>{t("sessions.scratchReview.summary")}</summary>
      <div className="scratch-body">
        {loading && <p className="sub">{t("sessions.scratchReview.inspecting")}</p>}
        {result && (
          <>
            <p className="sub" id="scratch-summary">
              {work.length
                ? t("sessions.scratchReview.holdWork", { n: work.length })
                : t("sessions.scratchReview.nothingUnclaimed")}{" "}
              {empty.length
                ? t("sessions.scratchReview.emptyIdle", { n: empty.length, days: result.days })
                : t("sessions.scratchReview.noneDue")}
            </p>
            {unreachable.map((target) => (
              <p className="sub" key={target.target_name}>
                {t("sessions.scratchReview.uninspectable", { target: target.target_name, error: target.error ?? "" })}
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
                  title={t("sessions.scratchReview.keepTitle")}
                  onClick={() => decide(entry, "keep")}
                >
                  {t("sessions.scratchReview.keep")}
                </button>
                <button
                  className="b danger"
                  disabled={busy === entry.path}
                  title={t("sessions.scratchReview.discardTitle")}
                  onClick={() => decide(entry, "discard")}
                >
                  {t("sessions.scratchReview.discard")}
                </button>
              </div>
            ))}
            <div className="scratch-actions">
              <button className="b" disabled={loading} onClick={load}>
                {t("sessions.memory.refresh")}
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
                        onNotice(t("sessions.scratchReview.removed", { n: done.trashed.length }));
                        load();
                      })
                      .catch((error) => {
                        setLoading(false);
                        onNotice(String(error), true);
                      });
                  }}
                >
                  {t("sessions.scratchReview.removeEmpty", { n: empty.length })}
                </button>
              )}
            </div>
          </>
        )}
      </div>
    </details>
  );
}
