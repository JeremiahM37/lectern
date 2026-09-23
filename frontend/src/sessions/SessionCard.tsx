import { SessionLineage } from "../continuity/SessionLineage";
import { SessionMemory } from "./SessionMemory";
import { useState } from "react";
import type { InteractiveWorkspace, Project, SessionView } from "../types";
import type { SessionsApi } from "./Sessions";
import { ActionMenu } from "./ActionMenu";
import {
  isScratchTerminal,
  scratchDefaultName,
  scratchTitle,
} from "./scratch";
export function duration(seconds: number) {
  seconds = Math.max(0, Math.floor(seconds || 0));
  return seconds < 60
    ? `${seconds}s`
    : seconds < 3600
      ? `${Math.floor(seconds / 60)}m`
      : seconds < 86400
        ? `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`
        : `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`;
}
interface Props {
  session: SessionView;
  projects: Project[];
  api: SessionsApi;
  progressError?: string;
  mediaCount?: number;
  onMedia?(sessionID: number): void;
  onRefresh: () => Promise<void>;
  onNotice: (text: string, error?: boolean) => void;
  onAttach: (session: SessionView) => void;
  onChat: (session: SessionView) => void;
  onReview: (session: SessionView) => void;
  // Opens the review-and-merge panel (live diff, inline comments, commit/
  // push/PR) — a different thing from onReview's working-tree file browser.
  // Optional so existing call sites (and tests) that predate it keep compiling.
  onMergeReview?: (session: SessionView) => void;
  onHandoff: (session: SessionView) => void;
  onSwitch?: (session: SessionView) => void;
  onGroup: (session: SessionView) => void;
  onHistory: (session: SessionView) => void;
  onWorkspace: (session: SessionView) => void;
  onArchive: (session: SessionView) => void;
  onDiscover: () => void;
}
export function SessionCard({
  session: s,
  projects,
  api,
  progressError,
  mediaCount = 0,
  onMedia = () => {},
  onRefresh,
  onNotice,
  onAttach,
  onChat,
  onReview,
  onMergeReview,
  onHandoff,
  onSwitch,
  onGroup,
  onHistory,
  onWorkspace,
  onArchive,
  onDiscover,
}: Props) {
  const [progress, setProgress] = useState(""),
    [progressBusy, setProgressBusy] = useState(false);
  const setup = s.setup_state === "creating",
    failed = s.setup_state === "failed",
    ended = s.ended_at != null,
    archived = s.archived_at != null,
    adopted = s.origin === "discovered",
    scratch = isScratchTerminal(s),
    // A blank shell is named after the folder the target made for it, so two
    // "Shell · <target>" cards are told apart. An explicit rename still wins.
    scratchPath = scratch ? s.workdir || s.workspace?.path || "" : "",
    cardTitle = scratch ? scratchTitle(s) : s.name,
    live = !ended && s.status !== "dead" && !setup && !failed,
    // A blank shell is a terminal, not a conversation: chat would be a worse
    // way to drive it, so the card keeps the terminal as its one action.
    chatReady = live && s.agent !== "shell",
    workspace = s.workspace;
  async function run(
    path: string,
    method = "POST",
    body?: { [key: string]: string | number | boolean | null },
    message?: string,
  ) {
    try {
      await api.request(path, { method, body });
      if (message) onNotice(message);
      await onRefresh();
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  const action = (
    name: string,
    method = "POST",
    body?: { [key: string]: string | number | boolean | null },
    message?: string,
  ) => run(`/sessions/${s.id}/${name}`, method, body, message);
  const cancel = () =>
    action(
      "setup/cancel",
      "POST",
      {},
      "Cancellation requested. Files already created will be retained.",
    );
  // The one promotion path, shared by the visible scratch action and the
  // actions menu. A bare shell has no conversation to wrap, and a wrap prompt
  // would be typed into the shell itself, so only an AI session wraps; either
  // way the directory and the running terminal are untouched.
  function makeProject() {
    const suggested =
      (s.workdir || "")
        .split("/")
        .filter(Boolean)
        .pop()
        ?.replace(/-\d{8}-[A-Za-z0-9]{6}$/, "") || s.name;
    const name = prompt(
      `Make this a project.\n\n${s.workdir}\n\nIt stays exactly where it is. Name it:`,
      suggested,
    );
    if (name !== null)
      void action(
        "promote",
        "POST",
        { name: name.trim(), wrap: !scratch },
        "Project created. The session keeps running.",
      );
  }
  function end(kill: boolean) {
    void run(`/sessions/${s.id}${kill ? "?kill=true" : ""}`, "DELETE");
  }
  // Renaming a scratch card is a convenience on top of the folder title: the
  // directory stays where it is, only the tracked row's name changes.
  function rename() {
    const next = prompt(
      "Rename this scratch terminal. Its folder is untouched.",
      cardTitle,
    );
    if (next === null || !next.trim()) return;
    void run(`/sessions/${s.id}`, "PATCH", { name: next.trim() }, "Renamed.");
  }
  async function copyPath() {
    try {
      await navigator.clipboard.writeText(scratchPath);
      onNotice("Scratch folder path copied.");
    } catch {
      onNotice("Copy failed. Select the path and copy it manually.", true);
    }
  }
  const status = setup
    ? s.setup_cancel_requested
      ? "cancelling"
      : "setting up"
    : failed
      ? "setup failed"
      : archived
        ? "archived"
        : ended
          ? s.status === "dead"
            ? "ended"
            : "untracked"
          : {
              waiting: "wants you",
              running: "working",
              starting: "starting",
              idle: "idle",
              dead: "ended",
            }[s.status] || s.status;
  const preview = setup
    ? (s.setup_cancel_requested
        ? "Cancellation requested. Waiting for checkout to stop; files will be retained."
        : "Setting up workspace… Attach becomes available when setup finishes.") +
      (workspace?.repositories || [])
        .map((repo) => `\n${repo.name}: ${repo.worktree.state}`)
        .join("") +
      (s.setup_error ? "\n" + s.setup_error : "") +
      (progressError ? "\nProgress unavailable: " + progressError : "")
    : s.setup_error
      ? "Setup failed: " + s.setup_error
      : s.pane_tail || "";
  return (
    <article
      className={`scard s-${failed ? "failed" : s.status}`}
      data-session-id={s.id}
      data-scratch-terminal={scratch ? "true" : undefined}
    >
      {/* A blank shell has no project, so its location line names the machine
          instead: the card's title is the folder, and this keeps "which host"
          readable without repeating the folder. */}
      <div className="scard-project">
        {scratch ? scratchDefaultName(s) : s.project_name || "Unassigned"}
      </div>
      <div className="scard-top">
        <span className={`dot ${s.status === "running" ? "live" : ""}`} />
        <span className="nm">{cardTitle}</span>
        <span className="sstate">{status}</span>
        <span className="sidle">
          {s.status === "dead" || setup
            ? ""
            : "quiet " + duration(s.idle_seconds)}
        </span>
      </div>
      {scratch && scratchPath && (
        <div className="scard-path">
          <code title={scratchPath}>{scratchPath}</code>
          <button className="b copy-path" onClick={() => void copyPath()}>
            Copy path
          </button>
        </div>
      )}
      <div className="smeta">
        <span className="chip">
          {s.agent}
          {s.model ? " · " + s.model : ""}
        </span>
        <span className="chip tgt">{s.target_name}</span>
        <span className="chip">
          {setup ? "setup" : "up"} {duration(s.uptime_seconds)}
        </span>
        {mediaCount > 0 && (
          <button
            className="chip media-chip"
            title="Recordings, files and links this session posted"
            onClick={() => onMedia(s.id)}
          >
            ▶ {mediaCount} media
          </button>
        )}
        {s.launch_profile && (
          <span className="chip" title="Captured launch profile">
            {s.launch_profile}
          </span>
        )}
        {adopted && (
          <span
            className="chip info"
            title="started outside lectern and adopted"
          >
            adopted
          </span>
        )}
        {!!s.wraps && <span className="chip info">⇥ {s.wraps}</span>}
        {s.handoff_in_flight && (
          <span className="chip warn">writing handoff…</span>
        )}
        {s.context_pct != null && (
          <span
            className={`ctxbar ${s.context_pct <= 10 ? "crit" : s.context_pct <= 25 ? "low" : ""}`}
          >
            ctx{" "}
            <i>
              <b
                style={{
                  width: `${Math.max(0, Math.min(100, s.context_pct))}%`,
                }}
              />
            </i>{" "}
            {s.context_pct}%
          </span>
        )}
        {s.group_path && <span className="chip">{s.group_path}</span>}
      </div>
      <div className="spane">{preview}</div>
      {workspace && (
        <details className="session-worktree">
          <summary>
            {workspace.repositories?.length ? "Workspace" : "Worktree"} ·{" "}
            {workspace.branch} · {workspace.state}
          </summary>
          <code>{workspace.path}</code>
          <small>
            {workspace.repositories?.length
              ? `${workspace.repositories.length} repositories`
              : `Base: ${workspace.base} · ${workspace.commit?.slice(0, 12) || "not created"}`}
          </small>
          {workspace.error && <p>Setup error: {workspace.error}</p>}
          {(
            workspace.repositories || [
              { name: s.project_name || "Repository", worktree: workspace },
            ]
          ).map(
            (entry, index) =>
              entry.worktree.setup_command && (
                <pre className="workspace-setup-output" key={index}>
                  {entry.name} setup:{" "}
                  {entry.worktree.setup_state || "not completed"}
                  {"\n"}
                  {entry.worktree.setup_output || ""}
                </pre>
              ),
          )}
          {!!workspace.repositories?.length && (
            <>
              <button
                className="b"
                disabled={progressBusy}
                onClick={async () => {
                  setProgressBusy(true);
                  try {
                    const current = await api.request<InteractiveWorkspace>(
                      `/sessions/${s.id}/worktree`,
                    );
                    setProgress(
                      `Recorded workspace state: ${current.state}\n` +
                        (current.repositories || [])
                          .map(
                            (repo) =>
                              `${repo.name}: ${repo.worktree.state}${repo.worktree.error ? " — " + repo.worktree.error : ""}`,
                          )
                          .join("\n") +
                        (current.error ? "\n" + current.error : ""),
                    );
                  } catch (error) {
                    setProgress(String(error));
                  } finally {
                    setProgressBusy(false);
                  }
                }}
              >
                Refresh setup progress
              </button>
              <pre aria-live="polite">{progress}</pre>
            </>
          )}
        </details>
      )}
      <SessionLineage api={api} session={s} onOpen={onChat} className="session-lineage" />
      <div className="btnrow">
        {setup && (
          <>
            <button className="b" disabled>
              Setting up
            </button>
            <button className="b no" onClick={() => void cancel()}>
              {s.setup_cancel_requested ? "Retry cancellation" : "Cancel setup"}
            </button>
          </>
        )}
        {live && (
          <>
            <button
              className={`b attach${chatReady ? "" : " grow"}`}
              onClick={() => onAttach(s)}
            >
              ⌨ Attach
            </button>
            {chatReady && (
              <button className="b grow chat-open" onClick={() => onChat(s)}>
                Chat
              </button>
            )}
            {scratch && (
              <button className="b ok make-project" onClick={makeProject}>
                ⇑ Make a project
              </button>
            )}
            {scratch && (
              <button className="b rename" onClick={rename}>
                ✎ Rename
              </button>
            )}
            {onSwitch && s.agent !== "shell" && <button className="b" disabled={s.handoff_in_flight} onClick={()=>onSwitch(s)}>{s.handoff_in_flight ? "Switching…" : "⇄ Switch"}</button>}
          </>
        )}
        {ended && !archived && s.can_restore && (
          <button
            className="b ok"
            onClick={() =>
              void action(
                "restore",
                "POST",
                {},
                "Tracking restored. Your session keeps running.",
              )
            }
          >
            Track again
          </button>
        )}
        <ActionMenu name={s.name}>
          {live && (
            <>
              <button className="b" onClick={() => onReview(s)}>
                Review changes
              </button>
              {onMergeReview && (
                <button className="b" onClick={() => onMergeReview(s)}>
                  Review &amp; merge
                </button>
              )}
              <a className="b" href={`lectern://attach/session/${s.id}`}>
                Open in terminal
              </a>
              {s.status === "running" && (
                <button
                  className="b warn"
                  onClick={() => void action("send", "POST", { key: "escape" })}
                >
                  ⎋ Interrupt
                </button>
              )}
              <button className="b" onClick={() => onHandoff(s)}>
                ⇥ Handoff
              </button>
              {!s.project_id && !scratch && (
                <button className="b ok" onClick={makeProject}>
                  ⇑ Make a project
                </button>
              )}
            </>
          )}
          {failed && workspace?.state !== "removed" && (
            <button className="b" onClick={() => void cancel()}>
              Cancel remaining checkout
            </button>
          )}
          <button className="b" onClick={() => onGroup(s)}>
            Move to group
          </button>
          {["claude", "codex"].includes(s.agent) && (
            <button className="b" onClick={() => onHistory(s)}>
              Saved conversations
            </button>
          )}
          {workspace && (
            <>
              {!!workspace.repositories?.length &&
                !setup &&
                workspace.state !== "removed" && (
                  <button className="b" onClick={() => onWorkspace(s)}>
                    Workspace repositories
                  </button>
                )}
              {(failed || workspace.state === "failed") &&
                workspace.state !== "removed" && (
                  <button
                    className="b"
                    onClick={() =>
                      void action(
                        "worktree/recover",
                        "POST",
                        {},
                        "Allocation validated; files retained. Setup did not restart.",
                      )
                    }
                  >
                    Recover allocation
                  </button>
                )}
              {workspace.state !== "removed" && (
                <button
                  className="b"
                  onClick={() => {
                    if (
                      confirm(
                        `Remove ${workspace.path}? End its sessions first. Changed, untracked or ignored files prevent removal. The Git branch is kept.`,
                      )
                    )
                      void action(
                        "worktree",
                        "DELETE",
                        undefined,
                        "Worktree removed; branch kept.",
                      );
                  }}
                >
                  Remove worktree
                </button>
              )}
            </>
          )}
          {archived ? (
            <>
              <button className="b" onClick={() => onArchive(s)}>
                Archived terminal output
              </button>
              <button
                className="b"
                onClick={() =>
                  void action(
                    "archive",
                    "DELETE",
                    undefined,
                    "Record unarchived. Its terminal stays stopped; find it under Include ended and untracked.",
                  )
                }
              >
                Unarchive record
              </button>
            </>
          ) : ended ? (
            <>
              <button
                className="b"
                onClick={() => void action("archive", "POST", { stop: false })}
              >
                Archive stopped record
              </button>
              {!s.can_restore && (
                <button className="b" onClick={onDiscover}>
                  Find running sessions
                </button>
              )}
            </>
          ) : s.status === "dead" ? (
            <button className="b no" onClick={() => end(false)}>
              Dismiss
            </button>
          ) : setup ? null : adopted ? (
            <>
              <button className="b" onClick={() => end(false)}>
                Stop tracking
              </button>
              <button
                className="b no"
                onClick={() => {
                  if (
                    confirm(
                      `Kill "${s.name}"?\n\nThis ends the tmux session and the conversation. Stop tracking leaves it running.`,
                    )
                  )
                    end(true);
                }}
              >
                Kill
              </button>
            </>
          ) : (
            <button
              className="b no"
              onClick={() => {
                if (
                  confirm(
                    `End "${s.name}"? The tmux session is killed; the record and its handoffs stay.`,
                  )
                )
                  end(true);
              }}
            >
              End
            </button>
          )}
          {!archived && !ended && (
            <button
              className="b no"
              onClick={() => {
                if (
                  confirm(
                    `Stop "${s.name}" and move its record to Archive? This ends its terminal process. Captured output, saved conversations and worktree files are retained.`,
                  )
                )
                  void action(
                    "archive",
                    "POST",
                    { stop: true },
                    "Session stopped and archived.",
                  );
              }}
            >
              Stop and archive
            </button>
          )}
          <label className="menu-field">
            Project
            <select
              className="f sess-proj"
              value={s.project_id || ""}
              onChange={(event) =>
                void run(`/sessions/${s.id}`, "PATCH", {
                  project_id: event.target.value
                    ? Number(event.target.value)
                    : null,
                })
              }
            >
              <option value="">— unassigned —</option>
              {projects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </select>
          </label>
        </ActionMenu>
      </div>
      {/* Below the actions, not above them: on a card with no worktree the
          actions menu is the first disclosure, and the browser suite opens it
          that way. */}
      <SessionMemory api={api} sessionId={s.id} projectId={s.project_id ?? null} />
    </article>
  );
}
