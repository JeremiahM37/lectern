import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { t, useLocale } from "../i18n";
import type { Project } from "../types";
import { errorText, type Notice, type TrackerApi } from "./api";
import { Avatar, ChecksBadge, Labels, ReviewBadge, StatePill } from "./bits";
import { IssuePage } from "./IssuePage";
import { ago, boardColumns, issuesLabel, itemMark, matches, parseTasksHash, sameRef, SOURCE_NAME, tasksHash } from "./logic";
import { MergeQueue } from "./MergeQueue";
import { PRPage } from "./PRPage";
import type { StartedWork } from "./StartWork";
import type { Item, ItemRef, Transition, TrackersResponse, WorkResponse, WorkSource } from "./types";
import "./trackers.css";

type Tab = "all" | "pr" | "issue" | "queue" | `c${number}`;
type Filter = "open" | "assigned" | "review" | "authored" | "closed" | "all";
type View = "list" | "board" | "table";

const FILTERS = (): { key: Filter; label: string; pr?: boolean }[] => [
  { key: "open", label: t("trackers.hub.filter.open") },
  { key: "assigned", label: t("trackers.hub.filter.assigned") },
  { key: "review", label: t("trackers.hub.filter.review"), pr: true },
  { key: "authored", label: t("trackers.hub.filter.authored") },
  { key: "closed", label: t("trackers.hub.filter.closed") },
  { key: "all", label: t("trackers.hub.filter.all") },
];

const VIEW_LABEL = (): Record<View, string> => ({
  list: t("trackers.hub.view.list"),
  board: t("trackers.hub.view.board"),
  table: t("trackers.hub.view.table"),
});

function filterQuery(f: Filter): string {
  switch (f) {
    case "assigned":
    case "review":
    case "authored":
      return `state=open&mine=${f}`;
    case "closed":
      return "state=closed";
    case "all":
      return "state=all";
  }
  return "state=open";
}

function store<T extends string>(key: string, fallback: T): T {
  try {
    return (localStorage.getItem(key) as T) || fallback;
  } catch {
    return fallback;
  }
}

function save(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {}
}

/** The Tasks hub: a project's pull requests and issues from its GitHub or
 * GitLab repository, plus its Linear and Jira issues, in one list (or board,
 * or table), with each item's page beside it on a desktop and over it on a
 * phone. Deep links: #issues/<project>/<source>/<pr|issue>/<id>[/<connection>]. */
export function TasksHub({
  api,
  projects,
  refreshVersion,
  onNotice,
  onOpenSession,
  onOpenTask,
  onSettings,
}: {
  api: TrackerApi;
  projects: Project[];
  refreshVersion?: number;
  onNotice: Notice;
  onOpenSession(id: number, name: string): void;
  onOpenTask(id: number): void;
  onSettings(projectId: number): void;
}) {
  useLocale();
  const initial = parseTasksHash(location.hash);
  const [projectId, setProjectId] = useState<number | undefined>(() => initial.project ?? (Number(store("lec-tasks-project", "0")) || undefined));
  const [selected, setSelected] = useState<ItemRef | undefined>(initial.ref);
  const [tab, setTab] = useState<Tab>(() => store<Tab>("lec-tasks-tab", "all"));
  const [filter, setFilter] = useState<Filter>(() => store<Filter>("lec-tasks-filter", "open"));
  const [view, setView] = useState<View>(() => store<View>("lec-tasks-view", "list"));
  const [text, setText] = useState("");
  const [query, setQuery] = useState("");
  const [trackers, setTrackers] = useState<TrackersResponse>();
  const [data, setData] = useState<WorkResponse>();
  const [states, setStates] = useState<Transition[]>([]);
  const [teams, setTeams] = useState<{ key: string; name: string }[]>([]);
  const [team, setTeam] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const generation = useRef(0);

  const project = projects.find((p) => p.id === projectId) || projects[0];
  const pid = project?.id;

  // follow #issues/... links, including ones the app navigates to later
  useEffect(() => {
    const apply = () => {
      const h = parseTasksHash(location.hash);
      if (h.project) setProjectId(h.project);
      setSelected(h.ref);
    };
    window.addEventListener("hashchange", apply);
    return () => window.removeEventListener("hashchange", apply);
  }, []);

  const open = useCallback(
    (ref?: ItemRef) => {
      setSelected(ref);
      if (pid) history.replaceState(null, "", tasksHash(pid, ref));
    },
    [pid],
  );

  useEffect(() => {
    if (!pid) return;
    save("lec-tasks-project", String(pid));
    setTrackers(undefined);
    api
      .request<TrackersResponse>(`/projects/${pid}/trackers`)
      .then(setTrackers)
      .catch((e) => setTrackers({ forge: { error: errorText(e) }, connections: [] }));
  }, [api, pid]);

  const conns = useMemo(() => (trackers?.connections || []).filter((c) => c.kind === "linear" || c.kind === "jira"), [trackers]);
  const connTab = tab.startsWith("c") ? conns.find((c) => `c${c.id}` === tab) : undefined;
  useEffect(() => {
    if (tab.startsWith("c") && trackers && !connTab) setTab("all");
  }, [tab, trackers, connTab]);
  useEffect(() => {
    if (tab === "queue" && trackers && !(trackers.forge.kind === "github" || trackers.forge.kind === "gitlab")) setTab("all");
  }, [tab, trackers]);

  const load = useCallback(async () => {
    if (!pid || tab === "queue") return;
    const g = ++generation.current;
    setLoading(true);
    setError("");
    const q = query ? `&q=${encodeURIComponent(query)}` : "";
    try {
      let res: WorkResponse;
      if (connTab) {
        const t = connTab.kind === "linear" && team ? `&team=${encodeURIComponent(team)}` : "";
        const items = await api.request<Item[]>(`/trackers/${connTab.id}/issues?${filterQuery(filter)}${q}${t}`);
        res = { items, sources: [{ source: connTab.kind, name: connTab.name, ok: true, count: items.length, connection_id: connTab.id }] };
      } else {
        const kind = tab === "pr" || tab === "issue" ? `&kind=${tab}` : "";
        res = await api.request<WorkResponse>(`/projects/${pid}/work?${filterQuery(filter)}${kind}${q}`);
      }
      if (g === generation.current) setData(res);
    } catch (e) {
      if (g === generation.current) {
        setError(errorText(e));
        setData(undefined);
      }
    } finally {
      if (g === generation.current) setLoading(false);
    }
  }, [api, pid, connTab, tab, filter, query, team]);
  useEffect(() => {
    void load();
  }, [load, refreshVersion]);

  useEffect(() => {
    setStates([]);
    if (connTab?.kind === "linear")
      api.request<Transition[]>(`/trackers/${connTab.id}/states${team ? `?team=${encodeURIComponent(team)}` : ""}`).then(setStates).catch(() => {});
  }, [api, connTab, team]);
  useEffect(() => {
    setTeams([]);
    setTeam(connTab?.kind === "linear" ? String(connTab.config.team_key || "") : "");
    if (connTab?.kind === "linear") api.request<{ key: string; name: string }[]>(`/trackers/${connTab.id}/teams`).then(setTeams).catch(() => {});
  }, [api, connTab]);

  // search: instant local narrowing, then the server's own search
  useEffect(() => {
    const t = window.setTimeout(() => setQuery(text.trim()), 450);
    return () => window.clearTimeout(t);
  }, [text]);

  const items = useMemo(() => (data?.items || []).filter((i) => matches(i, text)), [data, text]);
  const failed = (data?.sources || []).filter((s) => !s.ok);
  const counts = useMemo(() => {
    const c = { pr: 0, issue: 0 };
    for (const s of data?.sources || []) if (s.ok && !s.connection_id && (s.kind === "pr" || s.kind === "issue")) c[s.kind] += s.count;
    return c;
  }, [data]);

  function started(res: StartedWork) {
    if (res.kind === "session" && res.session) {
      onNotice(
        res.branch
          ? t("trackers.hub.startedSessionOn", { name: res.session.name, branch: res.branch })
          : t("trackers.hub.startedSession", { name: res.session.name }),
      );
      onOpenSession(res.session.id, res.session.name);
    } else if (res.task) {
      onNotice(t("trackers.hub.createdTask", { id: res.task.id }));
      onOpenTask(res.task.id);
    }
  }

  if (!projects.length)
    return (
      <section className="tasks-hub">
        <div className="page-heading">
          <div>
            <h2>{t("trackers.hub.title")}</h2>
            <p>{t("trackers.hub.noProjects")}</p>
          </div>
        </div>
      </section>
    );

  const forge = trackers?.forge;
  const forgeName = forge?.kind ? SOURCE_NAME[forge.kind] : "GitHub";
  const showBoardToggle = true;
  const queueable = forge?.kind === "github" || forge?.kind === "gitlab";
  const detail =
    selected && pid ? (
      selected.kind === "pr" ? (
        <PRPage key={`${selected.source}/${selected.id}`} api={api} projectId={pid} item={selected} onClose={() => open(undefined)} onOpen={open}
          onNotice={onNotice} onChanged={() => void load()} onStarted={started}
          onQueue={queueable ? () => { setTab("queue"); open(undefined); } : undefined} />
      ) : (
        <IssuePage key={`${selected.source}/${selected.connection_id}/${selected.id}`} api={api} projectId={pid} item={selected} onClose={() => open(undefined)}
          onOpen={open} onNotice={onNotice} onChanged={() => void load()} onStarted={started} />
      )
    ) : null;

  return (
    <section className={`tasks-hub ${selected ? "has-detail" : ""} th-view-${view}`}>
      <div className="page-heading th-heading">
        <div>
          <h2>{t("trackers.hub.title")}</h2>
          <p>
            {forge?.repo ? (
              <>
                {forgeName} ·{" "}
                <a href={forge.url} target="_blank" rel="noreferrer">
                  {forge.repo}
                </a>
                {conns.length > 0 && <> · {conns.map((c) => c.name).join(" · ")}</>}
              </>
            ) : (
              t("trackers.hub.subtitle")
            )}
          </p>
        </div>
        <div className="th-head-actions">
          {projects.length > 1 && (
            <select aria-label={t("trackers.hub.project")} value={pid} onChange={(e) => { setProjectId(Number(e.target.value)); open(undefined); }}>
              {projects.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.name}
                </option>
              ))}
            </select>
          )}
          <button className="b" onClick={() => void load()} disabled={loading} aria-label={t("trackers.hub.refresh")}>
            {loading ? t("trackers.loading") : t("trackers.hub.refresh")}
          </button>
          {pid && (
            <button className="b" onClick={() => onSettings(pid)}>
              {t("trackers.hub.connect")}
            </button>
          )}
        </div>
      </div>

      <div className="th-tabs" role="tablist" aria-label={t("trackers.hub.source")}>
        {(
          [
            ["all", t("trackers.hub.tab.all")],
            ["pr", forge?.kind === "gitlab" ? t("trackers.hub.tab.mergeRequests") : t("trackers.hub.tab.pullRequests"), counts.pr],
            ["issue", issuesLabel(forge?.kind), counts.issue],
            ...(queueable ? [["queue", forge?.kind === "gitlab" ? t("trackers.hub.tab.mergeTrain") : t("trackers.hub.tab.mergeQueue")] as const] : []),
            ...conns.map((c) => [`c${c.id}`, c.name] as const),
          ] as [Tab, string, number?][]
        ).map(([key, label, n]) => (
          <button key={key} role="tab" aria-selected={tab === key} className={tab === key ? "on" : ""}
            onClick={() => { setTab(key); save("lec-tasks-tab", key); }}>
            {label}
            {n !== undefined && tab !== "all" && !connTab && data && <b className="th-count">{n}</b>}
          </button>
        ))}
      </div>

      {tab !== "queue" && (
      <div className="th-toolbar">
        <div className="th-chips" role="group" aria-label={t("trackers.hub.filter")}>
          {FILTERS().filter((f) => !f.pr || tab === "pr" || tab === "all").map((f) => (
            <button key={f.key} className={filter === f.key ? "on" : ""} aria-pressed={filter === f.key}
              onClick={() => { setFilter(f.key); save("lec-tasks-filter", f.key); }}>
              {f.label}
            </button>
          ))}
        </div>
        {teams.length > 1 && (
          <select className="th-team" aria-label={t("trackers.hub.linearTeam")} value={team} onChange={(e) => setTeam(e.target.value)}>
            {teams.map((t) => (
              <option key={t.key} value={t.key}>
                {t.name} ({t.key})
              </option>
            ))}
          </select>
        )}
        <input className="th-search" type="search" aria-label={t("trackers.hub.search")} placeholder={t("trackers.hub.searchPlaceholder")} value={text} onChange={(e) => setText(e.target.value)} />
        {showBoardToggle && (
          <div className="th-viewtoggle" role="group" aria-label={t("trackers.hub.view")}>
            {(["list", "board", "table"] as View[]).map((v) => (
              <button key={v} className={view === v ? "on" : ""} aria-pressed={view === v} onClick={() => { setView(v); save("lec-tasks-view", v); }}>
                {VIEW_LABEL()[v]}
              </button>
            ))}
          </div>
        )}
      </div>
      )}

      {forge?.error && !forge.repo && (tab === "all" || tab === "pr" || tab === "issue") && (
        <p className="th-banner" role="status">
          {forge.error}{" "}
          {pid && (
            <button className="b" onClick={() => onSettings(pid)}>
              {t("trackers.hub.setUp")}
            </button>
          )}
        </p>
      )}
      {failed.map((s: WorkSource, i) => (
        <p key={i} className="th-banner th-banner-err" role="status">
          <b>{s.name}</b> ({SOURCE_NAME[s.source]}): {s.error}
        </p>
      ))}
      {error && <p className="th-banner th-banner-err" role="alert">{error}</p>}

      <div className="th-body">
        <div className="th-list" aria-busy={loading}>
          {tab === "queue" && pid && queueable && (
            <MergeQueue api={api} projectId={pid} source={forge!.kind as "github" | "gitlab"} onOpen={open} onNotice={onNotice} />
          )}
          {tab !== "queue" && !loading && data && items.length === 0 && <p className="th-empty">{text ? t("trackers.hub.emptySearch") : t("trackers.hub.empty")}</p>}
          {!data && loading && <p className="th-empty">{t("trackers.loading")}</p>}
          {tab !== "queue" && view === "list" && (
            <ul className="th-rows">
              {items.map((it) => (
                <li key={`${it.source}/${it.connection_id || 0}/${it.kind}/${it.id}`}>
                  <button className={`th-row ${sameRef(selected, it) ? "on" : ""}`} onClick={() => open({ source: it.source, kind: it.kind, id: it.id, connection_id: it.connection_id })}>
                    <span className={`th-kind th-kind-${it.kind} th-src-${it.source}`} aria-hidden="true">
                      {it.kind === "pr" ? "⇄" : it.source === "linear" ? "◆" : it.source === "jira" ? "◼" : "●"}
                    </span>
                    <span className="th-row-main">
                      <span className="th-row-title">{it.title}</span>
                      <span className="th-row-meta">
                        <span className="th-num">{itemMark(it)}</span>
                        <StatePill item={it} />
                        {it.kind === "pr" && it.head && <code className="th-branch">{it.head}</code>}
                        {it.author && <span>{t("trackers.hub.by", { name: it.author })}</span>}
                        <span>{ago(it.updated_at)}</span>
                      </span>
                      <span className="th-row-badges">
                        <ChecksBadge checks={it.checks} />
                        <ReviewBadge review={it.review} />
                        {it.conflicts && <span className="th-badge th-conflicts">{t("trackers.conflictsBadge")}</span>}
                        {it.priority && it.priority !== "No priority" && <span className="th-badge">{it.priority}</span>}
                        <Labels labels={it.labels} />
                      </span>
                    </span>
                    {it.assignees[0] && <Avatar name={it.assignees[0]} />}
                  </button>
                </li>
              ))}
            </ul>
          )}
          {tab !== "queue" && view === "table" && items.length > 0 && (
            <div className="th-table-wrap">
              <table className="th-table">
                <thead>
                  <tr>
                    <th>{t("trackers.hub.col.id")}</th>
                    <th>{t("trackers.hub.col.title")}</th>
                    <th>{t("trackers.hub.col.status")}</th>
                    <th>{t("trackers.hub.col.assignees")}</th>
                    <th>{t("trackers.hub.col.updated")}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((it) => (
                    <tr key={`${it.source}/${it.connection_id || 0}/${it.kind}/${it.id}`} className={sameRef(selected, it) ? "on" : ""}
                      onClick={() => open({ source: it.source, kind: it.kind, id: it.id, connection_id: it.connection_id })}>
                      <td className="th-num">{itemMark(it)}</td>
                      <td>
                        <button className="th-cell-btn" onClick={(e) => { e.stopPropagation(); open({ source: it.source, kind: it.kind, id: it.id, connection_id: it.connection_id }); }}>
                          {it.title}
                        </button>{" "}
                        <ChecksBadge checks={it.checks} />
                      </td>
                      <td>
                        <StatePill item={it} />
                      </td>
                      <td>{it.assignees.join(", ") || "—"}</td>
                      <td>{ago(it.updated_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
          {tab !== "queue" && view === "board" && (
            <div className="th-board">
              {boardColumns(items, connTab?.kind === "linear" && (filter === "all" || filter === "open") ? states.filter((s) => filter === "all" || !["completed", "canceled"].includes(s.type || "")) : []).map((col) => (
                <div className="th-col" key={col.key}>
                  <h4>
                    {col.title} <b>{col.items.length}</b>
                  </h4>
                  {col.items.map((it) => (
                    <button key={`${it.source}/${it.connection_id || 0}/${it.kind}/${it.id}`} className={`th-card ${sameRef(selected, it) ? "on" : ""}`}
                      onClick={() => open({ source: it.source, kind: it.kind, id: it.id, connection_id: it.connection_id })}>
                      <span className="th-num">{itemMark(it)}</span>
                      <span className="th-row-title">{it.title}</span>
                      <span className="th-row-badges">
                        <ChecksBadge checks={it.checks} />
                        {it.assignees[0] && <span className="th-muted">{it.assignees[0]}</span>}
                      </span>
                    </button>
                  ))}
                </div>
              ))}
            </div>
          )}
        </div>
        {detail && <aside className="th-detail" aria-label={t("trackers.hub.details")}>{detail}</aside>}
      </div>
    </section>
  );
}
