import { useCallback, useEffect, useState } from "react";
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
      <button className="b th-back" onClick={onClose} aria-label="Back to the list">
        ←<span> Back</span>
      </button>
      <span className="th-detail-kind">{SOURCE_NAME[item.source]} issue</span>
      {issue && (
        <a className="b" href={issue.url} target="_blank" rel="noreferrer">
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
  if (!issue)
    return (
      <div className="th-detail-inner">
        {head}
        <p className="th-muted">Loading {itemMark(item)}…</p>
      </div>
    );
  const statusID = issue.transitions.find((t) => t.name === issue.state || t.name.endsWith(`→ ${issue.state}`))?.id || "";
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
              opened by <b>{issue.author}</b>
            </span>
          )}
          {issue.assignees.length > 0 && <span>assigned to {issue.assignees.join(", ")}</span>}
          {issue.priority && <span className="th-badge">{issue.priority}</span>}
          <span className="th-muted">updated {ago(issue.updated_at)}</span>
          <Reactions reactions={issue.reactions} />
        </div>
      </header>

      <section className="th-section th-mergebox" aria-label="Work on this">
        <div className="btnrow">
          <button className="b ok grow" id="th-start" onClick={() => setStarting(true)}>
            Start session or task
          </button>
          {forge && (
            <button className={`b ${issue.state === "open" ? "no" : ""}`} disabled={busy} onClick={() => void post("state", { open: issue.state !== "open" }, issue.state === "open" ? "Closed" : "Reopened")}>
              {issue.state === "open" ? "Close issue" : "Reopen"}
            </button>
          )}
        </div>
        <p className="th-muted th-branch-hint">
          Suggested branch <code className="th-branch">{issue.branch_name}</code>
        </p>
        {!forge && issue.transitions.length > 0 && (
          <label className="th-status-pick">
            {item.source === "jira" ? "Move with transition" : "Status"}
            <select
              aria-label="Status"
              value={item.source === "linear" ? statusID : ""}
              disabled={busy}
              onChange={(e) => {
                const t = issue.transitions.find((x) => x.id === e.target.value);
                if (t) void post("status", { id: t.id }, `Moved to ${t.name}`);
              }}
            >
              {item.source === "jira" && <option value="">{issue.state} — choose…</option>}
              {issue.transitions.map((t) => (
                <option key={t.id} value={t.id}>
                  {t.name}
                </option>
              ))}
            </select>
          </label>
        )}
      </section>

      {(issue.parent_item || issue.children.length > 0) && (
        <section className="th-section" aria-label="Related issues">
          {issue.parent_item && (
            <>
              <h3>Parent</h3>
              <button className="th-link-row" onClick={() => onOpen({ ...item, id: issue.parent_item!.id })}>
                <span className="th-num">{issue.parent_item.id}</span> {issue.parent_item.title}
              </button>
            </>
          )}
          {issue.children.length > 0 && (
            <>
              <h3>
                Sub-issues <span className="th-muted">{issue.children.filter((c) => ["completed", "done"].includes(c.status_type || "")).length}/{issue.children.length} done</span>
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

      <section className="th-section" aria-label="Labels">
        <h3>Labels</h3>
        <Labels labels={issue.labels} onRemove={forge ? (name) => void post("labels", { remove: [name] }, `Removed ${name}`) : undefined} />
        {!issue.labels.length && <p className="th-muted">None.</p>}
        {forge && (
          <NamePicker
            label="Add label"
            busy={busy}
            options={(meta?.labels || []).filter((l) => !issue.labels.some((x) => x.name === l.name)).map((l) => ({ value: l.name }))}
            onPick={(name) => void post("labels", { add: [name] }, `Labelled ${name}`)}
          />
        )}
      </section>

      <section className="th-section th-desc" aria-label="Description">
        <h3>Description</h3>
        <div className="th-md">{issue.body ? <Markdown text={issue.body} /> : <p className="th-muted">No description.</p>}</div>
      </section>

      <section className="th-section" aria-label="Comments">
        <h3>Comments</h3>
        <Timeline
          events={issue.timeline}
          onComment={async (body) => {
            await post("comments", { body }, "Comment posted");
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
