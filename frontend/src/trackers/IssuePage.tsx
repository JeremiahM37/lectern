import { useCallback, useEffect, useState } from "react";
import { t, useLocale } from "../i18n";
import { Markdown } from "../sessions/markdown";
import { errorText, issuePath, type Notice, type TrackerApi } from "./api";
import { Labels, NamePicker, Reactions, StatePill, Timeline } from "./bits";
import { ago, itemMark, SOURCE_NAME } from "./logic";
import { StartWork, type StartedWork } from "./StartWork";
import type { ForgeMeta, IssueDetail, ItemRef } from "./types";

/** An issue from any tracker: description, sub-issues and parent, comments,
 * status (Linear workflow states, Jira transitions, forge open/closed),
 * labels where the tracker lets Lectern edit them, and Start work. */
export function IssuePage({
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
  useLocale();
  const forge = item.source === "github" || item.source === "gitlab";
  const [issue, setIssue] = useState<IssueDetail>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [meta, setMeta] = useState<ForgeMeta>();
  const [starting, setStarting] = useState(false);
  const path = issuePath(projectId, item);

  const load = useCallback(async () => {
    setError("");
    try {
      setIssue(await api.request<IssueDetail>(path));
    } catch (e) {
      setError(errorText(e));
    }
  }, [api, path]);
  useEffect(() => {
    setIssue(undefined);
    void load();
  }, [load]);
  useEffect(() => {
    if (forge) api.request<ForgeMeta>(`/projects/${projectId}/forge/meta`).then(setMeta).catch(() => {});
  }, [api, forge, projectId]);

  async function post(sub: string, body: Record<string, unknown>, done: string) {
    setBusy(true);
    try {
      const fresh = await api.request<IssueDetail | { ok: boolean }>(`${path}/${sub}`, { method: "POST", body: body as never });
      onNotice(done);
      if ("id" in fresh) setIssue(fresh);
      else await load();
      onChanged();
    } catch (e) {
      onNotice(errorText(e), true);
    } finally {
      setBusy(false);
    }
  }

  const head = (
    <div className="th-detail-bar">
      <button className="b th-back" onClick={onClose} aria-label={t("trackers.detail.backToList")}>
        ←<span> {t("trackers.detail.back")}</span>
      </button>
      <span className="th-detail-kind">{t("trackers.issue.kind", { source: SOURCE_NAME[item.source] })}</span>
      {issue && (
        <a className="b" href={issue.url} target="_blank" rel="noreferrer">
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
  if (!issue)
    return (
      <div className="th-detail-inner">
        {head}
        <p className="th-muted">{t("trackers.detail.loading", { mark: itemMark(item) })}</p>
      </div>
    );
  const statusID = issue.transitions.find((tr) => tr.name === issue.state || tr.name.endsWith(`→ ${issue.state}`))?.id || "";
  return (
    <div className="th-detail-inner" data-issue={issue.id}>
      {head}
      <header className="th-pr-head">
        <h2>
          {issue.title} <span className="th-num">{itemMark(issue)}</span>
        </h2>
        <div className="th-pr-meta">
          <StatePill item={issue} />
          {issue.author && (
            <span>
              {t("trackers.issue.openedBy")} <b>{issue.author}</b>
            </span>
          )}
          {issue.assignees.length > 0 && <span>{t("trackers.issue.assignedTo", { names: issue.assignees.join(", ") })}</span>}
          {issue.priority && <span className="th-badge">{issue.priority}</span>}
          <span className="th-muted">{t("trackers.detail.updated", { ago: ago(issue.updated_at) })}</span>
          <Reactions reactions={issue.reactions} />
        </div>
      </header>

      <section className="th-section th-mergebox" aria-label={t("trackers.issue.workOnThis")}>
        <div className="btnrow">
          <button className="b ok grow" id="th-start" onClick={() => setStarting(true)}>
            {t("trackers.issue.startSessionOrTask")}
          </button>
          {forge && (
            <button className={`b ${issue.state === "open" ? "no" : ""}`} disabled={busy} onClick={() => void post("state", { open: issue.state !== "open" }, issue.state === "open" ? t("trackers.notice.closed") : t("trackers.notice.reopened"))}>
              {issue.state === "open" ? t("trackers.issue.closeIssue") : t("trackers.reopen")}
            </button>
          )}
        </div>
        <p className="th-muted th-branch-hint">
          {t("trackers.issue.suggestedBranch")} <code className="th-branch">{issue.branch_name}</code>
        </p>
        {!forge && issue.transitions.length > 0 && (
          <label className="th-status-pick">
            {item.source === "jira" ? t("trackers.issue.moveWithTransition") : t("trackers.issue.status")}
            <select
              aria-label={t("trackers.issue.status")}
              value={item.source === "linear" ? statusID : ""}
              disabled={busy}
              onChange={(e) => {
                const tr = issue.transitions.find((x) => x.id === e.target.value);
                if (tr) void post("status", { id: tr.id }, t("trackers.issue.movedTo", { name: tr.name }));
              }}
            >
              {item.source === "jira" && <option value="">{t("trackers.issue.chooseTransition", { state: issue.state })}</option>}
              {issue.transitions.map((tr) => (
                <option key={tr.id} value={tr.id}>
                  {tr.name}
                </option>
              ))}
            </select>
          </label>
        )}
      </section>

      {(issue.parent_item || issue.children.length > 0) && (
        <section className="th-section" aria-label={t("trackers.issue.related")}>
          {issue.parent_item && (
            <>
              <h3>{t("trackers.issue.parent")}</h3>
              <button className="th-link-row" onClick={() => onOpen({ ...item, id: issue.parent_item!.id })}>
                <span className="th-num">{issue.parent_item.id}</span> {issue.parent_item.title}
              </button>
            </>
          )}
          {issue.children.length > 0 && (
            <>
              <h3>
                {t("trackers.issue.subIssues")}{" "}
                <span className="th-muted">
                  {t("trackers.issue.subIssuesDone", {
                    done: issue.children.filter((c) => ["completed", "done"].includes(c.status_type || "")).length,
                    total: issue.children.length,
                  })}
                </span>
              </h3>
              <ul className="th-children">
                {issue.children.map((c) => (
                  <li key={c.id}>
                    <button className="th-link-row" onClick={() => onOpen({ ...item, id: c.id })}>
                      <StatePill item={c} /> <span className="th-num">{c.id}</span> {c.title}
                    </button>
                  </li>
                ))}
              </ul>
            </>
          )}
        </section>
      )}

      <section className="th-section" aria-label={t("trackers.labels")}>
        <h3>{t("trackers.labels")}</h3>
        <Labels labels={issue.labels} onRemove={forge ? (name) => void post("labels", { remove: [name] }, t("trackers.notice.removed", { name })) : undefined} />
        {!issue.labels.length && <p className="th-muted">{t("trackers.labels.none")}</p>}
        {forge && (
          <NamePicker
            label={t("trackers.labels.add")}
            busy={busy}
            options={(meta?.labels || []).filter((l) => !issue.labels.some((x) => x.name === l.name)).map((l) => ({ value: l.name }))}
            onPick={(name) => void post("labels", { add: [name] }, t("trackers.notice.labelled", { name }))}
          />
        )}
      </section>

      <section className="th-section th-desc" aria-label={t("trackers.description")}>
        <h3>{t("trackers.description")}</h3>
        <div className="th-md">{issue.body ? <Markdown text={issue.body} /> : <p className="th-muted">{t("trackers.description.none")}</p>}</div>
      </section>

      <section className="th-section" aria-label={t("trackers.issue.comments")}>
        <h3>{t("trackers.issue.comments")}</h3>
        <Timeline
          events={issue.timeline}
          onComment={async (body) => {
            await post("comments", { body }, t("trackers.notice.commentPosted"));
          }}
        />
      </section>

      {starting && (
        <StartWork
          api={api}
          projectId={projectId}
          item={{ ...item, id: issue.id }}
          title={issue.title}
          branch={issue.branch_name}
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
