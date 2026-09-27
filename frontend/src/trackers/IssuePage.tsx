import { useCallback, useEffect, useState } from "react";
import { Markdown } from "../sessions/markdown";
import { errorText, issuePath, type Notice, type TrackerApi } from "./api";
import { Labels, NamePicker, ReactionBar, StatePill, Timeline } from "./bits";
import { RichEditor } from "./RichEditor";
import { ago, canReact, isForge, itemMark, SOURCE_NAME } from "./logic";
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
  const forge = isForge(item.source);
  const [issue, setIssue] = useState<IssueDetail>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [meta, setMeta] = useState<ForgeMeta>();
  const [starting, setStarting] = useState(false);
  const [editing, setEditing] = useState<string>();
  const [lossyOK, setLossyOK] = useState(false);
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

  async function saveDescription() {
    if (editing === undefined) return;
    setBusy(true);
    try {
      const fresh = await api.request<IssueDetail>(`${path}/description`, { method: "POST", body: { body: editing, confirm_lossy: lossyOK } });
      if (fresh && "id" in fresh) setIssue(fresh);
      else await load();
      setEditing(undefined);
      onNotice("Description saved");
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
          <ReactionBar reactions={issue.reactions} label={itemMark(issue)}
            onReact={canReact(item.source) ? async (emoji) => { await post("reactions", { emoji }, "Reaction added"); } : undefined} />
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
        <h3>
          Description
          {issue.editable && editing === undefined && (
            <button type="button" className="b th-edit-btn" onClick={() => { setEditing(issue.body); setLossyOK(false); }}>
              Edit
            </button>
          )}
        </h3>
        {editing !== undefined ? (
          <form
            className="th-desc-edit"
            onSubmit={(e) => {
              e.preventDefault();
              void saveDescription();
            }}
          >
            {issue.body_lossy && (
              <label className="th-warn th-check">
                <input type="checkbox" checked={lossyOK} onChange={(e) => setLossyOK(e.target.checked)} />
                This description has formatting the editor cannot keep (a table, panel, colour or attachment). Saving replaces it.
              </label>
            )}
            <RichEditor label="Description" value={editing} onChange={setEditing} rows={10} autoFocus />
            <div className="btnrow">
              <button className="b ok" type="submit" disabled={busy || (issue.body_lossy && !lossyOK)}>
                {busy ? "Saving…" : "Save description"}
              </button>
              <button className="b" type="button" onClick={() => setEditing(undefined)}>
                Cancel
              </button>
            </div>
          </form>
        ) : (
          <div className="th-md">{issue.body ? <Markdown text={issue.body} /> : <p className="th-muted">No description.</p>}</div>
        )}
      </section>

      <section className="th-section" aria-label="Comments">
        <h3>Comments</h3>
        <Timeline
          events={issue.timeline}
          onComment={async (body) => {
            await post("comments", { body }, "Comment posted");
          }}
          onReact={canReact(item.source) ? async (subject, emoji) => { await post("reactions", { subject, emoji }, "Reaction added"); } : undefined}
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
