import { useEffect, useRef, useState, type ReactNode } from "react";
import type { JsonValue } from "../api";
import { ApiError } from "../api/client";
import { DiffViewer } from "./DiffViewer";
import type { Hunk } from "./diffModel";
import type { CommitResult, CommitStep, FilePatch, GitFile, GitStatus } from "./types";
import { t, useLocale } from "../i18n";
import { branchFor } from "./branch";

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
  useLocale();
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
        {t("review.action.cancel")}
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
  useLocale();
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

  // Starts from what the person asked for or what changed (suggested_message,
  // once the status loads) — the session's name is not a commit message.
  const [message, setMessageState] = useState("");
  const messageTouched = useRef(false);
  // Set once a drafted message has replaced the suggestion (below).
  const drafted = useRef(false);
  const setMessage = (m: string) => {
    messageTouched.current = true;
    setMessageState(m);
  };
  const [amend, setAmend] = useState(false);
  const [push, setPush] = useState(true);
  // On the default branch: commit on a new branch (the default) or, after
  // saying so, on the default branch itself.
  const [onMain, setOnMain] = useState<"branch" | "main">("branch");
  const [newBranch, setNewBranch] = useState(() => branchFor(defaultMessage || ""));
  const [mainConfirmed, setMainConfirmed] = useState(false);
  const remoteKnown = useRef(false);
  const [pr, setPr] = useState(false);
  const [prTitle, setPrTitle] = useState("");
  const [prBody, setPrBody] = useState("");
  const [result, setResult] = useState<CommitResult>();
  const [pushedGuard, setPushedGuard] = useState<string[]>();
  const [noIdentity, setNoIdentity] = useState(false);
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
        // Nowhere to push to: Push starts unticked (once, so a later tick sticks).
        if (!remoteKnown.current && s.has_remote === false) setPush(false);
        remoteKnown.current = true;
        // Finishing a merge: git's own message, unless one was typed.
        if (s.operation && s.merge_message && !messageTouched.current) setMessageState(s.merge_message);
        else if (!messageTouched.current && !drafted.current && s.suggested_message) setMessageState(s.suggested_message);
      })
      .catch((e) => live && setError(String(e instanceof Error ? e.message : e)));
    return () => {
      live = false;
    };
  }, [sessionId, repo, reload, refreshKey]);

  // Draft once from the actual diff. Never replace text the user starts typing
  // while the model is answering, and retain manual entry if it is unavailable.
  const draftRequested = useRef(false);
  useEffect(() => {
    if (!status?.files.length || status.operation || messageTouched.current || draftRequested.current) return;
    draftRequested.current = true;
    let live = true;
    // The suggestion from the changed files stays until the draft arrives,
    // and is what is left if drafting is unavailable.
    api.request<{ message: string }>(base + "/commit-message", { method: "POST", body: { repo } })
      .then((draft) => {
        if (live && !messageTouched.current && draft.message) {
          drafted.current = true;
          setMessageState(draft.message);
        }
      })
      .catch(() => { /* Manual entry and Write message remain available. */ });
    return () => { live = false; };
  }, [!!status?.files.length, sessionId, repo]);

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
      const hunk = (op: string) => ({
        path: f.path,
        op,
        index: h.index,
        fingerprint: fp,
        ...(lines.length ? { lines } : {}),
      });
      return scope === "staged" ? (
        <button type="button" className="b" disabled={!!busy} onClick={() => void act("hunk", "/hunk", hunk("unstage"))}>
          {lines.length ? t("review.git.unstageLines", { count: lines.length }) : t("review.git.unstageHunk")}
        </button>
      ) : (
        <>
          <button type="button" className="b ok" disabled={!!busy} onClick={() => void act("hunk", "/hunk", hunk("stage"))}>
            {lines.length ? t("review.git.stageLines", { count: lines.length }) : t("review.git.stageHunk")}
          </button>
          <ConfirmButton
            label={lines.length ? t("review.git.discardLines", { count: lines.length }) : t("review.git.discardHunk")}
            confirm={t("review.git.discard")}
            disabled={!!busy}
            onConfirm={() =>
              void act(
                "hunk",
                "/hunk",
                hunk("discard"),
                lines.length ? t("review.git.discardedLines", { count: lines.length }) : t("review.git.discardedHunk"),
              )
            }
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
                    {t("review.git.unstage")}
                  </button>
                ) : (
                  <>
                    <button
                      type="button"
                      className="b ok"
                      disabled={!!busy}
                      onClick={() => void act("stage", "/stage", { paths: [f.path] })}
                    >
                      {t("review.git.stage")}
                    </button>
                    <ConfirmButton
                      label={t("review.git.discard")}
                      confirm={f.new_file ? t("review.git.deleteFile") : t("review.git.discardChanges")}
                      disabled={!!busy}
                      onConfirm={() =>
                        void act("discard", "/discard", { paths: [f.path] }, t("review.git.discardedPath", { path: f.path }))
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
      onNotice(
        out.model
          ? t("review.git.messageByModel", { agent: out.agent, model: out.model })
          : t("review.git.messageBy", { agent: out.agent }),
      );
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
    setNoIdentity(false);
    try {
      const out = await api.request<CommitResult>(base + "/commit", {
        method: "POST",
        body: {
          repo,
          message,
          stage_all: staged.length === 0,
          amend,
          allow_pushed_amend: allowPushedAmend,
          push: push && status?.has_remote !== false && !(amend && allowPushedAmend),
          ...(status?.on_base_branch
            ? onMain === "branch"
              ? { new_branch: newBranch.trim() }
              : { allow_base_branch: true }
            : {}),
          pr: pr && push,
          pr_title: prTitle,
          pr_body: prBody,
        },
      });
      setResult(out);
      const failed = out.steps.find((s) => Number(s.rc) !== 0);
      const movedTo = status?.on_base_branch && onMain === "branch" && out.branch ? out.branch : "";
      onNotice(
        failed
          ? t("review.git.stepFailed", { step: failed.step })
          : amend
            ? t("review.git.amended")
            : movedTo
              ? status?.own_checkout
                ? t("review.onMain.folderNowOn", { branch: movedTo, dir: status.dir || "" })
                : t("review.onMain.committedOn", { branch: movedTo })
              : t("review.git.committed"),
        Boolean(failed),
      );
      if (!failed) {
        setAmend(false);
        if (amend && allowPushedAmend) setForceAsk(true);
      }
      refresh();
    } catch (e) {
      if (e instanceof ApiError && (e.payload as { code?: string } | undefined)?.code === "no_git_identity") {
        setNoIdentity(true);
      } else if (e instanceof ApiError && (e.payload as { code?: string } | undefined)?.code === "amend_pushed") {
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
      onNotice(failed ? t("review.git.forceRefused") : t("review.git.forcePushed"), !!failed);
    }
  }

  async function fixWithAgent() {
    if (!result?.hook_failure) return;
    await act("fix", "/fix-hook", {
      message,
      output: result.hook_failure.output,
      hooks: result.hook_failure.hooks ?? [],
    }, t("review.git.hookSent"));
  }

  if (error) return <p className="sub error">{error}</p>;
  // Sentences with markup inside: the text around the <code>/<b> element.
  const openPr = t("review.git.openPr").split("{gh}");
  const alreadyPushed = t("review.git.alreadyPushed").split("{refs}");
  if (!status) return <p className="sub">{t("review.git.loading")}</p>;
  // Every reason Commit can be unavailable, said next to it (never a button
  // that is simply greyed out).
  const commitBlocked = !status.session_live
    ? t("review.why.ended")
    : !message.trim()
      ? t("review.why.noMessage")
      : status.on_base_branch && onMain === "branch" && !newBranch.trim()
        ? t("review.why.noBranch")
        : status.on_base_branch && onMain === "main" && !mainConfirmed
          ? t("review.why.confirmMain", { branch: status.branch })
          : "";

  return (
    <section className="git-panel" aria-label="Git">
      <header className="git-branch">
        <span className="chip info">⎇ {status.branch || t("review.git.detached")}</span>
        {status.upstream ? (
          <span className="sub">
            {status.upstream} · ↑{status.ahead} ↓{status.behind}
          </span>
        ) : (
          <span className="sub">{status.remote_sha ? t("review.git.onOrigin") : t("review.git.notPushed")}</span>
        )}
        {status.hooks.length > 0 && <span className="sub">{t("review.git.hooks", { hooks: status.hooks.join(", ") })}</span>}
        <button type="button" className="b" onClick={refresh}>
          {t("review.git.refresh")}
        </button>
      </header>

      {status.operation && (
        <div className="git-operation" role="status">
          <b>{t("review.git.inProgress", { operation: status.operation })}</b>{" "}
          {conflicted.length > 0
            ? t("review.git.stillInConflict", { n: conflicted.length })
            : t("review.git.allResolved")}
          <span className="git-operation-actions">
            {conflicted.length > 0 && onShowConflicts && (
              <button type="button" className="b ok" onClick={onShowConflicts}>
                {t("review.git.resolveConflicts")}
              </button>
            )}
            <ConfirmButton
              label={t("review.git.abort", { operation: status.operation })}
              confirm={t("review.git.abortThe", { operation: status.operation })}
              onConfirm={() => void act("abort", "/abort", {}, t("review.git.aborted", { operation: status.operation }))}
            />
          </span>
        </div>
      )}

      <div className="git-section">
        <h3>
          {t("review.git.staged", { n: staged.length })}
          {staged.length > 0 && (
            <button
              type="button"
              className="b"
              disabled={!!busy}
              onClick={() => void act("unstage", "/unstage", { paths: staged.map((f) => f.path) })}
            >
              {t("review.git.unstageAll")}
            </button>
          )}
        </h3>
        {staged.length ? fileList(staged, "staged") : <p className="sub">{t("review.git.nothingStaged")}</p>}
      </div>
      <div className="git-section">
        <h3>
          {t("review.git.changes", { n: unstaged.length })}
          {unstaged.length > 0 && (
            <button
              type="button"
              className="b ok"
              disabled={!!busy}
              onClick={() => void act("stage", "/stage", { paths: unstaged.map((f) => f.path) })}
            >
              {t("review.git.stageAll")}
            </button>
          )}
        </h3>
        {unstaged.length ? fileList(unstaged, "unstaged") : <p className="sub">{t("review.git.noUnstaged")}</p>}
        {status.truncated && <p className="sub review-truncated">{t("review.git.truncated")}</p>}
      </div>

      <section className="review-commit-form">
        <h3>{t("review.git.commitHeading")}</h3>
        {status.on_base_branch && (
          <fieldset className="commit-on-main" id={`commit-on-main-${sessionId}`}>
            <legend>{t("review.onMain.title", { branch: status.branch })}</legend>
            <label className="review-checkbox">
              <input type="radio" name={`on-main-${sessionId}`} checked={onMain === "branch"} onChange={() => setOnMain("branch")} />
              {t("review.onMain.newBranch")}
            </label>
            {onMain === "branch" && (
              <input
                className="f commit-new-branch"
                aria-label={t("review.onMain.branchName")}
                value={newBranch}
                onChange={(e) => setNewBranch(e.target.value)}
              />
            )}
            {onMain === "branch" && status.own_checkout && (
              <p className="sub commit-own-folder" role="note">
                {t("review.onMain.ownFolder", { branch: newBranch.trim() || "…", dir: status.dir || "" })}
              </p>
            )}
            <label className="review-checkbox">
              <input type="radio" name={`on-main-${sessionId}`} checked={onMain === "main"} onChange={() => setOnMain("main")} />
              {t("review.onMain.direct", { branch: status.branch })}
            </label>
            {onMain === "main" && (
              <label className="review-checkbox commit-main-confirm">
                <input type="checkbox" checked={mainConfirmed} onChange={(e) => setMainConfirmed(e.target.checked)} />
                {t("review.onMain.confirm", { branch: status.branch })}
              </label>
            )}
          </fieldset>
        )}
        <label className="f" htmlFor={`commit-msg-${sessionId}`}>
          {t("review.git.commitMessage")}
        </label>
        <textarea
          id={`commit-msg-${sessionId}`}
          className="f commit-message"
          rows={3}
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          // Once someone is in the field it is theirs: a draft arriving now
          // would land in the middle of what they are typing.
          onFocus={() => {
            messageTouched.current = true;
          }}
        />
        <div className="btnrow">
          <button type="button" className="b" disabled={!!busy} onClick={() => void writeMessage()}>
            {busy === "message" ? t("review.git.writing") : t("review.git.writeMessage")}
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
          {status.head_pushed ? t("review.git.amendLastPushed") : t("review.git.amendLast")}
        </label>
        <label className="review-checkbox">
          <input type="checkbox" checked={push && status.has_remote !== false} disabled={status.has_remote === false} onChange={(e) => setPush(e.target.checked)} />
          {t("review.git.pushToOrigin")}
          {status.has_remote === false && <span className="sub disabled-why"> — {t("review.why.noRemote")}</span>}
        </label>
        <label className="review-checkbox">
          <input type="checkbox" checked={pr} disabled={!push || status.has_remote === false} onChange={(e) => setPr(e.target.checked)} />
          {openPr[0]}
          <code>gh</code>
          {openPr[1]}
          {(!push || status.has_remote === false) && <span className="sub disabled-why"> — {t("review.why.prNeedsPush")}</span>}
        </label>
        {pr && (
          <>
            <label className="f">
              {t("review.git.prTitle")}
              <input className="f" value={prTitle} onChange={(e) => setPrTitle(e.target.value)} placeholder={message} />
            </label>
            <label className="f">
              {t("review.git.prBody")}
              <textarea className="f" rows={4} value={prBody} onChange={(e) => setPrBody(e.target.value)} />
            </label>
            <button type="button" className="b" disabled={!!busy} onClick={() => void generateDescription()}>
              {busy === "describe" ? t("review.git.generating") : t("review.git.generateDescription")}
            </button>
          </>
        )}
        <p className="sub commit-scope">
          {staged.length
            ? t("review.git.commitsStaged", { count: staged.length })
            : t("review.git.commitsEverything")}
        </p>
        <button
          type="button"
          className="b ok"
          disabled={!!busy || !!commitBlocked}
          aria-describedby={commitBlocked ? `commit-why-${sessionId}` : undefined}
          title={commitBlocked || undefined}
          onClick={() => void commit()}
        >
          {busy === "commit"
            ? t("review.git.committing")
            : amend
              ? t("review.git.amendCommit")
              : status.on_base_branch && onMain === "branch"
                ? t("review.onMain.commitOnBranch", { branch: newBranch.trim() || "…" })
                : t("review.git.commit")}
        </button>
        {commitBlocked && (
          <p className="sub disabled-why" id={`commit-why-${sessionId}`}>
            {commitBlocked}
          </p>
        )}

        {pushedGuard && (
          <div className="git-guard" role="alert">
            <p>
              {alreadyPushed[0]}
              <b>{pushedGuard.join(", ")}</b>
              {alreadyPushed[1]}
            </p>
            <div className="btnrow">
              <button type="button" className="b" onClick={() => setPushedGuard(undefined)}>
                {t("review.git.keepIt")}
              </button>
              <button type="button" className="b no" onClick={() => void commit(true)}>
                {t("review.git.amendAnyway")}
              </button>
            </div>
          </div>
        )}

        {(diverged || forceAsk) && status.branch && (
          <div className="git-force">
            {forceAsk || diverged ? (
              <ConfirmButton
                label={t("review.git.forcePushLease")}
                confirm={t("review.git.forcePush")}
                className="b warn"
                prompt={
                  status.remote_sha
                    ? t("review.git.forcePromptRemote", { branch: status.branch, remote: short(status.remote_sha), head: short(status.head) })
                    : t("review.git.forcePrompt", { branch: status.branch, head: short(status.head) })
                }
                onConfirm={() => void forcePush()}
              />
            ) : null}
          </div>
        )}

        {noIdentity && (
          <div className="commit-no-identity" role="alert">
            <p>{t("review.git.noIdentity")}</p>
            <pre>{'git config --global user.name "Your Name"\ngit config --global user.email you@example.com'}</pre>
          </div>
        )}
        {result && (
          <ul className="review-commit-steps">
            {result.steps.map((s, i) => (
              <li key={i} data-ok={Number(s.rc) === 0}>
                <b>{s.step}</b>: {Number(s.rc) === 0 ? t("review.git.stepOk") : t("review.git.stepFailedShort")}
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
              {t("review.git.hookRejected", { hooks: (result.hook_failure.hooks ?? []).join("/") || t("review.git.hookCommit") })}
            </p>
            <button type="button" className="b ok" disabled={!!busy} onClick={() => void fixWithAgent()}>
              {t("review.git.fixWithAgent")}
            </button>
          </div>
        )}
      </section>
    </section>
  );
}

