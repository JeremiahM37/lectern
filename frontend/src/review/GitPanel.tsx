import { useEffect, useRef, useState, type ReactNode } from "react";
import type { JsonValue } from "../api";
import { ApiError } from "../api/client";
import { DiffViewer } from "./DiffViewer";
import type { Hunk } from "./diffModel";
import type { CommitResult, CommitStep, FilePatch, GitFile, GitStatus } from "./types";

export interface ReviewApi {
  request<T>(path: string, options?: { method?: string; body?: JsonValue }): Promise<T>;
}

/** A destructive button that asks once, inline, before it acts — a native
 * confirm() is easy to fat-finger on a phone and invisible to tests. */
export function ConfirmButton({
  label,
  confirm,
  prompt,
  onConfirm,
  className = "b no",
  disabled,
}: {
  label: ReactNode;
  confirm: string;
  prompt?: ReactNode;
  onConfirm(): void;
  className?: string;
  disabled?: boolean;
}) {
  const [asking, setAsking] = useState(false);
  if (!asking)
    return (
      <button type="button" className={className} disabled={disabled} onClick={() => setAsking(true)}>
        {label}
      </button>
    );
  return (
    <span className="confirm-inline" role="group">
      {prompt && <span className="confirm-prompt">{prompt}</span>}
      <button type="button" className="b" onClick={() => setAsking(false)}>
        Cancel
      </button>
      <button
        type="button"
        className="b no"
        onClick={() => {
          setAsking(false);
          onConfirm();
        }}
      >
        {confirm}
      </button>
    </span>
  );
}

function short(sha: string) {
  return sha ? sha.slice(0, 7) : "";
}

/**
 * The local git flow for one repository of a session: staged and unstaged
 * files with per-file and per-hunk stage/unstage/discard, then a commit form
 * with an AI-written message, amend (guarded against pushed commits), push,
 * force-push-with-lease behind a confirmation, and a PR. A commit rejected by
 * a hook offers "Fix with agent", which sends the hook's output to the
 * session.
 */
export function GitPanel({
  api,
  sessionId,
  repo,
  defaultMessage,
  refreshKey,
  onChanged,
  onNotice,
  onShowConflicts,
}: {
  api: ReviewApi;
  sessionId: number;
  repo: string;
  defaultMessage?: string;
  refreshKey?: number;
  onChanged(): void;
  onNotice(text: string, error?: boolean): void;
  onShowConflicts?(): void;
}) {
  const base = `/sessions/${sessionId}/git`;
  const q = repo ? `?repo=${encodeURIComponent(repo)}` : "";
  const [status, setStatus] = useState<GitStatus>();
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [reload, setReload] = useState(0);
  // Lines ticked for line-level staging, per scope, file and hunk. Any git
  // action reloads the patches, so the choice is cleared with it.
  const [chosen, setChosen] = useState<Record<string, number[]>>({});

  const [message, setMessageState] = useState(defaultMessage ?? "");
  const messageTouched = useRef(false);
  const setMessage = (m: string) => {
    messageTouched.current = true;
    setMessageState(m);
  };
  const [amend, setAmend] = useState(false);
  const [push, setPush] = useState(true);
  const [pr, setPr] = useState(false);
  const [prTitle, setPrTitle] = useState("");
  const [prBody, setPrBody] = useState("");
  const [result, setResult] = useState<CommitResult>();
  const [pushedGuard, setPushedGuard] = useState<string[]>();
  const [forceAsk, setForceAsk] = useState(false);

  useEffect(() => {
    let live = true;
    setError("");
    api
      .request<GitStatus>(base + q)
      .then((s) => {
        if (!live) return;
        setStatus(s);
        setChosen({});
        // Finishing a merge: git's own message, unless one was typed.
        if (s.operation && s.merge_message && !messageTouched.current) setMessageState(s.merge_message);
      })
      .catch((e) => live && setError(String(e instanceof Error ? e.message : e)));
    return () => {
      live = false;
    };
  }, [sessionId, repo, reload, refreshKey]);

  const refresh = () => {
    setReload((n) => n + 1);
    onChanged();
  };

  async function act<T>(label: string, path: string, body: Record<string, JsonValue>, done?: string): Promise<T | undefined> {
    setBusy(label);
    try {
      const out = await api.request<T>(base + path, { method: "POST", body: { repo, ...body } });
      if (done) onNotice(done);
      refresh();
      return out;
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
      if (e instanceof ApiError && e.status === 409) refresh();
      return undefined;
    } finally {
      setBusy("");
    }
  }

  const files = status?.files ?? [];
  const conflicted = files.filter((f) => f.conflicted);
  const staged = files.filter((f) => f.staged);
  const unstaged = files.filter((f) => f.unstaged);
  const diverged = !!status && !!status.remote_sha && status.remote_sha !== status.head;

  const chosenKey = (scope: string, path: string, hunk: number) => `${scope}\0${path}\0${hunk}`;
  function lineChecks(scope: "staged" | "unstaged") {
    return {
      checked: (path: string, hunk: number, line: number) => !!chosen[chosenKey(scope, path, hunk)]?.includes(line),
      toggle: (path: string, hunk: number, line: number) =>
        setChosen((c) => {
          const key = chosenKey(scope, path, hunk);
          const now = c[key] ?? [];
          return { ...c, [key]: now.includes(line) ? now.filter((x) => x !== line) : [...now, line] };
        }),
    };
  }

  function hunkButtons(f: GitFile, scope: "staged" | "unstaged") {
    return (_patch: FilePatch, h: Hunk) => {
      const fp = (scope === "staged" ? f.staged_hunks : f.unstaged_hunks)[h.index];
      if (!fp) return null;
      const lines = chosen[chosenKey(scope, f.path, h.index)] ?? [];
      const what = lines.length ? `${lines.length} line${lines.length === 1 ? "" : "s"}` : "hunk";
      const hunk = (op: string) => ({
        path: f.path,
        op,
        index: h.index,
        fingerprint: fp,
        ...(lines.length ? { lines } : {}),
      });
      return scope === "staged" ? (
        <button type="button" className="b" disabled={!!busy} onClick={() => void act("hunk", "/hunk", hunk("unstage"))}>
          Unstage {what}
        </button>
      ) : (
        <>
          <button type="button" className="b ok" disabled={!!busy} onClick={() => void act("hunk", "/hunk", hunk("stage"))}>
            Stage {what}
          </button>
          <ConfirmButton
            label={`Discard ${what}`}
            confirm="Discard"
            disabled={!!busy}
            onConfirm={() => void act("hunk", "/hunk", hunk("discard"), `Discarded that ${what}.`)}
          />
        </>
      );
    };
  }

  function fileList(list: GitFile[], scope: "staged" | "unstaged") {
    return (
      <ul className="git-files">
        {list.map((f) => {
          const key = `${scope}:${f.path}`;
          const patch = scope === "staged" ? f.staged_patch : f.unstaged_patch;
          return (
            <li key={key} className="git-file" data-path={f.path}>
              <div className="git-file-row">
                <button
                  type="button"
                  className="git-file-name"
                  aria-expanded={!!expanded[key]}
                  onClick={() => setExpanded((x) => ({ ...x, [key]: !x[key] }))}
                >
                  <span className="git-status" title={f.status}>
                    {f.untracked ? "U" : (scope === "staged" ? f.status[0] : f.status[1]) || "M"}
                  </span>
                  <span className="git-path">{f.path}</span>
                </button>
                {scope === "staged" ? (
                  <button
                    type="button"
                    className="b"
                    disabled={!!busy}
                    onClick={() => void act("unstage", "/unstage", { paths: [f.path] })}
                  >
                    Unstage
                  </button>
                ) : (
                  <>
                    <button
                      type="button"
                      className="b ok"
                      disabled={!!busy}
                      onClick={() => void act("stage", "/stage", { paths: [f.path] })}
                    >
                      Stage
                    </button>
                    <ConfirmButton
                      label="Discard"
                      confirm={f.new_file ? "Delete file" : "Discard changes"}
                      disabled={!!busy}
                      onConfirm={() =>
                        void act("discard", "/discard", { paths: [f.path] }, `Discarded ${f.path}.`)
                      }
                    />
                  </>
                )}
              </div>
              {expanded[key] && (
                <DiffViewer
                  files={patch ? [{ path: f.path, patch }] : []}
                  stats={[]}
                  wrap={false}
                  hunkActions={hunkButtons(f, scope)}
                  lineChecks={lineChecks(scope)}
                  idPrefix={`git-${scope}-`}
                />
              )}
            </li>
          );
        })}
      </ul>
    );
  }

  async function writeMessage() {
    setBusy("message");
    try {
      const out = await api.request<{ message: string; agent: string; model: string }>(
        base + "/commit-message",
        { method: "POST", body: { repo } },
      );
      setMessage(out.message);
      onNotice(`Commit message written by ${out.agent}${out.model ? ` (${out.model})` : ""}.`);
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    } finally {
      setBusy("");
    }
  }

  async function generateDescription() {
    setBusy("describe");
    try {
      const out = await api.request<{ title: string; body: string }>(`/sessions/${sessionId}/pr-description`, {
        method: "POST",
      });
      setPrTitle(out.title);
      setPrBody(out.body);
    } catch (e) {
      onNotice(e instanceof Error ? e.message : String(e), true);
    } finally {
      setBusy("");
    }
  }

  async function commit(allowPushedAmend = false) {
    setBusy("commit");
    setResult(undefined);
    setPushedGuard(undefined);
    try {
      const out = await api.request<CommitResult>(base + "/commit", {
        method: "POST",
        body: {
          repo,
          message,
          stage_all: staged.length === 0,
          amend,
          allow_pushed_amend: allowPushedAmend,
          push: push && !(amend && allowPushedAmend),
          pr: pr && push,
          pr_title: prTitle,
          pr_body: prBody,
        },
      });
      setResult(out);
      const failed = out.steps.find((s) => Number(s.rc) !== 0);
      onNotice(failed ? `Commit step "${failed.step}" failed.` : amend ? "Amended." : "Committed.", Boolean(failed));
      if (!failed) {
        setAmend(false);
        if (amend && allowPushedAmend) setForceAsk(true);
      }
      refresh();
    } catch (e) {
      if (e instanceof ApiError && (e.payload as { code?: string } | undefined)?.code === "amend_pushed") {
        setPushedGuard(((e.payload as { refs?: string[] }).refs ?? []) as string[]);
      } else {
        onNotice(e instanceof Error ? e.message : String(e), true);
      }
    } finally {
      setBusy("");
    }
  }

  async function forcePush() {
    const out = await act<{ steps: CommitStep[] }>("push", "/push", {
      force_with_lease: true,
      lease: status?.remote_sha ?? "",
    });
    if (out) {
      setResult({ steps: out.steps });
      setForceAsk(false);
      const failed = out.steps.find((s) => Number(s.rc) !== 0);
      onNotice(failed ? "Force push refused: origin moved since you looked. Refresh and check." : "Force pushed.", !!failed);
    }
  }

  async function fixWithAgent() {
    if (!result?.hook_failure) return;
    await act("fix", "/fix-hook", {
      message,
      output: result.hook_failure.output,
      hooks: result.hook_failure.hooks ?? [],
    }, "Sent the hook output to the agent.");
  }

  if (error) return <p className="sub error">{error}</p>;
  if (!status) return <p className="sub">Loading git status…</p>;

  return (
    <section className="git-panel" aria-label="Git">
      <header className="git-branch">
        <span className="chip info">⎇ {status.branch || "detached"}</span>
        {status.upstream ? (
          <span className="sub">
            {status.upstream} · ↑{status.ahead} ↓{status.behind}
          </span>
        ) : (
          <span className="sub">{status.remote_sha ? "on origin" : "not pushed yet"}</span>
        )}
        {status.hooks.length > 0 && <span className="sub">hooks: {status.hooks.join(", ")}</span>}
        <button type="button" className="b" onClick={refresh}>
          Refresh
        </button>
      </header>

      {status.operation && (
        <div className="git-operation" role="status">
          <b>A {status.operation} is in progress.</b>{" "}
          {conflicted.length > 0
            ? `${conflicted.length} file(s) still in conflict.`
            : "All conflicts are resolved — commit to finish it."}
          <span className="git-operation-actions">
            {conflicted.length > 0 && onShowConflicts && (
              <button type="button" className="b ok" onClick={onShowConflicts}>
                Resolve conflicts
              </button>
            )}
            <ConfirmButton
              label={`Abort ${status.operation}`}
              confirm={`Abort the ${status.operation}`}
              onConfirm={() => void act("abort", "/abort", {}, `Aborted the ${status.operation}.`)}
            />
          </span>
        </div>
      )}

      <div className="git-section">
        <h3>
          Staged ({staged.length})
          {staged.length > 0 && (
            <button
              type="button"
              className="b"
              disabled={!!busy}
              onClick={() => void act("unstage", "/unstage", { paths: staged.map((f) => f.path) })}
            >
              Unstage all
            </button>
          )}
        </h3>
        {staged.length ? fileList(staged, "staged") : <p className="sub">Nothing staged. Commit takes every change.</p>}
      </div>
      <div className="git-section">
        <h3>
          Changes ({unstaged.length})
          {unstaged.length > 0 && (
            <button
              type="button"
              className="b ok"
              disabled={!!busy}
              onClick={() => void act("stage", "/stage", { paths: unstaged.map((f) => f.path) })}
            >
              Stage all
            </button>
          )}
        </h3>
        {unstaged.length ? fileList(unstaged, "unstaged") : <p className="sub">No unstaged changes.</p>}
        {status.truncated && <p className="sub review-truncated">Some patches were too large to show.</p>}
      </div>

      <section className="review-commit-form">
        <h3>Commit</h3>
        {status.on_base_branch && (
          <p className="sub error">
            This session works directly on {status.branch}; commit from an isolated worktree instead.
          </p>
        )}
        <label className="f" htmlFor={`commit-msg-${sessionId}`}>
          Commit message
        </label>
        <textarea
          id={`commit-msg-${sessionId}`}
          className="f commit-message"
          rows={3}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
        />
        <div className="btnrow">
          <button type="button" className="b" disabled={!!busy} onClick={() => void writeMessage()}>
            {busy === "message" ? "Writing…" : "✨ Write message"}
          </button>
        </div>
        <label className="review-checkbox">
          <input
            type="checkbox"
            checked={amend}
            onChange={(e) => {
              setAmend(e.target.checked);
              if (e.target.checked && !message.trim()) setMessage(status.head_message);
            }}
          />
          Amend the last commit{status.head_pushed ? " (already pushed)" : ""}
        </label>
        <label className="review-checkbox">
          <input type="checkbox" checked={push} onChange={(e) => setPush(e.target.checked)} />
          Push to origin
        </label>
        <label className="review-checkbox">
          <input type="checkbox" checked={pr} disabled={!push} onChange={(e) => setPr(e.target.checked)} />
          Open a PR (needs <code>gh</code> on the target)
        </label>
        {pr && (
          <>
            <label className="f">
              PR title
              <input className="f" value={prTitle} onChange={(e) => setPrTitle(e.target.value)} placeholder={message} />
            </label>
            <label className="f">
              PR body
              <textarea className="f" rows={4} value={prBody} onChange={(e) => setPrBody(e.target.value)} />
            </label>
            <button type="button" className="b" disabled={!!busy} onClick={() => void generateDescription()}>
              {busy === "describe" ? "Generating…" : "✨ Generate description"}
            </button>
          </>
        )}
        <p className="sub commit-scope">
          {staged.length
            ? `Commits the ${staged.length} staged file${staged.length === 1 ? "" : "s"}.`
            : "Nothing is staged, so this commits every change."}
        </p>
        <button
          type="button"
          className="b ok"
          disabled={!!busy || !message.trim() || status.on_base_branch || !status.session_live}
          onClick={() => void commit()}
        >
          {busy === "commit" ? "Committing…" : amend ? "⎇ Amend commit" : "⎇ Commit"}
        </button>

        {pushedGuard && (
          <div className="git-guard" role="alert">
            <p>
              The last commit is already on <b>{pushedGuard.join(", ")}</b>. Amending it rewrites published history,
              and origin will then need a force push.
            </p>
            <div className="btnrow">
              <button type="button" className="b" onClick={() => setPushedGuard(undefined)}>
                Keep it
              </button>
              <button type="button" className="b no" onClick={() => void commit(true)}>
                Amend anyway
              </button>
            </div>
          </div>
        )}

        {(diverged || forceAsk) && status.branch && (
          <div className="git-force">
            {forceAsk || diverged ? (
              <ConfirmButton
                label="Force push (with lease)…"
                confirm="Force push"
                className="b warn"
                prompt={
                  <>
                    Replace origin/{status.branch}
                    {status.remote_sha ? ` (${short(status.remote_sha)})` : ""} with your {short(status.head)}? The push
                    is refused if origin has moved since this status was loaded.
                  </>
                }
                onConfirm={() => void forcePush()}
              />
            ) : null}
          </div>
        )}

        {result && (
          <ul className="review-commit-steps">
            {result.steps.map((s, i) => (
              <li key={i} data-ok={Number(s.rc) === 0}>
                <b>{s.step}</b>: {Number(s.rc) === 0 ? "ok" : "failed"}
                {s.url && (
                  <>
                    {" — "}
                    <a href={s.url} target="_blank" rel="noreferrer">
                      {s.url}
                    </a>
                  </>
                )}
                {Number(s.rc) !== 0 && s.output && <pre className="review-check-output">{s.output}</pre>}
              </li>
            ))}
          </ul>
        )}
        {result?.hook_failure && (
          <div className="git-hook-failure" role="alert">
            <p>
              The {(result.hook_failure.hooks ?? []).join("/") || "commit"} hook rejected this commit.
            </p>
            <button type="button" className="b ok" disabled={!!busy} onClick={() => void fixWithAgent()}>
              🛠 Fix with agent
            </button>
          </div>
        )}
      </section>
    </section>
  );
}
