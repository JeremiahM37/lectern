import { useCallback, useEffect, useState } from "react";
import { Modal } from "../sessions/Modal";
import { Markdown } from "../sessions/markdown";
import { errorText, type Notice, type TrackerApi } from "./api";
import { Avatar, CheckIcon, ChecksBadge, Labels, NamePicker, Reactions, ReviewBadge, StatePill, Timeline } from "./bits";
import { ago, itemMark, mergeButtonLabel, mergeState, methodLabel, SOURCE_NAME } from "./logic";
import { StartWork, type StartedWork } from "./StartWork";
import type { Check, Conflicts, ForgeMeta, ItemRef, MergeMethod, PRDetail } from "./types";

type Busy = "" | "merge" | "auto" | "state" | "reviewers" | "labels" | "resolve" | "fix";

/** The pull request page: header, stack, merge box (conflicts, checks,
 * merge/auto-merge/close behind confirmations), reviewers, labels,
 * description and conversation. Every action is a signed-in human's; the
 * server refuses them from anything else. */
export function PRPage({
  api,
  projectId,
  item,
  onClose,
  onOpen,
  onNotice,
  onChanged,
  onStarted,
}: {
  api: TrackerApi;
  projectId: number;
  item: ItemRef;
  onClose(): void;
  onOpen(ref: ItemRef): void;
  onNotice: Notice;
  onChanged(): void;
  onStarted(result: StartedWork): void;
}) {
  const base = `/projects/${projectId}/forge`;
  const [pr, setPr] = useState<PRDetail>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState<Busy>("");
  const [meta, setMeta] = useState<ForgeMeta>();
  const [conflicts, setConflicts] = useState<Conflicts>();
  const [logs, setLogs] = useState<Record<string, string>>({});
  const [openLog, setOpenLog] = useState("");
  const [method, setMethod] = useState<MergeMethod>("merge");
  const [deleteBranch, setDeleteBranch] = useState(false);
  const [confirm, setConfirm] = useState<"" | "merge" | "auto" | "close" | "reopen">("");
  const [starting, setStarting] = useState(false);

  const load = useCallback(async () => {
    setError("");
    try {
      const d = await api.request<PRDetail>(`${base}/prs/${item.id}`);
      setPr(d);
      setMethod(d.merge.methods.includes(d.merge.default) ? d.merge.default : d.merge.methods[0] || "merge");
      setDeleteBranch(d.merge.delete_branch_default);
      if (d.mergeable === "conflicting")
        api
          .request<{ conflicts: Conflicts }>(`${base}/prs/${item.id}/conflicts`)
          .then((c) => setConflicts(c.conflicts))
          .catch((e) => setConflicts({ files: [], checked: false, detail: errorText(e) }));
      else setConflicts(undefined);
    } catch (e) {
      setError(errorText(e));
    }
  }, [api, base, item.id]);
  useEffect(() => {
    setPr(undefined);
    setLogs({});
    setOpenLog("");
    void load();
  }, [load]);
  useEffect(() => {
    api.request<ForgeMeta>(`${base}/meta`).then(setMeta).catch(() => {});
  }, [api, base]);

  async function act(kind: Busy, path: string, body: Record<string, unknown>, done: string, method = "POST") {
    setBusy(kind);
    try {
      await api.request(`${base}${path}`, { method, body: body as never });
      onNotice(done);
      await load();
      onChanged();
      return true;
    } catch (e) {
      onNotice(errorText(e), true);
      return false;
    } finally {
      setBusy("");
    }
  }

  async function merge(auto: boolean) {
    if (!pr) return;
    setConfirm("");
    const ok = await act(auto ? "auto" : "merge", `/prs/${pr.id}/merge`,
      { method, delete_branch: deleteBranch, auto, head_sha: pr.head_sha, confirm: true },
      auto ? `Auto-merge is on for #${pr.id}` : pr.merge.merge_queue ? `#${pr.id} added to the merge queue` : `Merged #${pr.id}`);
    if (!ok) void load();
  }

  async function agent(kind: "resolve" | "fix") {
    if (!pr) return;
    setBusy(kind);
    try {
      const res = await api.request<{ session: { id: number; name: string } }>(`${base}/prs/${pr.id}/${kind === "fix" ? "fix-checks" : "resolve"}`, {
        method: "POST",
        body: {},
      });
      onStarted({ kind: "session", session: res.session });
      await load();
    } catch (e) {
      onNotice(errorText(e), true);
    } finally {
      setBusy("");
    }
  }

  async function loadLog(c: Check) {
    if (openLog === c.id) {
      setOpenLog("");
      return;
    }
    setOpenLog(c.id);
    if (logs[c.id]) return;
    setLogs((l) => ({ ...l, [c.id]: "Loading log…" }));
    try {
      const res = await api.request<{ log: string }>(`${base}/prs/${item.id}/log?check=${encodeURIComponent(c.id)}`);
      setLogs((l) => ({ ...l, [c.id]: res.log || "(empty log)" }));
    } catch (e) {
      setLogs((l) => ({ ...l, [c.id]: errorText(e) }));
    }
  }

  const head = (
    <div className="th-detail-bar">
      <button className="b th-back" onClick={onClose} aria-label="Back to the list">
        ←<span> Back</span>
      </button>
      <span className="th-detail-kind">{pr ? SOURCE_NAME[pr.source] : ""} pull request</span>
      {pr && (
        <a className="b" href={pr.url} target="_blank" rel="noreferrer">
          Open ↗
        </a>
      )}
    </div>
  );
  if (error)
    return (
      <div className="th-detail-inner">
        {head}
        <p className="th-error" role="alert">{error}</p>
        <button className="b" onClick={() => void load()}>Retry</button>
      </div>
    );
  if (!pr)
    return (
      <div className="th-detail-inner">
        {head}
        <p className="th-muted">Loading #{item.id}…</p>
      </div>
    );

  const ms = mergeState(pr);
  const failing = pr.check_runs.filter((c) => c.status === "fail" || c.status === "cancel");
  const sorted = [...pr.check_runs].sort((a, b) => rank(a) - rank(b));
  const requested = new Set(pr.reviewers.map((r) => r.login));
  const ciActive = !!pr.ci?.active;
  return (
    <div className="th-detail-inner" data-pr={pr.id}>
      {head}
      <header className="th-pr-head">
        <h2>
          {pr.title} <span className="th-num">{itemMark(pr)}</span>
        </h2>
        <div className="th-pr-meta">
          <StatePill item={pr} />
          <span>
            <b>{pr.author}</b> wants to merge into <code className="th-branch">{pr.base}</code> from{" "}
            <code className="th-branch">{pr.head}</code>
          </span>
        </div>
        <div className="th-pr-meta th-muted">
          <span className="th-diffstat">
            <span className="th-add-n">+{pr.additions}</span> <span className="th-del-n">−{pr.deletions}</span> · {pr.changed_files} files
          </span>
          <span>updated {ago(pr.updated_at)}</span>
          <Reactions reactions={pr.reactions} />
        </div>
      </header>

      {pr.stack && pr.stack.length > 1 && (
        <section className="th-section th-stack" aria-label="Stack">
          <h3>Stack</h3>
          <ol>
            {pr.stack.map((s) => (
              <li key={s.number} style={{ paddingLeft: `${s.depth * 14}px` }} className={s.current ? "current" : ""}>
                <button type="button" className="th-stack-btn" disabled={s.current} onClick={() => onOpen({ source: pr.source, kind: "pr", id: String(s.number) })}>
                  <span className="th-num">#{s.number}</span> {s.title}
                </button>
                <small className="th-muted">
                  {s.head} → {s.base}
                </small>
              </li>
            ))}
          </ol>
        </section>
      )}

      <section className="th-section th-mergebox" aria-label="Merge">
        <div className="th-status-rows">
          <div className={`th-status-row th-checks-${pr.checks || "none"}`}>
            <ChecksBadge checks={pr.checks} />
            {pr.checks === "none" && <span className="th-muted">No checks reported</span>}
            {failing.length > 0 && <span>{failing.length} failing</span>}
          </div>
          <div className="th-status-row">
            {pr.review ? <ReviewBadge review={pr.review} /> : <span className="th-muted">No review decision</span>}
          </div>
          {pr.mergeable === "conflicting" && (
            <div className="th-status-row th-conflict" role="status">
              <span className="th-badge th-conflicts">conflicts</span> This branch conflicts with <code className="th-branch">{pr.base}</code>
            </div>
          )}
          {pr.ci && (
            <div className="th-status-row">
              <span className={`th-badge th-ci-${pr.ci.state}`}>{pr.ci.label}</span>
              {pr.ci.session_id && <span className="th-muted">session #{pr.ci.session_id} is on it</span>}
            </div>
          )}
        </div>

        {pr.mergeable === "conflicting" && (
          <div className="th-conflicts-box">
            <h4>Conflicting files</h4>
            {!conflicts ? (
              <p className="th-muted">Working out which files conflict…</p>
            ) : conflicts.files.length ? (
              <ul className="th-files">
                {conflicts.files.map((f) => (
                  <li key={f}>
                    <code>{f}</code>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="th-muted">{conflicts.detail || "No conflicting files found; the base may have moved."}</p>
            )}
            <button className="b ok" id="th-resolve" disabled={busy !== "" || pr.state !== "open"} onClick={() => void agent("resolve")}>
              {busy === "resolve" ? "Starting…" : "Resolve with agent"}
            </button>
          </div>
        )}

        {pr.state === "open" && (
          <div className="th-merge-actions">
            {ms.block ? (
              <p className="th-muted">{ms.block}</p>
            ) : (
              <>
                {ms.warn && <p className="th-warn">{ms.warn}</p>}
                {pr.auto_merge ? (
                  <div className="th-auto-on">
                    <span>
                      Auto-merge is on{pr.auto_merge.method ? ` (${pr.auto_merge.method})` : ""}
                      {pr.auto_merge.enabled_by ? `, set by ${pr.auto_merge.enabled_by}` : ""}.
                    </span>
                    <button className="b" disabled={busy !== ""} onClick={() => void act("auto", `/prs/${pr.id}/auto-merge`, {}, "Auto-merge turned off", "DELETE")}>
                      Turn off
                    </button>
                  </div>
                ) : (
                  <>
                    <div className="th-merge-opts">
                      {pr.merge.methods.length > 1 && (
                        <select aria-label="Merge method" value={method} onChange={(e) => setMethod(e.target.value as MergeMethod)}>
                          {pr.merge.methods.map((m) => (
                            <option key={m} value={m}>
                              {methodLabel(m)}
                            </option>
                          ))}
                        </select>
                      )}
                      <label className="th-check">
                        <input type="checkbox" checked={deleteBranch} onChange={(e) => setDeleteBranch(e.target.checked)} /> Delete branch
                      </label>
                    </div>
                    <div className="btnrow">
                      {!ms.autoOnly && (
                        <button className="b ok grow th-merge-btn" id="th-merge" disabled={busy !== ""} onClick={() => setConfirm("merge")}>
                          {busy === "merge" ? "Merging…" : mergeButtonLabel(pr, method, false)}
                        </button>
                      )}
                      {pr.merge.auto_merge_allowed && (
                        <button className={`b grow ${ms.autoOnly ? "ok" : ""}`} id="th-auto" disabled={busy !== ""} onClick={() => setConfirm("auto")}>
                          {busy === "auto" ? "Enabling…" : "Auto-merge when ready"}
                        </button>
                      )}
                    </div>
                  </>
                )}
              </>
            )}
          </div>
        )}
        <div className="btnrow th-secondary">
          {pr.state === "open" && failing.length > 0 && (
            <button className="b warn" id="th-fix" disabled={busy !== "" || ciActive} title={ciActive ? "The CI loop is already working on this" : ""} onClick={() => void agent("fix")}>
              {busy === "fix" ? "Starting…" : "Fix checks with agent"}
            </button>
          )}
          {pr.state !== "merged" && (
            <button className={`b ${pr.state === "open" ? "no" : ""}`} disabled={busy !== ""} onClick={() => setConfirm(pr.state === "open" ? "close" : "reopen")}>
              {pr.state === "open" ? "Close" : "Reopen"}
            </button>
          )}
          <button className="b" onClick={() => setStarting(true)}>
            Start session
          </button>
        </div>
      </section>

      <section className="th-section" aria-label="Checks">
        <h3>
          Checks <span className="th-muted">{pr.check_runs.length}</span>
        </h3>
        {sorted.length === 0 && <p className="th-muted">No checks.</p>}
        <ul className="th-checks">
          {sorted.map((c) => (
            <li key={c.id}>
              <div className="th-check-row">
                <CheckIcon check={c} />
                <span className="th-check-name">
                  {c.name}
                  {c.workflow && <small className="th-muted"> · {c.workflow}</small>}
                </span>
                {c.has_log ? (
                  <button className="b th-log-btn" aria-expanded={openLog === c.id} onClick={() => void loadLog(c)}>
                    {openLog === c.id ? "Hide log" : "Log"}
                  </button>
                ) : c.url ? (
                  <a className="b th-log-btn" href={c.url} target="_blank" rel="noreferrer">
                    Details ↗
                  </a>
                ) : null}
              </div>
              {openLog === c.id && <pre className="th-log">{logs[c.id]}</pre>}
            </li>
          ))}
        </ul>
      </section>

      <section className="th-section" aria-label="Reviewers">
        <h3>Reviewers</h3>
        <ul className="th-reviewers">
          {pr.reviewers.length === 0 && <li className="th-muted">Nobody yet.</li>}
          {pr.reviewers.map((r) => (
            <li key={r.login}>
              <Avatar name={r.login} />
              <span className="th-rev-name">
                {r.login}
                {r.team && <small className="th-muted"> team</small>}
              </span>
              <span className={`th-rev-state th-rev-${r.state}`}>{r.state.replace("_", " ")}</span>
              {r.state === "requested" && (
                <button className="th-x" aria-label={`Remove reviewer ${r.login}`} disabled={busy !== ""}
                  onClick={() => void act("reviewers", `/prs/${pr.id}/reviewers`, { remove: [r.login] }, `Removed ${r.login}`)}>
                  ×
                </button>
              )}
            </li>
          ))}
        </ul>
        <NamePicker
          label="Request review"
          busy={busy !== ""}
          options={(meta?.users || []).filter((u) => !requested.has(u.login)).map((u) => ({ value: u.login, hint: u.name }))}
          onPick={(login) => void act("reviewers", `/prs/${pr.id}/reviewers`, { add: [login] }, `Asked ${login} to review`)}
        />
      </section>

      <section className="th-section" aria-label="Labels">
        <h3>Labels</h3>
        <Labels labels={pr.labels} onRemove={(name) => void act("labels", `/prs/${pr.id}/labels`, { remove: [name] }, `Removed ${name}`)} />
        {!pr.labels.length && <p className="th-muted">None.</p>}
        <NamePicker
          label="Add label"
          busy={busy !== ""}
          options={(meta?.labels || []).filter((l) => !pr.labels.some((x) => x.name === l.name)).map((l) => ({ value: l.name }))}
          onPick={(name) => void act("labels", `/prs/${pr.id}/labels`, { add: [name] }, `Labelled ${name}`)}
        />
      </section>

      <section className="th-section th-desc" aria-label="Description">
        <h3>Description</h3>
        <div className="th-md">{pr.body ? <Markdown text={pr.body} /> : <p className="th-muted">No description.</p>}</div>
      </section>

      <section className="th-section" aria-label="Conversation">
        <h3>Conversation</h3>
        <Timeline
          events={pr.timeline}
          onComment={async (body) => {
            await act("", `/prs/${pr.id}/comments`, { body }, "Comment posted");
          }}
        />
      </section>

      {confirm && (
        <Modal className="th-confirm" aria-labelledby="th-confirm-title" onCancel={() => setConfirm("")}>
          <h2 id="th-confirm-title">
            {confirm === "merge" ? mergeButtonLabel(pr, method, false) : confirm === "auto" ? "Enable auto-merge" : confirm === "close" ? "Close pull request" : "Reopen pull request"}?
          </h2>
          {confirm === "merge" || confirm === "auto" ? (
            <>
              <p>
                {confirm === "auto" ? "When every requirement passes, " : ""}
                <b>{itemMark(pr)}</b> {pr.title} {confirm === "auto" ? "will be merged" : pr.merge.merge_queue ? "joins the merge queue" : "is merged"} into{" "}
                <code className="th-branch">{pr.base}</code> with <b>{methodLabel(method).toLowerCase()}</b>
                {deleteBranch ? ", and its branch is deleted" : ""}.
              </p>
              <p className="th-muted">
                Only commit <code>{pr.head_sha.slice(0, 7)}</code> is merged; if the branch moves first, the merge is refused.
              </p>
              {ms.warn && <p className="th-warn">{ms.warn}</p>}
            </>
          ) : (
            <p>
              {itemMark(pr)} will be {confirm === "close" ? "closed without merging" : "reopened"}.
            </p>
          )}
          <div className="btnrow">
            <button
              className={`b grow ${confirm === "close" ? "no" : "ok"}`}
              id="th-confirm-go"
              autoFocus
              onClick={() => {
                if (confirm === "merge" || confirm === "auto") void merge(confirm === "auto");
                else {
                  const open = confirm === "reopen";
                  setConfirm("");
                  void act("state", `/prs/${pr.id}/state`, { open }, open ? "Reopened" : "Closed");
                }
              }}
            >
              {confirm === "merge" ? "Confirm merge" : confirm === "auto" ? "Enable auto-merge" : confirm === "close" ? "Close" : "Reopen"}
            </button>
            <button className="b" onClick={() => setConfirm("")}>
              Cancel
            </button>
          </div>
        </Modal>
      )}
      {starting && (
        <StartWork
          api={api}
          projectId={projectId}
          item={{ source: pr.source, kind: "pr", id: pr.id }}
          title={pr.title}
          pr
          onClose={() => setStarting(false)}
          onNotice={onNotice}
          onStarted={(res) => {
            setStarting(false);
            onStarted(res);
          }}
        />
      )}
    </div>
  );
}

function rank(c: Check): number {
  return { fail: 0, cancel: 1, pending: 2, pass: 3, skipping: 4 }[c.status] ?? 5;
}
