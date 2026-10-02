import { OpenInEditor } from "../remote/OpenInEditor";
import { SessionLineage } from "../continuity/SessionLineage";
import { SessionMemory } from "./SessionMemory";
import { useState } from "react";
import type { Approval, InteractiveWorkspace, Project, SessionView } from "../types";
import type { SessionsApi } from "./Sessions";
import { ActionMenu } from "./ActionMenu";
import { CheckBadge } from "./CheckBadge";
import { CIChip } from "../review/CIChip";
import { ApprovalCard, decisionBody, type ApprovalDecisionOptions } from "./ApprovalCard";
import { StatusBadge } from "./StatusBadge";
import { sessionState } from "./status";
import {
  isScratchTerminal,
  scratchDefaultName,
  scratchTitle,
} from "./scratch";
import { CompactionWarning, ContextBadge, CostBadge, LinesBadge } from "./UsageBadges";
import { AwarenessOverlapChip } from "./AwarenessOverlapChip";
import { LimitBanner } from "../limits/LimitBanner";
import { t, useLocale } from "../i18n";
export function duration(seconds: number) {
  seconds = Math.max(0, Math.floor(seconds || 0));
  return seconds < 60
    ? t("sessions.card.duration.seconds", { s: seconds })
    : seconds < 3600
      ? t("sessions.card.duration.minutes", { m: Math.floor(seconds / 60) })
      : seconds < 86400
        ? t("sessions.card.duration.hoursMinutes", { h: Math.floor(seconds / 3600), m: Math.floor((seconds % 3600) / 60) })
        : t("sessions.card.duration.daysHours", { d: Math.floor(seconds / 86400), h: Math.floor((seconds % 86400) / 3600) });
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
  // onRestore reopens a session a restart interrupted; onClosed offers Undo
  // after this card ends, archives or stops tracking one. Both optional.
  onRestore?: (session: SessionView) => void;
  // onRevive restarts an agent that exited and left its terminal at a shell.
  onRevive?: (session: SessionView) => void;
  onClosed?: (session: SessionView, text: string) => void;
  // The one pending approval for this session's PermissionRequest hold, if
  // any (docs/agent-events.md section 3). Optional so existing call sites
  // and tests that predate the feature keep compiling.
  approval?: Approval;
}
export function SessionCard({
  session: s,
  approval,
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
  onRestore,
  onRevive,
  onClosed,
}: Props) {
  useLocale();
  const [progress, setProgress] = useState(""),
    [progressBusy, setProgressBusy] = useState(false);
  // Optimistic hide: `approval` is a prop from the parent's own poll (a
  // separate cadence from onRefresh), so a decision here would otherwise stay
  // visible until that poll's next tick catches up.
  const [resolvedApprovalID, setResolvedApprovalID] = useState<number | null>(null);
  const activeApproval = approval && approval.id !== resolvedApprovalID ? approval : undefined;
  async function decideApproval(decision: "approved" | "denied", opts?: ApprovalDecisionOptions) {
    if (!activeApproval) return;
    const id = activeApproval.id;
    try {
      await api.request(`/approvals/${id}/decision`, { method: "POST", body: decisionBody(decision, opts) });
      setResolvedApprovalID(id);
      await onRefresh();
    } catch (error) {
      onNotice(String(error), true);
    }
  }
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
    interrupted = !ended && s.status === "interrupted",
    agentExited = !ended && !!s.agent_exited_at,
    live = !ended && s.status !== "dead" && !interrupted && !setup && !failed,
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
      t("sessions.card.cancelRequested"),
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
      t("sessions.card.makeProjectPrompt", { path: s.workdir ?? "" }),
      suggested,
    );
    if (name !== null)
      void action(
        "promote",
        "POST",
        { name: name.trim(), wrap: !scratch },
        t("sessions.card.projectCreated"),
      );
  }
  // Ending, archiving and stopping tracking all offer Undo, which reopens
  // this record: its conversation, its tracking or its archive come back.
  async function close(
    path: string,
    method: string,
    body: { [key: string]: boolean } | undefined,
    text: string,
  ) {
    try {
      await api.request(path, { method, body });
      if (onClosed) onClosed(s, text);
      else onNotice(text);
      await onRefresh();
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  function end(kill: boolean) {
    if (s.status === "dead") {
      void run(`/sessions/${s.id}`, "DELETE");
      return;
    }
    const name = cardTitle || t("sessions.card.sessionFallback");
    void close(
      `/sessions/${s.id}${kill ? "?kill=true" : ""}`,
      "DELETE",
      undefined,
      adopted && !kill ? t("sessions.card.stoppedTracking", { name }) : t("sessions.card.ended", { name }),
    );
  }
  // Rename only the tracked label; the directory and process stay untouched.
  // In place: the title becomes a text box (Enter saves, Esc cancels).
  const [renaming, setRenaming] = useState(false),
    [nameDraft, setNameDraft] = useState("");
  function rename() {
    setNameDraft(cardTitle);
    setRenaming(true);
  }
  function saveName() {
    setRenaming(false);
    const next = nameDraft.trim();
    if (!next || next === cardTitle) return;
    void run(`/sessions/${s.id}`, "PATCH", { name: next }, t("sessions.card.renamed"));
  }
  // Run check and Memory moved into ⋯ (re-audit N8); Memory opens below.
  const [showMemory, setShowMemory] = useState(false);
  async function runCheck() {
    try {
      await api.request(`/sessions/${s.id}/checks`, { method: "POST" });
      onNotice(t("sessions.check.started"));
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  async function copyPath() {
    try {
      await navigator.clipboard.writeText(scratchPath);
      onNotice(t("sessions.card.pathCopied"));
    } catch {
      onNotice(t("sessions.card.copyFailed"), true);
    }
  }
  const preview = setup
    ? (s.setup_cancel_requested
        ? t("sessions.card.previewCancelling")
        : t("sessions.card.previewSettingUp")) +
      (workspace?.repositories || [])
        .map((repo) => `\n${repo.name}: ${repo.worktree.state}`)
        .join("") +
      (s.setup_error ? "\n" + s.setup_error : "") +
      (progressError ? "\n" + t("sessions.card.progressUnavailable", { error: progressError }) : "")
    : s.setup_error
      ? t("sessions.card.setupFailedPreview", { error: s.setup_error })
      : s.pane_tail || "";
  return (
    <article
      className={`scard s-${failed ? "failed" : s.status}`}
      data-session-id={s.id}
      data-state={sessionState(s, approval ? !!activeApproval : undefined).state}
      data-scratch-terminal={scratch ? "true" : undefined}
    >
      {/* A blank shell has no project, so its location line names the machine
          instead: the card's title is the folder, and this keeps "which host"
          readable without repeating the folder. */}
      <div className="scard-project">
        {scratch ? scratchDefaultName(s) : s.project_name || folderName(s.workdir) || t("sessions.card.unassigned")}
      </div>
      <div className="scard-top">
        <span className={`dot ${s.status === "running" ? "live" : ""}`} />
        {renaming ? (
          <input
            className="f nm-edit"
            autoFocus
            aria-label={scratch ? t("sessions.card.renameScratchPrompt") : t("sessions.card.renameSessionPrompt")}
            value={nameDraft}
            onChange={(e) => setNameDraft(e.target.value)}
            onBlur={saveName}
            onKeyDown={(e) => {
              if (e.key === "Enter") saveName();
              if (e.key === "Escape") {
                e.preventDefault();
                setRenaming(false);
              }
            }}
          />
        ) : (
          <button type="button" className="nm" title={t("sessions.card.renameHint")} onClick={rename}>
            {cardTitle}
          </button>
        )}
        <StatusBadge session={s} pendingApproval={approval ? !!activeApproval : undefined} />
        <span className="sidle">
          {s.status === "dead" || setup
            ? ""
            : t("sessions.card.quiet", { duration: duration(s.idle_seconds) })}
        </span>
      </div>
      {activeApproval && (
        <div className="scard-approval" data-approval-id={activeApproval.id}>
          <ApprovalCard compact approval={activeApproval} onDecide={decideApproval} />
        </div>
      )}
      {live && s.limit && (
        <LimitBanner
          hold={s.limit}
          api={api}
          onRefresh={onRefresh}
          onNotice={onNotice}
          onPickAgent={onSwitch ? () => onSwitch(s) : undefined}
        />
      )}
      {scratch && scratchPath && (
        <div className="scard-path">
          <code title={scratchPath}>{scratchPath}</code>
          <button className="b copy-path" onClick={() => void copyPath()}>
            {t("sessions.card.copyPath")}
          </button>
        </div>
      )}
      <div className="smeta">
        <span className="chip">
          {s.agent}
          {s.model ? " · " + s.model : ""}
        </span>
        <span className="chip tgt">{s.target_name}</span>
        {s.account && (
          <span className="chip account-chip" title={t("sessions.card.accountTitle")}>
            👤 {s.account}
          </span>
        )}
        <span className="chip">
          {setup ? t("sessions.card.setupFor", { duration: duration(s.uptime_seconds) }) : t("sessions.card.upFor", { duration: duration(s.uptime_seconds) })}
        </span>
        {mediaCount > 0 && (
          <button
            className="chip media-chip"
            title={t("sessions.card.mediaTitle")}
            onClick={() => onMedia(s.id)}
          >
            {t("sessions.card.mediaCount", { count: mediaCount })}
          </button>
        )}
        {s.launch_profile && (
          <span className="chip" title={t("sessions.card.launchProfileTitle")}>
            {s.launch_profile}
          </span>
        )}
        {s.isolation?.mode && (
          <span
            className="chip info"
            title={
              s.isolation.network === "deny"
                ? t("sessions.card.isolationDenied", { mode: s.isolation.mode })
                : t("sessions.card.isolationAllowed", { mode: s.isolation.mode })
            }
          >
            🔒 {s.isolation.mode}
            {s.isolation.network === "deny" ? " · net:deny" : ""}
          </span>
        )}
        {adopted && (
          <span
            className="chip info"
            title={t("sessions.card.adoptedTitle")}
          >
            {t("sessions.card.adopted")}
          </span>
        )}
        {!!s.wraps && <span className="chip info">⇥ {s.wraps}</span>}
        {s.handoff_in_flight && (
          <span className="chip warn">{t("sessions.card.writingHandoff")}</span>
        )}
        <ContextBadge session={s} />
        <CostBadge session={s} />
        <LinesBadge session={s} />
        <CompactionWarning session={s} />
        <AwarenessOverlapChip session={s} />
        {s.target_reach?.unreachable && (
          <span
            className="chip warn target-unreachable"
            title={t("sessions.card.unreachableTitle", { machine: s.target_name || t("sessions.card.thisMachine"), error: s.target_reach.error || t("sessions.card.noReply") })}
          >
            {t("sessions.card.unreachable", { machine: s.target_name || t("sessions.card.machine") })}
          </span>
        )}
        {s.group_path && <span className="chip">{s.group_path}</span>}
        {live && s.agent !== "shell" && (
          <CheckBadge session={s} api={api} onNotice={onNotice} hideRun />
        )}
        <CIChip ci={s.ci} />
      </div>
      <div className="spane">{preview}</div>
      {workspace && (
        <details className="session-worktree">
          <summary>
            {t(workspace.repositories?.length ? "sessions.card.workspaceSummary" : "sessions.card.worktreeSummary", { branch: workspace.branch, state: workspace.state })}
          </summary>
          <code>{workspace.path}</code>
          <small>
            {workspace.repositories?.length
              ? t("sessions.card.repositoryCount", { n: workspace.repositories.length })
              : t("sessions.card.base", { base: workspace.base, commit: workspace.commit?.slice(0, 12) || t("sessions.card.notCreated") })}
          </small>
          {workspace.error && <p>{t("sessions.card.setupError", { error: workspace.error })}</p>}
          {(
            workspace.repositories || [
              { name: s.project_name || t("sessions.card.repository"), worktree: workspace },
            ]
          ).map(
            (entry, index) =>
              entry.worktree.setup_command && (
                <pre className="workspace-setup-output" key={index}>
                  {t("sessions.card.repoSetup", { name: entry.name, state: entry.worktree.setup_state || t("sessions.card.notCompleted") })}
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
                      t("sessions.card.recordedState", { state: current.state }) + "\n" +
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
                {t("sessions.card.refreshProgress")}
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
              {t("sessions.card.settingUpButton")}
            </button>
            <button className="b no" onClick={() => void cancel()}>
              {s.setup_cancel_requested ? t("sessions.card.retryCancel") : t("sessions.card.cancelSetup")}
            </button>
          </>
        )}
        {agentExited && onRevive && (
          <button className="b ok grow revive-agent" onClick={() => onRevive(s)}>
            {t("sessions.card.revive")}
          </button>
        )}
        {interrupted && onRestore && (
          <button className="b ok grow restore-interrupted" onClick={() => onRestore(s)}>
            {t("sessions.card.restore")}
          </button>
        )}
        {live && (
          <>
            <button
              className={`b attach${chatReady ? "" : " grow"}`}
              onClick={() => onAttach(s)}
            >
              {t("sessions.card.attach")}
            </button>
            {chatReady && (
              <button className="b grow chat-open" onClick={() => onChat(s)}>
                {t("sessions.card.chat")}
              </button>
            )}
            {scratch && (
              <button className="b ok make-project" onClick={makeProject}>
                {t("sessions.card.makeProject")}
              </button>
            )}
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
                t("sessions.card.trackingRestored"),
              )
            }
          >
            {t("sessions.card.trackAgain")}
          </button>
        )}
        <ActionMenu name={s.name}>
          <button className="b rename" onClick={rename}>{t("sessions.card.rename")}</button>
          {live && (
            <>
              {onSwitch && s.agent !== "shell" && (
                <button className="b" disabled={s.handoff_in_flight} onClick={() => onSwitch(s)}>
                  {s.handoff_in_flight ? t("sessions.card.switching") : t("sessions.card.switch")}
                </button>
              )}
              {s.agent !== "shell" && (
                <button className="b" onClick={() => void runCheck()}>
                  {t("sessions.check.run")}
                </button>
              )}
              <button className="b" aria-expanded={showMemory} onClick={() => setShowMemory((open) => !open)}>
                {t("sessions.memory.summary")}
              </button>
              <button className="b" onClick={() => onReview(s)}>
                {t("sessions.card.reviewChanges")}
              </button>
              {onMergeReview && (
                <button className="b" onClick={() => onMergeReview(s)}>
                  {t("sessions.card.reviewMerge")}
                </button>
              )}
              <a className="b" href={`lectern://attach/session/${s.id}`}>
                {t("sessions.card.openInTerminal")}
              </a>
              <OpenInEditor targetId={s.target_id} path={s.workspace?.path || s.workdir} />
              {s.status === "running" && (
                <button
                  className="b warn"
                  onClick={() => void action("send", "POST", { key: "escape" })}
                >
                  {t("sessions.card.interrupt")}
                </button>
              )}
              <button className="b" onClick={() => onHandoff(s)}>
                {t("sessions.card.handoff")}
              </button>
              {!s.project_id && !scratch && (
                <button className="b ok" onClick={makeProject}>
                  {t("sessions.card.makeProject")}
                </button>
              )}
            </>
          )}
          {failed && workspace?.state !== "removed" && (
            <button className="b" onClick={() => void cancel()}>
              {t("sessions.card.cancelCheckout")}
            </button>
          )}
          <button className="b" onClick={() => onGroup(s)}>
            {t("sessions.card.moveToGroup")}
          </button>
          {(s.saved_conversations ?? ["claude", "codex"].includes(s.agent)) && (
            <button className="b" onClick={() => onHistory(s)}>
              {t("sessions.card.savedConversations")}
            </button>
          )}
          {workspace && (
            <>
              {!!workspace.repositories?.length &&
                !setup &&
                workspace.state !== "removed" && (
                  <button className="b" onClick={() => onWorkspace(s)}>
                    {t("sessions.card.workspaceRepositories")}
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
                        t("sessions.card.allocationValidated"),
                      )
                    }
                  >
                    {t("sessions.card.recoverAllocation")}
                  </button>
                )}
              {workspace.state !== "removed" && (
                <button
                  className="b"
                  onClick={() => {
                    if (
                      confirm(
                        t("sessions.card.removeWorktreeConfirm", { path: workspace.path }),
                      )
                    )
                      void action(
                        "worktree",
                        "DELETE",
                        undefined,
                        t("sessions.card.worktreeRemoved"),
                      );
                  }}
                >
                  {t("sessions.card.removeWorktree")}
                </button>
              )}
            </>
          )}
          {archived ? (
            <>
              <button className="b" onClick={() => onArchive(s)}>
                {t("sessions.card.archivedOutput")}
              </button>
              <button
                className="b"
                onClick={() =>
                  void action(
                    "archive",
                    "DELETE",
                    undefined,
                    t("sessions.card.unarchived"),
                  )
                }
              >
                {t("sessions.card.unarchive")}
              </button>
            </>
          ) : ended ? (
            <>
              <button
                className="b"
                onClick={() => void action("archive", "POST", { stop: false })}
              >
                {t("sessions.card.archiveStopped")}
              </button>
              {!s.can_restore && (
                <button className="b" onClick={onDiscover}>
                  {t("sessions.card.findRunning")}
                </button>
              )}
            </>
          ) : s.status === "dead" ? (
            <button className="b no" onClick={() => end(false)}>
              {t("sessions.card.dismiss")}
            </button>
          ) : setup ? null : adopted ? (
            <>
              <button className="b" onClick={() => end(false)}>
                {t("sessions.card.stopTracking")}
              </button>
              <button
                className="b no"
                onClick={() => {
                  if (
                    confirm(
                      t("sessions.card.killConfirm", { name: s.name }),
                    )
                  )
                    end(true);
                }}
              >
                {t("sessions.card.kill")}
              </button>
            </>
          ) : (
            // No confirmation: ending offers Undo, which reopens the session.
            <button className="b no" onClick={() => end(true)}>
              {t("sessions.card.end")}
            </button>
          )}
          {!archived && !ended && (
            <button
              className="b no"
              onClick={() => {
                if (
                  confirm(
                    t("sessions.card.stopArchiveConfirm", { name: s.name }),
                  )
                )
                  void close(
                    `/sessions/${s.id}/archive`,
                    "POST",
                    { stop: true },
                    t("sessions.card.stoppedArchived", { name: cardTitle || t("sessions.card.sessionFallback") }),
                  );
              }}
            >
              {t("sessions.card.stopArchive")}
            </button>
          )}
          <label className="menu-field">
            {t("sessions.card.project")}
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
              <option value="">{t("sessions.card.unassignedOption")}</option>
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
      {showMemory && <SessionMemory api={api} sessionId={s.id} projectId={s.project_id ?? null} open />}
    </article>
  );
}

/** The last path segment, for a session with no project: "~/notes" → "notes". */
function folderName(path?: string | null): string {
  return (path || "").split("/").filter(Boolean).pop() || "";
}
