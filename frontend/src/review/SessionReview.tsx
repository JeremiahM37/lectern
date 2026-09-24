import { useEffect, useRef, useState } from "react";
import { Modal } from "../sessions/Modal";
import { DiffViewer } from "./DiffViewer";
import { CommentTray } from "./CommentTray";
import { nextDraftKey, toWireComments } from "./types";
import type { DraftComment, DiffResponse } from "./types";
import type { JsonValue } from "../api";
import type { SessionCheck } from "../types";

// A subset of the app's `api.request`, the same seam TaskDetail uses — so
// this panel can be opened both from the top-level app (the full DeckApi)
// and from inside Conversation.tsx, which only carries this much.
export interface SessionReviewApi {
  request<T>(
    path: string,
    options?: { method?: string; body?: JsonValue },
  ): Promise<T>;
}

/** The "Review & merge" panel for a session: its live diff (across every
 * repository in a grouped workspace), click-to-comment, a pending-comments
 * tray with "Send to agent", and a commit/push/PR form with "Generate
 * description". Reachable from the session card and the Conversation view. */
export function SessionReview({
  api,
  sessionId,
  name,
  onClose,
  onNotice,
}: {
  api: SessionReviewApi;
  sessionId: number;
  name?: string;
  onClose(): void;
  onNotice(text: string, error?: boolean): void;
}) {
  const [diff, setDiff] = useState<DiffResponse>();
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");
  const [wrap, setWrap] = useState(
    () => localStorage.getItem("lec-diffwrap") === "1",
  );
  const [comments, setComments] = useState<DraftComment[]>([]);
  const [summary, setSummary] = useState("");
  const [sending, setSending] = useState(false);

  const [repo, setRepo] = useState("");
  const [message, setMessage] = useState(name ?? "");
  const [push, setPush] = useState(true);
  const [pr, setPr] = useState(false);
  const [prTitle, setPrTitle] = useState("");
  const [prBody, setPrBody] = useState("");
  const [committing, setCommitting] = useState(false);
  const [commitSteps, setCommitSteps] = useState<Record<string, JsonValue>[]>();
  const [generating, setGenerating] = useState(false);

  const [checks, setChecks] = useState<SessionCheck[]>();
  const [checksRunning, setChecksRunning] = useState(false);
  const checksPoll = useRef<number | undefined>(undefined);
  function loadChecks() {
    api
      .request<SessionCheck[]>(`/sessions/${sessionId}/checks?limit=10`)
      .then(setChecks)
      .catch(() => {});
  }
  useEffect(() => {
    loadChecks();
    return () => window.clearInterval(checksPoll.current);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId]);
  async function runCheckNow() {
    setChecksRunning(true);
    try {
      await api.request(`/sessions/${sessionId}/checks`, { method: "POST" });
      // the run happens on the target and can take a while; poll a few times
      // rather than opening a second SSE connection just for this one panel
      window.clearInterval(checksPoll.current);
      let tries = 0;
      checksPoll.current = window.setInterval(() => {
        loadChecks();
        if (++tries >= 15) window.clearInterval(checksPoll.current);
      }, 2000);
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setChecksRunning(false);
    }
  }

  useEffect(() => {
    const c = new AbortController();
    setLoading(true);
    setLoadError("");
    api
      .request<DiffResponse>(`/sessions/${sessionId}/diff`)
      .then((parsed) => {
        if (c.signal.aborted) return;
        setDiff(parsed);
        if (parsed.repos.length === 1) setRepo(parsed.repos[0]?.name ?? "");
      })
      .catch((e) => !c.signal.aborted && setLoadError(String(e)))
      .finally(() => !c.signal.aborted && setLoading(false));
    return () => c.abort();
  }, [sessionId]);

  function addComment(c: Omit<DraftComment, "key">) {
    setComments((prev) => [...prev, { ...c, key: nextDraftKey() }]);
  }
  function removeComment(key: string) {
    setComments((prev) => prev.filter((c) => c.key !== key));
  }

  async function sendReview() {
    setSending(true);
    try {
      const result = await api.request<{ sent: boolean; comments: number }>(
        `/sessions/${sessionId}/review`,
        {
          method: "POST",
          body: {
            comments: toWireComments(comments) as unknown as JsonValue,
            summary,
          },
        },
      );
      onNotice(
        `Sent ${String(result.comments ?? comments.length)} comment(s) to the session.`,
      );
      setComments([]);
      setSummary("");
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setSending(false);
    }
  }

  async function generateDescription() {
    setGenerating(true);
    try {
      const out = await api.request<{ title: string; body: string }>(
        `/sessions/${sessionId}/pr-description`,
        { method: "POST" },
      );
      setPrTitle(out.title);
      setPrBody(out.body);
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setGenerating(false);
    }
  }

  async function commit() {
    setCommitting(true);
    setCommitSteps(undefined);
    try {
      const path = `/sessions/${sessionId}/commit${
        repo ? `?repo=${encodeURIComponent(repo)}` : ""
      }`;
      const out = await api.request<{ steps: Record<string, JsonValue>[] }>(
        path,
        {
          method: "POST",
          body: { message, push, pr, pr_title: prTitle, pr_body: prBody },
        },
      );
      const steps = out.steps ?? [];
      setCommitSteps(steps);
      const failed = steps.find((s) => Number(s.rc) !== 0);
      onNotice(
        failed ? `Commit step "${String(failed.step)}" failed.` : "Committed.",
        Boolean(failed),
      );
    } catch (e) {
      onNotice(String(e), true);
    } finally {
      setCommitting(false);
    }
  }

  const repos = diff?.repos ?? [];
  const multi = repos.length > 1;
  const shown = multi ? repos.filter((r) => !repo || r.name === repo) : repos;

  return (
    <Modal className="sheet task-detail review-panel" id="session-review">
      <div className="sheet-head">
        <h2>Review &amp; merge{name ? ` — ${name}` : ""}</h2>
        <button className="b" onClick={onClose}>
          Close
        </button>
      </div>
      {loading && <p className="sub">Loading the live diff…</p>}
      {!loading && loadError && <p className="sub error">{loadError}</p>}
      {!loading && !loadError && diff && (
        <>
          <header className="diffhead">
            {repos.reduce((n, r) => n + r.files.length, 0)} file(s) changed
            {multi && (
              <select
                aria-label="Repository"
                value={repo}
                onChange={(e) => setRepo(e.target.value)}
              >
                <option value="">All repositories</option>
                {repos.map((r) => (
                  <option key={r.name} value={r.name}>
                    {r.name}
                  </option>
                ))}
              </select>
            )}
            <button
              className={"wrapbtn" + (wrap ? " on" : "")}
              onClick={() => {
                const n = !wrap;
                setWrap(n);
                localStorage.setItem("lec-diffwrap", n ? "1" : "");
              }}
            >
              ⏎ wrap: {wrap ? "on" : "off"}
            </button>
          </header>
          {shown.map((r) => (
            <DiffViewer
              key={r.name ?? "repo"}
              repoLabel={multi ? r.name : undefined}
              files={r.files}
              stats={r.stats}
              wrap={wrap}
              truncated={r.truncated}
              commentable
              comments={comments}
              onAddComment={addComment}
            />
          ))}

          <CommentTray
            comments={comments}
            summary={summary}
            onSummaryChange={setSummary}
            onRemove={removeComment}
            onSend={() => void sendReview()}
            busy={sending}
            sendLabel="Send to agent"
          />

          <section className="review-commit-form">
            <h3>Commit, push &amp; PR</h3>
            {multi && (
              <label className="f">
                Repository
                <select
                  className="f"
                  value={repo}
                  onChange={(e) => setRepo(e.target.value)}
                >
                  <option value="">choose one…</option>
                  {repos.map((r) => (
                    <option key={r.name} value={r.name}>
                      {r.name}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <label className="f">
              Commit message
              <input
                className="f"
                value={message}
                onChange={(e) => setMessage(e.target.value)}
              />
            </label>
            <label className="review-checkbox">
              <input
                type="checkbox"
                checked={push}
                onChange={(e) => setPush(e.target.checked)}
              />
              Push to origin
            </label>
            <label className="review-checkbox">
              <input
                type="checkbox"
                checked={pr}
                disabled={!push}
                onChange={(e) => setPr(e.target.checked)}
              />
              Open a PR (needs <code>gh</code> on the target)
            </label>
            {pr && (
              <>
                <label className="f">
                  PR title
                  <input
                    className="f"
                    value={prTitle}
                    onChange={(e) => setPrTitle(e.target.value)}
                    placeholder={message}
                  />
                </label>
                <label className="f">
                  PR body
                  <textarea
                    className="f"
                    rows={4}
                    value={prBody}
                    onChange={(e) => setPrBody(e.target.value)}
                  />
                </label>
                <button
                  type="button"
                  className="b"
                  disabled={generating}
                  onClick={() => void generateDescription()}
                >
                  {generating ? "Generating…" : "✨ Generate description"}
                </button>
              </>
            )}
            <button
              type="button"
              className="b ok"
              disabled={committing || !message.trim() || (multi && !repo)}
              onClick={() => void commit()}
            >
              {committing ? "Committing…" : "⎇ Commit"}
            </button>
            {commitSteps && (
              <ul className="review-commit-steps">
                {commitSteps.map((s, i) => (
                  <li key={i} data-ok={Number(s.rc) === 0}>
                    <b>{String(s.step)}</b>: {Number(s.rc) === 0 ? "ok" : "failed"}
                    {typeof s.url === "string" && s.url && (
                      <>
                        {" — "}
                        <a href={s.url} target="_blank" rel="noreferrer">
                          {s.url}
                        </a>
                      </>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="review-check-history">
            <h3>
              Checks
              <button
                type="button"
                className="b"
                disabled={checksRunning}
                onClick={() => void runCheckNow()}
              >
                {checksRunning ? "Starting…" : "Run check"}
              </button>
            </h3>
            {checks && checks.length === 0 && (
              <p className="sub">No checks have run yet.</p>
            )}
            {checks && checks.length > 0 && (
              <ul className="review-check-list">
                {checks.map((c) => (
                  <li key={c.id}>
                    <span className={`chip check-chip check-${c.status}`}>
                      {c.status}
                    </span>
                    <code>{c.command}</code>
                    <span className="sub">
                      {c.finished_at
                        ? new Date(c.finished_at * 1000).toLocaleString()
                        : "running…"}
                    </span>
                    {c.output_tail && (
                      <pre className="review-check-output">{c.output_tail}</pre>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </section>
        </>
      )}
    </Modal>
  );
}
