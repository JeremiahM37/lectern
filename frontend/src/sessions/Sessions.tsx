import { ScratchReview } from "./ScratchReview";
import { useEffect, useMemo, useRef, useState } from "react";
import type {
  Approval,
  InteractiveWorkspace,
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
import { RecentlyClosed, type RecentSession } from "./RecentlyClosed";
import { NeedsYou } from "./NeedsYou";
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
  onNotice(message: string, error?: boolean): void;
  refreshVersion?: number;
  action?: { kind: "new" | "discover"; version: number };
  onActionConsumed?: () => void;
  onMetadataRefresh?: () => void;
}
const order: Record<string, number> = {
  waiting: 0,
  running: 1,
  starting: 2,
  idle: 3,
  dead: 4,
};
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
}: SessionsProps) {
  const [rows, setRows] = useState<SessionView[]>([]),
    [scope, setScope] = useState<"active" | "all" | "archived">("active"),
    [query, setQuery] = useState(""),
    [group, setGroup] = useState<GroupMode>(savedGrouping),
    [collapsed, setCollapsed] = useState(savedCollapsed),
    [sheet, setSheet] = useState<"new" | "discover" | SessionView>(),
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
          (order[a.status] ?? 9) - (order[b.status] ?? 9) ||
          a.idle_seconds - b.idle_seconds,
      );
  }, [rows, scope, query]);
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
          "Workspace setup failed. Inspect retained files before launching again.",
        true,
      );
      return;
    }
    if (session.setup_state === "creating") {
      onNotice(
        "Workspace is setting up. Attach becomes available when setup finishes.",
      );
      return;
    }
    let lastError: unknown;
    // A resumed launch is returned as `starting`; its tmux socket can take a
    // moment to appear after the API has committed the new row. Retry only the
    // proxy's transient 503 while keeping permanent errors visible.
    for (let attempt = 0; attempt < 20; attempt += 1) {
      try {
        const result = await api.request<{ url: string }>(
          `/sessions/${session.id}/terminal`,
          { method: "POST" },
        );
        onOpenTerminal(result.url, session.name);
        return;
      } catch (error) {
        lastError = error;
        if (!(error instanceof ApiError) || error.status !== 503 || attempt === 19)
          break;
        await new Promise((resolve) => window.setTimeout(resolve, 250));
      }
    }
    onNotice(String(lastError) + " — attach manually", true);
    prompt("Attach with:", `tmux attach -t ${session.tmux_session}`);
  }
  const refreshAll = async () => {
    await load();
    onMetadataRefresh?.();
  };
  // A needs-you row for a session that cannot be chatted with (failed setup,
  // gone terminal) points at its card, where recovery lives. Opening the
  // worktree disclosure is how the setup error is already read.
  function showSession(session: SessionView) {
    const node = document.querySelector<HTMLElement>(
      `[data-session-id="${session.id}"]`,
    );
    if (!node) return;
    node.scrollIntoView();
    const worktree = node.querySelector<HTMLDetailsElement>(
      "details.session-worktree",
    );
    if (worktree) worktree.open = true;
  }
  async function restoreRecent(session: RecentSession) {
    try {
      const restored = await api.request<SessionView>(
        `/sessions/${session.id}/restore`,
        { method: "POST", body: {} },
      );
      setRecentOpen(false);
      await refreshAll();
      await attach(restored, true);
      onNotice(`Tracking restored for ${restored.name || session.name}`);
    } catch (error) {
      onNotice(String(error), true);
    }
  }
  async function resumeRecent(session: RecentSession) {
    try {
      const resumed = await api.request<SessionView>(
        `/sessions/${session.id}/resume-recent`,
        { method: "POST", body: { name: session.name } },
      );
      setRecentOpen(false);
      await refreshAll();
      await attach(resumed, true);
      onNotice(`Resumed ${resumed.name || session.name}`);
    } catch (error) {
      const status =
        typeof error === "object" && error !== null && "status" in error
          ? error.status
          : undefined;
      if (status === 404 || status === 409) setHistory(session);
      else onNotice(String(error), true);
    }
  }
  function render(session: SessionView) {
    const elapsed = Math.max(0, (clock - updated.current) / 1000),
      display = {
        ...session,
        idle_seconds: session.idle_seconds + elapsed,
        uptime_seconds: session.uptime_seconds + elapsed,
      };
    return (
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
    );
  }
  return (
    <section className="list wide">
      <div className="sesshead">
        <div>
          <h2>Sessions</h2>
          <p>
            {
              rows.filter(
                (session) => !session.ended_at && session.status !== "dead",
              ).length
            }{" "}
            active · Pick up where you left off.
          </p>
        </div>
        <button
          className="b"
          id="sess-saved-search"
          onClick={() => setSearch(true)}
          aria-label="Search saved conversations"
        >
          Search saved<span className="wide-only"> conversations</span>
        </button>
        <button
          className="b"
          id="sess-discover"
          onClick={() => setSheet("discover")}
          aria-label="Find running agents"
        >
          ⌕ Find<span className="wide-only"> running</span> agents
        </button>
        <button
          className="b"
          id="sess-recent"
          aria-expanded={recentOpen}
          onClick={() => setRecentOpen((open) => !open)}
        >
          Recently closed
        </button>
        <button className="b ok" id="sess-new" onClick={() => setSheet("new")}>
          + New session
        </button>
      </div>
      <input
        id="sess-search"
        className="f"
        type="search"
        placeholder="Search sessions, groups, branches or folders"
        aria-label="Find a session or project"
        value={query}
        onChange={(event) => setQuery(event.target.value)}
      />
      <div className="session-filters">
      <label className="session-grouping">
        Group by{" "}
        <select
          className="f"
          id="sess-grouping"
          aria-label="Group sessions by"
          value={group}
          onChange={(event) => {
            const value = event.target.value as GroupMode;
            setGroup(value);
            sessionStorage.setItem("lec-session-grouping", value);
          }}
        >
          <option value="none">None</option>
          <option value="group">Named group</option>
          <option value="project">Project</option>
          <option value="target">Target</option>
        </select>
      </label>
      <label className="session-grouping session-scope">
        Show{" "}
        <select
          className="f"
          id="sess-scope"
          value={scope}
          onChange={(event) => setScope(event.target.value as typeof scope)}
        >
          <option value="active">Active sessions</option>
          <option value="all">Include ended and untracked</option>
          <option value="archived">Archived sessions</option>
        </select>
      </label>
      </div>
      {recentOpen && (
        <RecentlyClosed
          api={api}
          onNotice={onNotice}
          refreshVersion={refreshVersion}
          onRestore={restoreRecent}
          onResume={resumeRecent}
          onHistory={(session) => setHistory(session)}
        />
      )}
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
      />
      {/* One id wraps both sections: nothing that already points at #sesslist
          breaks, while each list is labelled on its own. */}
      <div id="sesslist">
        <section
          className="session-section"
          id="regular-sessions"
          aria-labelledby="regular-sessions-title"
        >
          <h3 className="session-section-title" id="regular-sessions-title">
            Sessions and projects
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
              <div className="hint">
                {query
                  ? "No sessions match your search."
                  : scope === "archived"
                    ? "No archived sessions. Use “Stop and archive” in a session’s actions to keep it here for later."
                    : "No sessions yet. Start one here, or hit Find running agents to adopt sessions already running in tmux."}
              </div>
            )}
          </div>
        </section>
        <ScratchTerminals
          items={scratch}
          mode={group}
          query={query.trim()}
          collapsed={collapsed}
          onToggle={toggleGroup}
          render={render}
        />
      </div>
      {sheet === "new" && (
        <NewSession
          api={api}
          projects={projects}
          onClose={() => setSheet(undefined)}
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
          name={conversation.name}
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
                "Fork workspace setup started. Follow progress in Sessions.",
              );
            else if (action === "resume") void attach(session);
            else onNotice("Fork started. The original session keeps running.");
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
                "Fork workspace setup started. Follow progress in Sessions.",
              );
            else void attach(session);
          }}
          onNotice={onNotice}
        />
      )}{" "}
      {archiveText && (
        <Modal
          className="sheet archive-output"
          aria-label="Archived terminal output"
          onCancel={() => setArchiveText(undefined)}
        >
          <h2>{archiveText.name}</h2>
          <button
            className="b"
            aria-label="Close archived output"
            onClick={() => setArchiveText(undefined)}
          >
            Close
          </button>
          <p>{archiveText.note}</p>
          <pre>{archiveText.text || "No terminal output was available."}</pre>
        </Modal>
      )}
      <ScratchReview api={api} onNotice={onNotice} />
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
  const [path, setPath] = useState(session.group_path || ""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  return (
    <Modal
      id="sheet"
      className="sheet"
      aria-label="Move to group"
      onCancel={onClose}
    >
      <div className="sheet-head">
        <h2>Move to group</h2>
        <button className="x" aria-label="Close group editor" onClick={onClose}>
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
          Group path
        </label>
        <input
          className="f"
          id="sg-path"
          list="sg-existing"
          placeholder="Work/Client"
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
          Use / for nested groups. Leave blank to ungroup.
        </p>
        <p id="sg-error" role="status">
          {error}
        </p>
        <button className="b ok" id="sg-save" disabled={busy}>
          Save group
        </button>
      </form>
    </Modal>
  );
}
