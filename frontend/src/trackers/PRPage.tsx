import { useCallback, useEffect, useState } from "react";
import { t, useLocale } from "../i18n";
import { Modal } from "../sessions/Modal";
import { Markdown } from "../sessions/markdown";
import { errorText, type Notice, type TrackerApi } from "./api";
import { Avatar, CheckIcon, ChecksBadge, Labels, NamePicker, ReactionBar, ReviewBadge, StatePill, Timeline } from "./bits";
import { ago, canReact, itemMark, mergeButtonLabel, mergeState, methodLabel, SOURCE_NAME } from "./logic";
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
  onQueue,
}: {
  api: TrackerApi;
  projectId: number;
  item: ItemRef;
  onClose(): void;
  onOpen(ref: ItemRef): void;
  onNotice: Notice;
  onChanged(): void;
  onStarted(result: StartedWork): void;
  /** opens the merge queue view, where the host has one */
  onQueue?(): void;
}) {
  useLocale();
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
      auto
        ? t("trackers.pr.notice.autoOn", { id: pr.id })
        : pr.merge.merge_queue
          ? t("trackers.pr.notice.queued", { id: pr.id })
          : t("trackers.pr.notice.merged", { id: pr.id }));
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
    setLogs((l) => ({ ...l, [c.id]: t("trackers.pr.loadingLog") }));
    try {
      const res = await api.request<{ log: string }>(`${base}/prs/${item.id}/log?check=${encodeURIComponent(c.id)}`);
      setLogs((l) => ({ ...l, [c.id]: res.log || t("trackers.pr.emptyLog") }));
    } catch (e) {
      setLogs((l) => ({ ...l, [c.id]: errorText(e) }));
    }
  }

  const head = (
    <div className="th-detail-bar">
      <button className="b th-back" onClick={onClose} aria-label={t("trackers.detail.backToList")}>
        ←<span> {t("trackers.detail.back")}</span>
      </button>
      <span className="th-detail-kind">{t("trackers.pr.kind", { source: pr ? SOURCE_NAME[pr.source] : "" })}</span>
      {pr && (
        <a className="b" href={pr.url} target="_blank" rel="noreferrer">
          {t("trackers.detail.openExternal")}
        </a>
      )}
    </div>
  );
  if (error)
    return (
      <div className="th-detail-inner">
        {head}
        <p className="th-error" role="alert">{error}</p>
        <button className="b" onClick={() => void load()}>{t("trackers.detail.retry")}</button>
      </div>
    );
  if (!pr)
    return (
      <div className="th-detail-inner">
        {head}
        <p className="th-muted">{t("trackers.detail.loading", { mark: `#${item.id}` })}</p>
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
            <b>{pr.author}</b> {t("trackers.pr.wantsToMergeInto")} <code className="th-branch">{pr.base}</code> {t("trackers.pr.from")}{" "}
            <code className="th-branch">{pr.head}</code>
          </span>
        </div>
        <div className="th-pr-meta th-muted">
          {(pr.changed_files > 0 || pr.additions > 0 || pr.deletions > 0) && <span className="th-diffstat">
            <span className="th-add-n">+{pr.additions}</span> <span className="th-del-n">−{pr.deletions}</span> · {t("trackers.pr.files", { n: pr.changed_files })}
          </span>}
          <span>{t("trackers.detail.updated", { ago: ago(pr.updated_at) })}</span>
          <ReactionBar reactions={pr.reactions} label={`#${pr.id}`}
            onReact={canReact(pr.source) ? async (emoji) => { await act("", `/prs/${pr.id}/reactions`, { emoji }, t("trackers.notice.reactionAdded")); } : undefined} />
        </div>
      </header>

      {pr.stack && pr.stack.length > 1 && (
        <section className="th-section th-stack" aria-label={t("trackers.pr.stack")}>
          <h3>{t("trackers.pr.stack")}</h3>
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

      <section className="th-section th-mergebox" aria-label={t("trackers.pr.mergeSection")}>
        <div className="th-status-rows">
          <div className={`th-status-row th-checks-${pr.checks || "none"}`}>
            <ChecksBadge checks={pr.checks} />
            {pr.checks === "none" && <span className="th-muted">{t("trackers.pr.noChecksReported")}</span>}
            {failing.length > 0 && <span>{t("trackers.pr.failingCount", { n: failing.length })}</span>}
          </div>
          <div className="th-status-row">
            {pr.review ? <ReviewBadge review={pr.review} /> : <span className="th-muted">{t("trackers.pr.noReviewDecision")}</span>}
          </div>
          {pr.mergeable === "conflicting" && (
            <div className="th-status-row th-conflict" role="status">
              <span className="th-badge th-conflicts">{t("trackers.conflictsBadge")}</span> {t("trackers.pr.branchConflictsWith")} <code className="th-branch">{pr.base}</code>
            </div>
          )}
          {pr.ci && (
            <div className="th-status-row">
              <span className={`th-badge th-ci-${pr.ci.state}`}>{pr.ci.label}</span>
              {pr.ci.session_id && <span className="th-muted">{t("trackers.pr.ciSession", { id: pr.ci.session_id })}</span>}
            </div>
          )}
        </div>

        {pr.mergeable === "conflicting" && (
          <div className="th-conflicts-box">
            <h4>{t("trackers.pr.conflictingFiles")}</h4>
            {!conflicts ? (
              <p className="th-muted">{t("trackers.pr.findingConflicts")}</p>
            ) : conflicts.files.length ? (
              <ul className="th-files">
                {conflicts.files.map((f) => (
                  <li key={f}>
                    <code>{f}</code>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="th-muted">{conflicts.detail || t("trackers.pr.noConflictingFiles")}</p>
            )}
            <button className="b ok" id="th-resolve" disabled={busy !== "" || pr.state !== "open"} onClick={() => void agent("resolve")}>
              {busy === "resolve" ? t("trackers.starting") : t("trackers.pr.resolveWithAgent")}
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
                      {pr.auto_merge.method
                        ? pr.auto_merge.enabled_by
                          ? t("trackers.pr.autoOnMethodBy", { method: pr.auto_merge.method, name: pr.auto_merge.enabled_by })
                          : t("trackers.pr.autoOnMethod", { method: pr.auto_merge.method })
                        : pr.auto_merge.enabled_by
                          ? t("trackers.pr.autoOnBy", { name: pr.auto_merge.enabled_by })
                          : t("trackers.pr.autoOn")}
                    </span>
                    <button className="b" disabled={busy !== ""} onClick={() => void act("auto", `/prs/${pr.id}/auto-merge`, {}, t("trackers.pr.notice.autoOff"), "DELETE")}>
                      {t("trackers.pr.turnOff")}
                    </button>
                  </div>
                ) : (
                  <>
                    <div className="th-merge-opts">
                      {pr.merge.methods.length > 1 && (
                        <select aria-label={t("trackers.pr.mergeMethod")} value={method} onChange={(e) => setMethod(e.target.value as MergeMethod)}>
                          {pr.merge.methods.map((m) => (
                            <option key={m} value={m}>
                              {methodLabel(m)}
                            </option>
                          ))}
                        </select>
                      )}
                      <label className="th-check">
                        <input type="checkbox" checked={deleteBranch} onChange={(e) => setDeleteBranch(e.target.checked)} /> {t("trackers.pr.deleteBranch")}
                      </label>
                    </div>
                    <div className="btnrow">
                      {!ms.autoOnly && (
                        <button className="b ok grow th-merge-btn" id="th-merge" disabled={busy !== ""} onClick={() => setConfirm("merge")}>
                          {busy === "merge" ? t("trackers.pr.merging") : mergeButtonLabel(pr, method, false)}
                        </button>
                      )}
                      {pr.merge.auto_merge_allowed && (
                        <button className={`b grow ${ms.autoOnly ? "ok" : ""}`} id="th-auto" disabled={busy !== ""} onClick={() => setConfirm("auto")}>
                          {busy === "auto" ? t("trackers.pr.enabling") : t("trackers.pr.autoWhenReady")}
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
            <button className="b warn" id="th-fix" disabled={busy !== "" || ciActive} title={ciActive ? t("trackers.pr.ciBusy") : ""} onClick={() => void agent("fix")}>
              {busy === "fix" ? t("trackers.starting") : t("trackers.pr.fixChecks")}
            </button>
          )}
          {pr.state !== "merged" && (
            <button className={`b ${pr.state === "open" ? "no" : ""}`} disabled={busy !== ""} onClick={() => setConfirm(pr.state === "open" ? "close" : "reopen")}>
              {pr.state === "open" ? t("trackers.close") : t("trackers.reopen")}
            </button>
          )}
          <button className="b" onClick={() => setStarting(true)}>
            {t("trackers.startSession")}
          </button>
          {onQueue && (pr.merge.merge_queue || pr.auto_merge) && (
            <button className="b" id="th-view-queue" onClick={onQueue}>
              {pr.source === "gitlab" ? t("trackers.hub.tab.mergeTrain") : t("trackers.hub.tab.mergeQueue")}
            </button>
          )}
        </div>
      </section>

      <section className="th-section" aria-label={t("trackers.pr.checks")}>
        <h3>
          {t("trackers.pr.checks")} <span className="th-muted">{pr.check_runs.length}</span>
        </h3>
        {sorted.length === 0 && <p className="th-muted">{t("trackers.pr.noChecks")}</p>}
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
                    {openLog === c.id ? t("trackers.pr.hideLog") : t("trackers.pr.log")}
                  </button>
                ) : c.url ? (
                  <a className="b th-log-btn" href={c.url} target="_blank" rel="noreferrer">
                    {t("trackers.pr.details")}
                  </a>
                ) : null}
              </div>
              {openLog === c.id && <pre className="th-log">{logs[c.id]}</pre>}
            </li>
          ))}
        </ul>
      </section>

      <section className="th-section" aria-label={t("trackers.pr.reviewers")}>
        <h3>{t("trackers.pr.reviewers")}</h3>
        <ul className="th-reviewers">
          {pr.reviewers.length === 0 && <li className="th-muted">{t("trackers.pr.noReviewers")}</li>}
          {pr.reviewers.map((r) => (
            <li key={r.login}>
              <Avatar name={r.login} />
              <span className="th-rev-name">
                {r.login}
                {r.team && <small className="th-muted"> {t("trackers.pr.team")}</small>}
              </span>
              <span className={`th-rev-state th-rev-${r.state}`}>{REVIEWER_STATE()[r.state] || r.state.replace("_", " ")}</span>
              {r.state === "requested" && (
                <button className="th-x" aria-label={t("trackers.pr.removeReviewer", { name: r.login })} disabled={busy !== ""}
                  onClick={() => void act("reviewers", `/prs/${pr.id}/reviewers`, { remove: [r.login] }, t("trackers.notice.removed", { name: r.login }))}>
                  ×
                </button>
              )}
            </li>
          ))}
        </ul>
        <NamePicker
          label={t("trackers.pr.requestReview")}
          busy={busy !== ""}
          options={(meta?.users || []).filter((u) => !requested.has(u.login)).map((u) => ({ value: u.login, hint: u.name }))}
          onPick={(login) => void act("reviewers", `/prs/${pr.id}/reviewers`, { add: [login] }, t("trackers.pr.notice.askedReview", { name: login }))}
        />
      </section>

      <section className="th-section" aria-label={t("trackers.labels")}>
        <h3>{t("trackers.labels")}</h3>
        <Labels labels={pr.labels} onRemove={(name) => void act("labels", `/prs/${pr.id}/labels`, { remove: [name] }, t("trackers.notice.removed", { name }))} />
        {!pr.labels.length && <p className="th-muted">{t("trackers.labels.none")}</p>}
        <NamePicker
          label={t("trackers.labels.add")}
          busy={busy !== ""}
          options={(meta?.labels || []).filter((l) => !pr.labels.some((x) => x.name === l.name)).map((l) => ({ value: l.name }))}
          onPick={(name) => void act("labels", `/prs/${pr.id}/labels`, { add: [name] }, t("trackers.notice.labelled", { name }))}
        />
      </section>

      <section className="th-section th-desc" aria-label={t("trackers.description")}>
        <h3>{t("trackers.description")}</h3>
        <div className="th-md">{pr.body ? <Markdown text={pr.body} /> : <p className="th-muted">{t("trackers.description.none")}</p>}</div>
      </section>

      <section className="th-section" aria-label={t("trackers.pr.conversation")}>
        <h3>{t("trackers.pr.conversation")}</h3>
        <Timeline
          events={pr.timeline}
          onComment={async (body) => {
            await act("", `/prs/${pr.id}/comments`, { body }, t("trackers.notice.commentPosted"));
          }}
          onReact={canReact(pr.source) ? async (subject, emoji) => { await act("", `/prs/${pr.id}/reactions`, { subject, emoji }, t("trackers.notice.reactionAdded")); } : undefined}
        />
      </section>

      {confirm && (
        <Modal className="th-confirm" aria-labelledby="th-confirm-title" onCancel={() => setConfirm("")}>
          <h2 id="th-confirm-title">
            {confirm === "merge"
              ? t("trackers.pr.confirm.question", { action: mergeButtonLabel(pr, method, false) })
              : confirm === "auto"
                ? t("trackers.pr.confirm.autoTitle")
                : confirm === "close"
                  ? t("trackers.pr.confirm.closeTitle")
                  : t("trackers.pr.confirm.reopenTitle")}
          </h2>
          {confirm === "merge" || confirm === "auto" ? (
            <>
              <p>
                {confirm === "auto" ? t("trackers.pr.confirm.whenPasses") + " " : ""}
                <b>{itemMark(pr)}</b> {pr.title}{" "}
                {confirm === "auto"
                  ? t("trackers.pr.confirm.willBeMergedInto")
                  : pr.merge.merge_queue
                    ? t("trackers.pr.confirm.joinsQueueInto")
                    : t("trackers.pr.confirm.isMergedInto")}{" "}
                <code className="th-branch">{pr.base}</code> {t("trackers.pr.confirm.with")} <b>{methodLabel(method).toLowerCase()}</b>
                {deleteBranch ? t("trackers.pr.confirm.branchDeleted") : t("trackers.pr.confirm.end")}
              </p>
              <p className="th-muted">
                {t("trackers.pr.confirm.onlyCommit")} <code>{pr.head_sha.slice(0, 7)}</code> {t("trackers.pr.confirm.shaRule")}
              </p>
              {ms.warn && <p className="th-warn">{ms.warn}</p>}
            </>
          ) : (
            <p>
              {confirm === "close"
                ? t("trackers.pr.confirm.willClose", { mark: itemMark(pr) })
                : t("trackers.pr.confirm.willReopen", { mark: itemMark(pr) })}
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
                  void act("state", `/prs/${pr.id}/state`, { open }, open ? t("trackers.notice.reopened") : t("trackers.notice.closed"));
                }
              }}
            >
              {confirm === "merge"
                ? t("trackers.pr.confirm.go")
                : confirm === "auto"
                  ? t("trackers.merge.enableAuto")
                  : confirm === "close"
                    ? t("trackers.close")
                    : t("trackers.reopen")}
            </button>
            <button className="b" onClick={() => setConfirm("")}>
              {t("trackers.cancel")}
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

// A reviewer's state beside their name.
const REVIEWER_STATE = (): Record<string, string> => ({
  requested: t("trackers.pr.reviewer.requested"),
  approved: t("trackers.pr.reviewer.approved"),
  changes_requested: t("trackers.pr.reviewer.changesRequested"),
  commented: t("trackers.pr.reviewer.commented"),
  dismissed: t("trackers.pr.reviewer.dismissed"),
});

function rank(c: Check): number {
  return { fail: 0, cancel: 1, pending: 2, pass: 3, skipping: 4 }[c.status] ?? 5;
}
