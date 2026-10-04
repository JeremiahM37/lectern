import { SwipeRow } from "../mobile/SwipeRow";
import { usePhone } from "../mobile/usePhone";
import { sessionSwipes } from "../mobile/sessionSwipes";
import { ScratchReview } from "./ScratchReview";
import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import type {
  Approval,
  InteractiveWorkspace,
  NoticeAction,
  Project,
  SessionView,
  Target,
} from "../types";
import { ApiError, type RequestOptions } from "../api";
import { Discover, Handoff, NewSession } from "./SessionDialogs";
import { Conversation } from "./Conversation";
import { NativeHistory, NativeSearch } from "./SavedConversations";
import { Modal } from "./Modal";
import { WorkspaceExtension } from "./WorkspaceExtension";
import { SessionCard } from "./SessionCard";
import { SessionGroups, type GroupMode } from "./SessionGroups";
import { ScratchTerminals } from "./ScratchTerminals";
import { isScratchTerminal } from "./scratch";
import { RestorePanel } from "./RestorePanel";
import { NeedsHistory, reopenSession, type ReopenChoice } from "./restore";
import { QuickSwitch } from "./QuickSwitch";
import { NeedsYou, type PushPrompt } from "./NeedsYou";
import { NowStrip } from "./NowStrip";
import { QuotaChip } from "./QuotaChip";
import { GettingStarted } from "../shell/GettingStarted";
import { sessionState, STATE_RANK } from "./status";
import { t, useLocale } from "../i18n";
import { viewerUnavailable } from "../terminal/viewer";
import "./sessions.css";
export interface SessionsApi {
  sessions(options?: {
    archived?: boolean;
    all?: boolean;
    signal?: AbortSignal;
  }): Promise<SessionView[]>;
  request<T>(path: string, options?: RequestOptions): Promise<T>;
}
export interface SessionsProps {
  api: SessionsApi;
  projects: Project[];
  targets: Target[];
  onOpenTerminal(url: string, title: string): void;
  mediaCounts?: Record<number, number>;
  onMedia?(sessionID: number): void;
  onConversation?(session: SessionView): void;
  onReview(session: SessionView): void;
  onMergeReview?(session: SessionView): void;
  onSwitch?(session: SessionView): void;
  onOpenTask?(id: number): void;
  onNotice(message: string, error?: boolean, action?: NoticeAction): void;
  refreshVersion?: number;
  action?: { kind: "new" | "discover"; version: number; projectId?: number };
  onActionConsumed?: () => void;
  onMetadataRefresh?: () => void;
  pushPrompt?: PushPrompt;
  /** Terminal tabs already open: a way back to them from Sessions. */
  openTerminals?: { count: number; open(): void };
}
function savedGrouping(): GroupMode {
  try {
    const value = sessionStorage.getItem("lec-session-grouping");
    if (value && ["none", "group", "project", "target"].includes(value))
      return value as GroupMode;
  } catch {}
  return "none";
}
function savedCollapsed() {
  try {
    const value: unknown = JSON.parse(
      sessionStorage.getItem("lec-collapsed-session-groups") || "[]",
    );
    return new Set(
      Array.isArray(value)
        ? value.filter((key): key is string => typeof key === "string")
        : [],
    );
  } catch {
    return new Set<string>();
  }
}
export function Sessions({
  api,
  projects,
  targets,
  onOpenTerminal,
  onConversation,
  onReview,
  onMergeReview,
  onSwitch,
  onOpenTask,
  onNotice,
  refreshVersion = 0,
  mediaCounts = {},
  onMedia = () => {},
  action: externalAction,
  onActionConsumed = () => {},
  onMetadataRefresh,
  pushPrompt,
  openTerminals,
}: SessionsProps) {
  useLocale();
  const [rows, setRows] = useState<SessionView[]>([]),
    [scope, setScope] = useState<"active" | "all" | "archived">("active"),
    [query, setQuery] = useState(""),
    phone = usePhone(),
    [group, setGroup] = useState<GroupMode>(savedGrouping),
    [scratchShown, setScratchShown] = useState(false),
    [collapsed, setCollapsed] = useState(savedCollapsed),
    [sheet, setSheet] = useState<"new" | "discover" | SessionView>(),
    // The project "Start an agent" opens on when asked for one (the
    // `lectern up` landing link names the folder it ran in).
    [startProject, setStartProject] = useState<number>(),
    [conversation, setConversation] = useState<SessionView>(),
    [history, setHistory] = useState<SessionView>(),
    [workspaceSession, setWorkspaceSession] = useState<SessionView>(),
    [groupSession, setGroupSession] = useState<SessionView>(),
    [archiveText, setArchiveText] = useState<{
      name: string;
      text: string;
      note: string;
    }>(),
    [search, setSearch] = useState(false),
    [recentOpen, setRecentOpen] = useState(false),
    [restoreElsewhere, setRestoreElsewhere] = useState<SessionView>(),
    [restoringAll, setRestoringAll] = useState(false),
    [relaunched, setRelaunched] = useState<SessionView[]>([]),
    [errors, setErrors] = useState<Record<number, string>>({}),
    [clock, setClock] = useState(Date.now()),
    // Pending session-scoped approvals (docs/agent-events.md section 3),
    // polled separately from NeedsYou's own identical poll: two small
    // requests to the same cheap endpoint is simpler and safer than
    // threading a shared cache through both, and each stays independently
    // correct if the other is ever removed.
    [approvals, setApprovals] = useState<Approval[]>([]);
  const approvalBySession = useMemo(() => {
    const map = new Map<number, Approval>();
    for (const a of approvals) if (a.session_id) map.set(a.session_id, a);
    return map;
  }, [approvals]);
  const generation = useRef(0),
    rowsRef = useRef(rows),
    updated = useRef(Date.now()),
    currentScope = useRef(scope);
  rowsRef.current = rows;
  currentScope.current = scope;
  async function load(signal?: AbortSignal) {
    const version = ++generation.current;
    const next = await api.sessions({
      archived: currentScope.current === "archived",
      all: currentScope.current === "all",
      signal,
    });
    if (signal?.aborted || version !== generation.current) return;
    updated.current = Date.now();
    setRows(next);
  }
  useEffect(() => {
    const abort = new AbortController();
    void load(abort.signal).catch((error) => {
      if (!abort.signal.aborted) onNotice(String(error), true);
    });
    return () => abort.abort();
  }, [scope, refreshVersion]);
  useEffect(() => {
    const abort = new AbortController();
    const loadApprovals = async () => {
      if (document.hidden) return;
      try {
        const rows = await api.request<Approval[]>("/approvals?status=pending", {
          signal: abort.signal,
        });
        if (!abort.signal.aborted && Array.isArray(rows)) setApprovals(rows);
      } catch {
        // A failed poll leaves the last-known approvals in place — same
        // "never invent an all-clear" rule NeedsYou follows for the same
        // endpoint.
      }
    };
    void loadApprovals();
    const timer = window.setInterval(() => void loadApprovals(), 15000);
    const visible = () => {
      if (!document.hidden) void loadApprovals();
    };
    document.addEventListener("visibilitychange", visible);
    return () => {
      abort.abort();
      clearInterval(timer);
      document.removeEventListener("visibilitychange", visible);
    };
  }, [api, refreshVersion]);
  useEffect(() => {
    if (externalAction?.version) {
      setStartProject(externalAction.projectId);
      setSheet(externalAction.kind);
      onActionConsumed();
    }
  }, [externalAction?.version]);
  useEffect(() => {
    const abort = new AbortController();
    let busy = false;
    const timer = window.setInterval(async () => {
      if (
        document.hidden ||
        busy ||
        !rowsRef.current.some((session) => session.setup_state === "creating")
      )
        return;
      busy = true;
      try {
        await load(abort.signal);
        await Promise.all(
          rowsRef.current
            .filter(
              (session) =>
                session.setup_state === "creating" &&
                session.workspace?.repositories?.length,
            )
            .map(async (session) => {
              try {
                const workspace = await api.request<InteractiveWorkspace>(
                  `/sessions/${session.id}/worktree`,
                  { signal: abort.signal },
                );
                if (!abort.signal.aborted)
                  setRows((old) =>
                    old.map((row) =>
                      row.id === session.id &&
                      row.setup_state === "creating" &&
                      row.workspace?.path === session.workspace?.path
                        ? { ...row, workspace }
                        : row,
                    ),
                  );
                setErrors((old) => ({ ...old, [session.id]: "" }));
              } catch (error) {
                if (!abort.signal.aborted)
                  setErrors((old) => ({ ...old, [session.id]: String(error) }));
              }
            }),
        );
      } catch (error) {
        if (!abort.signal.aborted) onNotice(String(error), true);
      } finally {
        busy = false;
      }
    }, 4000);
    const tick = window.setInterval(() => {
      if (!document.hidden) setClock(Date.now());
    }, 5000);
    return () => {
      abort.abort();
      clearInterval(timer);
      clearInterval(tick);
    };
  }, [api]);
  const shown = useMemo(() => {
    const terms = query.toLocaleLowerCase().trim().split(/\s+/);
    return rows
      .filter(
        (session) =>
          (scope !== "active" ||
            session.status !== "dead" ||
            session.setup_state === "failed") &&
          terms.every((word) =>
            [
              session.name,
              session.project_name,
              session.target_name,
              session.agent,
              session.group_path,
              session.workdir,
              session.workspace?.branch,
            ]
              .join(" ")
              .toLocaleLowerCase()
              .includes(word),
          ),
      )
      .sort(
        (a, b) =>
          STATE_RANK[sessionState(a, approvalBySession.has(a.id) || undefined).state] -
            STATE_RANK[sessionState(b, approvalBySession.has(b.id) || undefined).state] ||
          a.idle_seconds - b.idle_seconds,
      );
  }, [rows, scope, query, approvalBySession]);
  // Blank shells and AI sessions share one dashboard but not one list. This is
  // presentation only: the same tracked rows, split so neither buries the other.
  const { regular, scratch } = useMemo(() => {
    const regular: SessionView[] = [],
      scratch: SessionView[] = [];
    for (const session of shown)
      (isScratchTerminal(session) ? scratch : regular).push(session);
    return { regular, scratch };
  }, [shown]);
  const toggleGroup = (key: string, open: boolean) =>
    setCollapsed((old) => {
      const next = new Set(old);
      if (open) next.delete(key);
      else next.add(key);
      sessionStorage.setItem(
        "lec-collapsed-session-groups",
        JSON.stringify([...next]),
      );
      return next;
    });
  async function attach(session: SessionView, waitForSetup = false) {
    if (waitForSetup && session.setup_state === "creating") {
      // Resume returns before the successor's launch/setup worker has marked
      // the row ready. Wait for that durable state before asking ttyd to attach.
      for (let attempt = 0; attempt < 40; attempt += 1) {
        await new Promise((resolve) => window.setTimeout(resolve, 250));
        try {
          session = await api.request<SessionView>(
            `/sessions/${session.id}`,
          );
        } catch (error) {
          onNotice(String(error), true);
          return;
        }
        if (session.setup_state !== "creating") break;
      }
    }
    if (session.setup_state === "failed") {
      onNotice(
        session.setup_error ||
          t("sessions.list.setupFailed"),
        true,
      );
      return;
    }
    if (session.setup_state === "creating") {
      onNotice(
        t("sessions.list.settingUp"),
      );
      return;
    }
    let lastError: unknown;
    // A resumed launch is returned as `starting`; its tmux socket can take a
    // moment to appear after the API has committed the new row. Retry only the
    // proxy's transient 503 while keeping permanent errors visible.
    for (let attempt = 0; attempt < 20; attempt += 1) {
      try {
        const result = await api.request<{ url: string; notice?: string }>(
          `/sessions/${session.id}/terminal`,
          { method: "POST" },
        );
        onOpenTerminal(result.url, session.name);
        if (result.notice) onNotice(result.notice);
        return;
      } catch (error) {
        const unavailable = viewerUnavailable(error);
        if (unavailable) {
          onNotice(unavailable, true);
          return;
        }
        lastError = error;
        if (!(error instanceof ApiError) || error.status !== 503 || attempt === 19)
          break;
        await new Promise((resolve) => window.setTimeout(resolve, 250));
      }
    }
    onNotice(t("sessions.list.attachManually", { error: String(lastError) }), true);
    prompt(t("sessions.list.attachWith"), `tmux attach -t ${session.tmux_session}`);
  }
  const refreshAll = async () => {
    await load();
    onMetadataRefresh?.();
  };
  // A needs-you row for a session that cannot be chatted with (failed setup,
  // gone terminal) points at its card, where recovery lives. Opening the
  // worktree disclosure is how the setup error is already read.
  function showSession(session: SessionView, retried?: boolean) {
    const node = document.querySelector<HTMLElement>(
      `[data-session-id="${session.id}"]`,
    );
    if (!node) {
      // The Now strip shows every non-archived session, including ones the
      // current "Active sessions" scope filters out of the list below (a
      // finished-but-not-yet-archived "done" chip, chiefly) — widen scope
      // once so the tap actually has somewhere to land, then give up rather
      // than polling forever for a row that will never render.
      if (retried) return;
      if (scope === "active" && session.status === "dead") setScope("all");
      requestAnimationFrame(() =>
        requestAnimationFrame(() => showSession(session, true)),
      );
      return;
    }
    node.scrollIntoView();
    const worktree = node.querySelector<HTMLDetailsElement>(
      "details.session-worktree",
    );
    if (worktree) worktree.open = true;
  }
  function showApprovals() {
    document.getElementById("needs-you")?.scrollIntoView();
  }
  // reopen is the single restore path: Restore rows, Undo toasts, the
  // interrupted banner and the "Other agent…" picker all end here. The server
  // decides what reopening means for the record; a record without a bound
  // conversation opens the history picker instead.
  async function reopen(
    session: SessionView,
    choice: ReopenChoice = {},
    options: { attach?: boolean; quiet?: boolean } = {},
  ) {
    try {
      const result = await reopenSession(api, session.id, choice);
      setRecentOpen(false);
      await refreshAll();
      if (options.attach !== false && result.session.agent !== "shell")
        await attach(result.session, true);
      if (!options.quiet) onNotice(result.message);
      return true;
    } catch (error) {
      if (error instanceof NeedsHistory) {
        onNotice(`${error.message}.`);
        setHistory(session);
      } else onNotice(String(error), true);
      return false;
    }
  }
  // Offered as a toast right after a card closes a session. Undo reopens the
  // same record through the same path, without taking over the current view.
  function closed(session: SessionView, text: string) {
    onNotice(text, false, {
      label: t("sessions.list.undo"),
      run: () => void reopen(session, {}, { attach: false }),
    });
  }
  // Sessions restart recovery brought back on its own, until dismissed.
  useEffect(() => {
    const abort = new AbortController();
    void api
      .request<SessionView[]>("/sessions/relaunched", { signal: abort.signal })
      .then((next) => {
        if (!abort.signal.aborted && Array.isArray(next)) setRelaunched(next);
      })
      .catch(() => {});
    return () => abort.abort();
  }, [api, refreshVersion]);
  async function dismissRelaunched() {
    setRelaunched([]);
    try {
      await api.request("/sessions/relaunched/dismiss", { method: "POST", body: {} });
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  async function revive(session: SessionView) {
    try {
      const next = await api.request<SessionView>(`/sessions/${session.id}/revive`, {
        method: "POST",
        body: {},
      });
      await refreshAll();
      onNotice(t("sessions.list.revived", { name: next.name || session.name }));
      await attach(next, true);
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  // "Try a demo agent": the scripted stand-in (internal/sessions/demo.go) in
  // a new empty folder, opened in Chat, for someone with no agent installed.
  async function startDemo() {
    try {
      const session = await api.request<SessionView>("/sessions", {
        method: "POST",
        body: { agent: "demo", scratch: true, name: t("start.demoName") },
      });
      await refreshAll();
      onConversation?.(session);
      setConversation(session);
      onNotice(t("start.demoStarted"));
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  const interrupted = rows.filter(
    (session) => session.status === "interrupted" && !session.ended_at,
  );
  async function restoreInterrupted() {
    setRestoringAll(true);
    let restored = 0;
    for (const session of interrupted)
      if (await reopen(session, {}, { attach: false, quiet: true })) restored++;
    setRestoringAll(false);
    onNotice(
      t("sessions.list.restoredOf", { restored, count: interrupted.length }),
      restored < interrupted.length,
    );
  }
  function render(session: SessionView) {
    const elapsed = Math.max(0, (clock - updated.current) / 1000),
      display = {
        ...session,
        idle_seconds: session.idle_seconds + elapsed,
        uptime_seconds: session.uptime_seconds + elapsed,
      };
    const swipes = sessionSwipes(session, approvalBySession.get(session.id), {
      decide: (id) => api.request(`/approvals/${id}/decision`, { method: "POST", body: { decision: "approved" } }),
      request: (path, init) => api.request(path, init),
      closed,
      refresh: refreshAll,
      notice: onNotice,
      confirm: (text) => confirm(text),
    });
    return (
      <SwipeRow key={session.id} id={String(session.id)} left={swipes.left} right={swipes.right}>
      <SessionCard
        key={session.id}
        session={display}
        approval={approvalBySession.get(session.id)}
        projects={projects}
        api={api}
        progressError={errors[session.id]}
        mediaCount={mediaCounts[session.id] || 0}
        onMedia={onMedia}
        onRefresh={refreshAll}
        onNotice={onNotice}
        onAttach={(session) => void attach(session)}
        onChat={(session) => {
          onConversation?.(session);
          setConversation(session);
        }}
        onReview={onReview}
        onMergeReview={onMergeReview}
        onHandoff={setSheet}
        onSwitch={onSwitch}
        onGroup={setGroupSession}
        onHistory={setHistory}
        onWorkspace={setWorkspaceSession}
        onRestore={(session) => void reopen(session)}
        onRevive={(session) => void revive(session)}
        onClosed={closed}
        onArchive={(session) => {
          void api
            .request<{ text: string; note: string }>(
              `/sessions/${session.id}/archive/history`,
            )
            .then((value) => setArchiveText({ name: session.name, ...value }))
            .catch((error) => onNotice(String(error), true));
        }}
        onDiscover={() => setSheet("discover")}
      />
      </SwipeRow>
    );
  }
  // Nothing live yet (the first run, or everything ended): the Start an agent
  // card leads, without an empty "Scratch terminals" section under it.
  const firstRun = scope === "active" && !query && rows.length === 0;
  // With only a couple of sessions there is nothing to search, group or
  // filter (re-audit N8): those tools wait under ⋯ until the list grows.
  const activeCount = rows.filter((row) => !row.ended_at && row.status !== "dead").length;
  const few = scope === "active" && !query && group === "none" && activeCount <= 2;
  // A phone keeps one row of header: Start an agent and ⋯. Everything else
  // the desk shows there (saved search, discovery, restore, grouping) waits
  // under ⋯, and the search field appears only once the list is long enough
  // to need it.
  const compact = few || phone;
  // Typing in ⋯'s search brings the field out under the header, still
  // focused, and closes the menu, so the results it filters are not under it.
  const showSearch = phone ? activeCount > 4 || !!query : !few;
  const headerExtras = (
    <>
        <button
        className="b"
        id="sess-saved-search"
        onClick={() => setSearch(true)}
        aria-label={t("sessions.list.searchSavedLabel")}
      >
        {t("sessions.list.searchSaved")}<span className="wide-only">{t("sessions.list.searchSavedWide")}</span>
      </button>
        <button
        className="b"
        id="sess-discover"
        onClick={() => setSheet("discover")}
        aria-label={t("sessions.list.findAgentsLabel")}
      >
        {t("sessions.list.findAgentsFind")}<span className="wide-only">{t("sessions.list.findAgentsWide")}</span>{t("sessions.list.findAgentsEnd")}
      </button>
    </>
  );
  const searchField = (
      <input
        id="sess-search"
        className="f"
        type="search"
        placeholder={t(phone ? "sessions.list.searchPlaceholderShort" : "sessions.list.searchPlaceholder")}
        aria-label={t("sessions.list.searchLabel")}
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
  );
  const listFilters = (
      <div className="session-filters">
      <label className="session-grouping">
        {t("sessions.list.groupBy")}{" "}
        <select
          className="f"
          id="sess-grouping"
          aria-label={t("sessions.list.groupByLabel")}
          value={group}
          onChange={(event) => {
            const value = event.target.value as GroupMode;
            setGroup(value);
            sessionStorage.setItem("lec-session-grouping", value);
            if (phone) event.currentTarget.closest("details")?.removeAttribute("open");
          }}
        >
          <option value="none">{t("sessions.list.groupNone")}</option>
          <option value="group">{t("sessions.list.groupNamed")}</option>
          <option value="project">{t("sessions.list.groupProject")}</option>
          <option value="target">{t("sessions.list.groupTarget")}</option>
        </select>
      </label>
      <label className="session-grouping session-scope">
        {t("sessions.list.show")}{" "}
        <select
          className="f"
          id="sess-scope"
          value={scope}
          onChange={(event) => {
            setScope(event.target.value as typeof scope);
            if (phone) event.currentTarget.closest("details")?.removeAttribute("open");
          }}
        >
          <option value="active">{t("sessions.list.scopeActive")}</option>
          <option value="all">{t("sessions.list.scopeAll")}</option>
          <option value="archived">{t("sessions.list.scopeArchived")}</option>
        </select>
      </label>
      </div>
  );
  useEffect(() => {
    if (!phone || !query) return;
    document.getElementById("sess-more")?.removeAttribute("open");
    const field = document.getElementById("sess-search") as HTMLInputElement | null;
    field?.focus();
    field?.setSelectionRange(field.value.length, field.value.length);
  }, [phone, !!query]);
  const restoreButton = (
        <button
          className="b"
          id="sess-recent"
          aria-expanded={recentOpen}
          aria-label={t("sessions.list.restoreClosedLabel")}
          onClick={(event) => {
            setRecentOpen((open) => !open);
            event.currentTarget.closest("details")?.removeAttribute("open");
          }}
        >
          {t("sessions.list.restoreButton")}
        </button>
  );
  return (
    <section className={`list wide${firstRun ? " first-run" : ""}${few ? " few" : ""}`}>
      <div className="sesshead">
        <div>
          <h2>{t("sessions.list.title")}</h2>
          <p>
            {t("sessions.list.activeSummary", {
              active: rows.filter(
                (session) => !session.ended_at && session.status !== "dead",
              ).length,
            })}
          </p>
        </div>
        <QuotaChip api={api} />
        {!compact && headerExtras}
        {!phone && restoreButton}
        {openTerminals && (
          <button className="b" id="sess-terminals" onClick={openTerminals.open}>
            {t("sessions.list.openTerminals", { n: openTerminals.count })}
          </button>
        )}
        <button className="b ok" id="sess-new" onClick={() => setSheet("new")}>
          {t("sessions.list.newSession")}
        </button>
        {compact && (
          <details className="action-menu sess-more" id="sess-more">
            <summary aria-label={t("sessions.list.moreLabel")}>⋯</summary>
            <div className="action-menu-panel">
              {headerExtras}
              {phone && restoreButton}
              {!showSearch && searchField}
              {listFilters}
              <button
                className="b"
                id="sess-scratch"
                onClick={(event) => {
                  setScratchShown(true);
                  event.currentTarget.closest("details")?.removeAttribute("open");
                }}
              >
                {t("sessions.scratchReview.summary")}
              </button>
            </div>
          </details>
        )}
      </div>
      {relaunched.length > 0 && (
        <div className="restore-banner relaunch-notice" role="status">
          <span>
            {t("sessions.list.relaunched", { count: relaunched.length })}{" "}
            {relaunched.map((session, index) => (
              <Fragment key={session.id}>
                {index > 0 && ", "}
                <button className="linkish" onClick={() => showSession(session)}>
                  {session.name || `#${session.id}`}
                </button>
              </Fragment>
            ))}
            .
          </span>
          <button className="b" id="relaunch-dismiss" onClick={() => void dismissRelaunched()}>
            {t("sessions.list.dismiss")}
          </button>
        </div>
      )}
      {interrupted.length > 0 && (
        <div className="restore-banner" role="status">
          <span>
            {t("sessions.list.interrupted", { count: interrupted.length })}
          </span>
          <button
            className="b ok"
            id="restore-interrupted"
            disabled={restoringAll}
            onClick={() => void restoreInterrupted()}
          >
            {restoringAll
              ? t("sessions.list.restoring")
              : interrupted.length === 1
                ? t("sessions.list.restoreIt")
                : t("sessions.list.restoreCount", { count: interrupted.length })}
          </button>
        </div>
      )}
      <NowStrip
        rows={rows}
        approvalsCount={approvals.length}
        onShowSession={showSession}
        onShowApprovals={showApprovals}
      />
      {showSearch && searchField}
      {!compact && listFilters}
      {recentOpen && (
        <RestorePanel
          api={api}
          onNotice={onNotice}
          refreshVersion={refreshVersion}
          onReopen={async (session) => {
            await reopen(session);
          }}
          onElsewhere={setRestoreElsewhere}
        />
      )}
      {restoreElsewhere && (
        <QuickSwitch
          api={api}
          session={restoreElsewhere}
          mode="restore"
          onClose={() => setRestoreElsewhere(undefined)}
          onChoose={async (request) => {
            const source = restoreElsewhere;
            const ok = await reopen(source, {
              agent: request.agent,
              model: request.model,
              profile_id: request.profile,
            });
            if (!ok) throw new Error(t("sessions.list.restoreFailed"));
          }}
        />
      )}
      {/* One id wraps both sections: nothing that already points at #sesslist
          breaks, while each list is labelled on its own. */}
      <div id="sesslist">
        <section
          className="session-section"
          id="regular-sessions"
          aria-labelledby="regular-sessions-title"
        >
          <h3 className="session-section-title" id="regular-sessions-title">
            {t("sessions.list.sectionTitle")}
          </h3>
          {/* #sesslist is a responsive card grid; each section holds its own
              grid so the cards keep the same columns they had before. */}
          <div className="session-grid">
            {regular.length ? (
              <SessionGroups
                items={regular}
                mode={group}
                query={query.trim()}
                collapsed={collapsed}
                onToggle={toggleGroup}
                render={render}
              />
            ) : (
              query || scope !== "active" ? (
                <div className="hint">
                  {query ? t("sessions.list.noMatch") : scope === "archived" ? t("sessions.list.noArchived") : t("sessions.list.empty")}
                </div>
              ) : (
                <GettingStarted
                  request={api.request}
                  onStart={() => setSheet("new")}
                  onDemo={startDemo}
                  onFind={() => setSheet("discover")}
                />
              )
            )}
          </div>
        </section>
        {(!compact || scratch.length > 0) && (
        <ScratchTerminals
          items={scratch}
          mode={group}
          query={query.trim()}
          collapsed={collapsed}
          onToggle={toggleGroup}
          render={render}
        />
        )}
      </div>
      {/* Below the sessions, not above them: a session that wants you is
          marked on its own card, and what is left here — approvals, failed
          tasks, the review queue — should not push the sessions down. */}
      <NeedsYou
        api={api}
        rows={rows}
        refreshVersion={refreshVersion}
        onChat={(session) => {
          onConversation?.(session);
          setConversation(session);
        }}
        onAttach={(session) => void attach(session)}
        onReview={onReview}
        onShowSession={showSession}
        onOpenTask={onOpenTask}
        onChanged={() => void load()}
        onNotice={onNotice}
        pushPrompt={pushPrompt}
      />
      {sheet === "new" && (
        <NewSession
          api={api}
          projects={projects}
          targets={targets}
          initialProject={startProject}
          onClose={() => {
            setSheet(undefined);
            setStartProject(undefined);
          }}
          onCreated={() => void refreshAll()}
          onNotice={onNotice}
        />
      )}{" "}
      {sheet === "discover" && (
        <Discover
          api={api}
          projects={projects}
          onClose={() => setSheet(undefined)}
          onCreated={() => void refreshAll()}
          onNotice={onNotice}
        />
      )}{" "}
      {typeof sheet === "object" && (
        <Handoff
          api={api}
          session={sheet}
          onClose={() => setSheet(undefined)}
          onCreated={() => void load()}
          onNotice={onNotice}
        />
      )}{" "}
      {groupSession && (
        <GroupEditor
          api={api}
          session={groupSession}
          groups={rows.map((row) => row.group_path).filter(Boolean)}
          onClose={() => setGroupSession(undefined)}
          onSaved={() => void load()}
        />
      )}{" "}
      {conversation && (
        <Conversation
          key={`session-${conversation.id}`}
          kind="session"
          id={conversation.id}
          name={rows.find((row) => row.id === conversation.id)?.name || conversation.name}
          session={rows.find(row => row.id === conversation.id) || conversation}
          onOpenSession={setConversation}
          api={api}
          onClose={() => setConversation(undefined)}
          onNotice={onNotice}
          onAttach={() => void attach(conversation)}
          onSwitch={onSwitch && conversation.agent!=='shell' && !conversation.ended_at && conversation.status!=='dead' ? ()=>{setConversation(undefined);onSwitch(conversation);} : undefined}
        />
      )}{" "}
      {workspaceSession && (
        <WorkspaceExtension
          api={api}
          session={workspaceSession}
          onClose={() => setWorkspaceSession(undefined)}
          onChanged={() => void load()}
          onNotice={onNotice}
        />
      )}{" "}
      {history && (
        <NativeHistory
          api={api}
          session={history}
          onClose={() => setHistory(undefined)}
          onSession={(session, action) => {
            setHistory(undefined);
            void load();
            if (session.setup_state === "creating")
              onNotice(
                t("sessions.list.forkSetupStarted"),
              );
            else if (action === "resume") void attach(session);
            else onNotice(t("sessions.list.forkStarted"));
          }}
          onNotice={onNotice}
        />
      )}{" "}
      {search && (
        <NativeSearch
          api={api}
          targets={targets}
          onClose={() => setSearch(false)}
          onFork={(session) => {
            setSearch(false);
            void load();
            if (session.setup_state === "creating")
              onNotice(
                t("sessions.list.forkSetupStarted"),
              );
            else void attach(session);
          }}
          onNotice={onNotice}
        />
      )}{" "}
      {archiveText && (
        <Modal
          className="sheet archive-output"
          aria-label={t("sessions.list.archivedOutput")}
          onCancel={() => setArchiveText(undefined)}
        >
          <h2>{archiveText.name}</h2>
          <button
            className="b"
            aria-label={t("sessions.list.closeArchivedOutput")} data-close
            onClick={() => setArchiveText(undefined)}
          >
            {t("sessions.list.close")}
          </button>
          <p>{archiveText.note}</p>
          <pre>{archiveText.text || t("sessions.list.noOutput")}</pre>
        </Modal>
      )}
      {(!few || scratchShown) && <ScratchReview api={api} onNotice={onNotice} startOpen={few} />}
    </section>
  );
}
function GroupEditor({
  api,
  session,
  groups,
  onClose,
  onSaved,
}: {
  api: SessionsApi;
  session: SessionView;
  groups: string[];
  onClose: () => void;
  onSaved: () => void;
}) {
  useLocale();
  const [path, setPath] = useState(session.group_path || ""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Modal
      id="sheet"
      className="sheet"
      aria-label={t("sessions.groupEditor.title")}
      onCancel={onClose}
    >
      <div className="sheet-head">
        <h2>{t("sessions.groupEditor.title")}</h2>
        <button className="x" aria-label={t("sessions.groupEditor.close")} data-close onClick={onClose}>
          ✕
        </button>
      </div>
      <p>{session.name}</p>
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          try {
            await api.request(`/sessions/${session.id}`, {
              method: "PATCH",
              body: { group_path: path },
            });
            onSaved();
            onClose();
          } catch (error) {
            setError(String(error));
          } finally {
            setBusy(false);
          }
        }}
      >
        <label className="f" htmlFor="sg-path">
          {t("sessions.groupEditor.path")}
        </label>
        <input
          className="f"
          id="sg-path"
          list="sg-existing"
          placeholder={t("sessions.groupEditor.pathPlaceholder")}
          value={path}
          onChange={(event) => setPath(event.target.value)}
          autoFocus
        />
        <datalist id="sg-existing">
          {[...new Set(groups)].sort().map((group) => (
            <option key={group} value={group} />
          ))}
        </datalist>
        <p className="subhint">
          {t("sessions.groupEditor.hint")}
        </p>
        <p id="sg-error" role="status">
          {error}
        </p>
        <button className="b ok" id="sg-save" disabled={busy}>
          {t("sessions.groupEditor.save")}
        </button>
      </form>
    </Modal>
  );
}
