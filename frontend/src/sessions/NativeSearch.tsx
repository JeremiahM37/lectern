import { useEffect, useRef, useState, type CSSProperties } from "react";
import "../terminal/native-search.css";
import { Modal } from "./Modal";
import type { SessionsApi } from "./Sessions";
import type { SessionView, Target } from "../types";
import type { JsonValue } from "../api";
import { t, useLocale } from "../i18n";
interface Hit {
  id: string;
  title: string;
  target: string;
  agent: string;
  cwd: string;
  snippet: string;
}
interface Scope {
  id: string;
  target: string;
  agent: string;
  state: string;
  error?: string;
  more?: boolean;
  progress: {
    documents: number;
    pending_files?: number;
    oversized_entries?: number;
    issues?: string[];
  };
}
interface Result {
  id: string;
  done: boolean;
  complete: boolean;
  results: Hit[];
  scopes: Scope[];
}
interface Choice {
  id: string;
  label: string;
  model?: string;
  supported: boolean;
}
interface Message {
  role: string;
  text: string;
  matched?: boolean;
  truncated?: boolean;
}
interface SearchPage {
  messages: Message[];
  before: number | null;
  after: number | null;
  fork_options?: Choice[];
  page_mode?: string;
  changed_neighbors?: number;
  index_complete?: boolean;
}
const explain = (e: unknown) => (e instanceof Error ? e.message : String(e));
export function NativeSearch({
  api,
  targets,
  onClose,
  onFork,
  onNotice,
}: {
  api: SessionsApi;
  targets: Target[];
  onClose(): void;
  onFork(s: SessionView): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [query, setQuery] = useState(""),
    [target, setTarget] = useState(""),
    [agent, setAgent] = useState(""),
    [result, setResult] = useState<Result>(),
    [status, setStatus] = useState(""),
    [starting, setStarting] = useState(false),
    [retry, setRetry] = useState(false),
    [hit, setHit] = useState<Hit>(),
    [page, setPage] = useState<SearchPage>(),
    [readStatus, setReadStatus] = useState(""),
    [reading, setReading] = useState(false),
    [forking, setForking] = useState(false),
    [forkPending, setForkPending] = useState(false),
    [configuration, setConfiguration] = useState(""),
    [name, setName] = useState(() => t("conversation.search.defaultForkName")),
    [isolated, setIsolated] = useState(false),
    [branch, setBranch] = useState(""),
    [base, setBase] = useState(""),
    [forkStatus, setForkStatus] = useState(""),
    [height, setHeight] = useState(
      window.visualViewport?.height || innerHeight,
    );
  const alive = useRef(true),
    generation = useRef(0),
    readGeneration = useRef(0),
    pollGeneration = useRef(0),
    job = useRef<string | undefined>(undefined),
    last = useRef<Result | undefined>(undefined),
    timer = useRef<number | undefined>(undefined),
    startBusy = useRef(false),
    forkBusy = useRef(false),
    results = useRef<HTMLDivElement>(null),
    messages = useRef<HTMLDivElement>(null),
    queryInput = useRef<HTMLInputElement>(null),
    backButton = useRef<HTMLButtonElement>(null),
    forkButton = useRef<HTMLButtonElement>(null),
    configInput = useRef<HTMLSelectElement>(null);
  const cancel = (id: string) =>
    api.request<Result>(`/conversation-search/${id}`, { method: "DELETE" });
  function accept(value: Result) {
    last.current = value;
    setResult(value);
    setRetry(false);
    setStatus("");
  }
  function later(id: string, version: number, ms = 600) {
    clearTimeout(timer.current);
    timer.current = window.setTimeout(() => void poll(id, version), ms);
  }
  async function poll(id: string, version: number) {
    const request = ++pollGeneration.current;
    try {
      const value = await api.request<Result>(`/conversation-search/${id}`);
      if (
        !alive.current ||
        version !== generation.current ||
        request !== pollGeneration.current
      )
        return;
      accept(value);
      if (!value.done) later(id, version);
    } catch (e) {
      if (
        alive.current &&
        version === generation.current &&
        request === pollGeneration.current
      ) {
        setStatus(
          t("conversation.search.updateFailed", { error: explain(e) }),
        );
        setRetry(true);
      }
    }
  }
  async function start(reset = false) {
    if (startBusy.current || !query.trim()) return;
    const version = ++generation.current,
      previous = job.current;
    job.current = undefined;
    startBusy.current = true;
    setStarting(true);
    clearTimeout(timer.current);
    setRetry(false);
    setStatus(t("conversation.search.starting"));
    try {
      if (previous && !last.current?.done) {
        try {
          await cancel(previous);
        } catch (e) {
          if (!(
            e &&
            typeof e === "object" &&
            "status" in e &&
            e.status === 404
          ))
            throw e;
        }
      }
      if (!alive.current || version !== generation.current) return;
      const body: Record<string, JsonValue> = { query, reset };
      if (target) body.target_id = Number(target);
      if (agent) body.agent = agent;
      const value = await api.request<Result>("/conversation-search", {
        method: "POST",
        body,
      });
      if (!alive.current || version !== generation.current) {
        void cancel(value.id).catch(() => {});
        return;
      }
      job.current = value.id;
      accept(value);
      if (!value.done) later(value.id, version, 300);
    } catch (e) {
      if (alive.current && version === generation.current) {
        job.current = previous;
        setStatus(t("conversation.search.startFailed", { error: explain(e) }));
      }
    } finally {
      startBusy.current = false;
      if (alive.current) setStarting(false);
    }
  }
  async function stop() {
    const id = job.current,
      version = generation.current;
    if (!id) return;
    try {
      const value = await cancel(id);
      if (!alive.current || version !== generation.current) return;
      clearTimeout(timer.current);
      pollGeneration.current++;
      accept(value);
      if (!value.done) later(id, version);
    } catch (e) {
      if (alive.current) setStatus(t("conversation.search.stopFailed", { error: explain(e) }));
    }
  }
  function close() {
    if (forkBusy.current) return;
    onClose();
  }
  useEffect(() => {
    alive.current = true;
    queryInput.current?.focus();
    const fit = () => setHeight(window.visualViewport?.height || innerHeight);
    window.visualViewport?.addEventListener("resize", fit);
    return () => {
      alive.current = false;
      generation.current++;
      readGeneration.current++;
      clearTimeout(timer.current);
      if (job.current && !last.current?.done)
        void cancel(job.current).catch(() => {});
      window.visualViewport?.removeEventListener("resize", fit);
    };
  }, []);
  async function read(selected: Hit, suffix = "") {
    const version = ++readGeneration.current,
      id = job.current;
    if (!id) return;
    setHit(selected);
    setForking(false);
    setReading(true);
    setReadStatus(t("conversation.search.loadingMatch"));
    setPage(undefined);
    requestAnimationFrame(() => backButton.current?.focus());
    try {
      const value = await api.request<SearchPage>(
        `/conversation-search/${id}/results/${selected.id}${suffix}`,
      );
      if (!alive.current || version !== readGeneration.current) return;
      setPage(value);
      setReadStatus(
        t("conversation.search.readStatus", {
          mode:
            value.page_mode === "latest"
              ? t("conversation.search.modeLatest")
              : value.page_mode && value.page_mode !== "match"
                ? t("conversation.search.modeSaved")
                : t("conversation.search.modeMatch"),
          changed: value.changed_neighbors
            ? t("conversation.search.changedOmitted", { count: value.changed_neighbors })
            : "",
          incomplete: !value.index_complete ? t("conversation.search.indexIncomplete") : "",
        }),
      );
      requestAnimationFrame(() => {
        if (messages.current) {
          messages.current.scrollTop = 0;
          messages.current
            .querySelector(".ns-match")
            ?.scrollIntoView({ block: "center" });
        }
      });
    } catch (e) {
      if (alive.current && version === readGeneration.current)
        setReadStatus(t("conversation.search.readFailed", { error: explain(e) }));
    } finally {
      if (alive.current && version === readGeneration.current)
        setReading(false);
    }
  }
  function back() {
    readGeneration.current++;
    setHit(undefined);
    setForking(false);
    requestAnimationFrame(() => {
      const button = Array.from(
        results.current?.querySelectorAll<HTMLButtonElement>("button") || [],
      ).find((button) => button.dataset.resultId === hit?.id);
      button?.focus({ preventScroll: true });
    });
  }
  const choices =
    page?.fork_options?.filter((choice) => choice.supported) || [];
  function confirmFork() {
    if (!choices.length) return;
    setConfiguration(choices.length === 1 ? choices[0]!.id : "");
    setForkStatus("");
    setForking(true);
    requestAnimationFrame(() => configInput.current?.focus());
  }
  async function createFork() {
    if (forkBusy.current || !hit || !job.current || !configuration) return;
    forkBusy.current = true;
    setForkPending(true);
    setForkStatus(t("conversation.search.startingFork"));
    const body: Record<string, JsonValue> = {
      configuration_id: configuration,
      name,
    };
    if (isolated) {
      body.background = true;
      body.worktree = { branch, base };
    }
    try {
      const session = await api.request<SessionView>(
        `/conversation-search/${job.current}/results/${hit.id}/fork`,
        { method: "POST", body },
      );
      if (alive.current) {
        onFork(session);
        onClose();
      }
    } catch (e) {
      if (alive.current) setForkStatus(explain(e));
      else onNotice(explain(e), true);
    } finally {
      forkBusy.current = false;
      if (alive.current) setForkPending(false);
    }
  }
  const unfinished =
      result?.scopes.filter((scope) =>
        ["queued", "indexing"].includes(scope.state),
      ).length || 0,
    problemCount =
      result?.scopes.filter(
        (scope) =>
          scope.error ||
          scope.progress.issues?.length ||
          scope.progress.oversized_entries,
      ).length || 0;
  const summary =
    status ||
    (result
      ? t("conversation.search.summary", {
          found: t("conversation.search.found", { count: result.results.length }),
          progress: unfinished
            ? t("conversation.search.searching", { count: unfinished })
            : result.complete
              ? ""
              : t("conversation.search.incomplete"),
          more: result.scopes.some((scope) => scope.more) ? t("conversation.search.more") : "",
          hint: !result.results.length && result.complete ? t("conversation.search.tryDifferent") : "",
        })
      : t("conversation.search.intro"));
  return (
    <Modal
      className="native-history native-search"
      aria-label={t("conversation.search.title")}
      style={{ "--search-height": `${height}px` } as CSSProperties}
      onCancel={(e) => {
        e.preventDefault();
        close();
      }}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (
          e.nativeEvent.isComposing ||
          hit ||
          !["ArrowDown", "ArrowUp"].includes(e.key)
        )
          return;
        const buttons = Array.from(
            results.current?.querySelectorAll<HTMLButtonElement>(
              ".ns-result",
            ) || [],
          ),
          index = buttons.indexOf(e.target as HTMLButtonElement);
        if (e.target !== queryInput.current && index < 0) return;
        e.preventDefault();
        buttons[
          Math.max(
            0,
            Math.min(
              buttons.length - 1,
              index + (e.key === "ArrowDown" ? 1 : -1),
            ),
          )
        ]?.focus();
      }}
    >
      <header>
        <h2>{t("conversation.search.title")}</h2>
        <button
          className="ns-close"
          aria-label={t("conversation.search.closeLabel")}
          disabled={forkPending}
          onClick={close}
        >
          {t("conversation.search.close")}
        </button>
      </header>
      <section className="ns-browse" hidden={!!hit}>
        <form
          className="ns-form"
          onSubmit={(e) => {
            e.preventDefault();
            void start();
          }}
        >
          <label htmlFor="native-search-query">{t("conversation.search.queryLabel")}</label>
          <input
            id="native-search-query"
            className="ns-query"
            ref={queryInput}
            type="search"
            required
            maxLength={500}
            placeholder={t("conversation.search.queryPlaceholder")}
            autoComplete="off"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
          <div className="ns-filters">
            <label>
              {t("conversation.search.target")}
              <select
                aria-label={t("conversation.search.target")}
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              >
                <option value="">{t("conversation.search.allTargets")}</option>
                {targets.map((row) => (
                  <option key={row.id} value={row.id}>
                    {row.name}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t("conversation.search.agent")}
              <select
                aria-label={t("conversation.search.agent")}
                value={agent}
                onChange={(e) => setAgent(e.target.value)}
              >
                <option value="">{t("conversation.search.claudeAndCodex")}</option>
                <option value="claude">Claude</option>
                <option value="codex">Codex</option>
              </select>
            </label>
            <button disabled={starting}>{t("conversation.search.search")}</button>
            {result && !result.done && !starting && (
              <button type="button" onClick={() => void stop()}>
                {t("conversation.search.stop")}
              </button>
            )}
          </div>
        </form>
        <p className="ns-status" role="status">
          {summary}
        </p>
        {retry && (
          <button
            className="ns-retry"
            onClick={() => {
              if (job.current) void poll(job.current, generation.current);
            }}
          >
            {t("conversation.search.retry")}
          </button>
        )}
        {!!result?.scopes.length && (
          <details className="ns-progress">
            <summary>
              {t("conversation.search.progress", { count: result.scopes.length })}
              {problemCount ? t("conversation.search.withIssues", { count: problemCount }) : ""}
            </summary>
            <div>
              {result.scopes.map((scope) => (
                <p key={scope.id}>
                  {t("conversation.search.scopeLine", {
                    target: scope.target,
                    agent: scope.agent,
                    state: scope.error || scope.state,
                    documents: scope.progress.documents,
                  })}
                  {scope.progress.pending_files
                    ? t("conversation.search.pending", { count: scope.progress.pending_files })
                    : ""}
                  {scope.progress.oversized_entries
                    ? t("conversation.search.oversized", { count: scope.progress.oversized_entries })
                    : ""}
                  {scope.progress.issues?.length
                    ? " · " + scope.progress.issues.join("; ")
                    : ""}
                </p>
              ))}
            </div>
          </details>
        )}
        <div
          className="ns-results"
          aria-label={t("conversation.search.resultsLabel")}
          ref={results}
        >
          {result?.results.map((row) => (
            <button
              type="button"
              className="ns-result"
              data-result-id={row.id}
              key={row.id}
              onClick={() => void read(row)}
            >
              <strong>{row.title}</strong>
              <small>
                {row.target} · {row.agent} · {row.cwd}
              </small>
              <span>{row.snippet}</span>
            </button>
          ))}
        </div>
        <details className="ns-advanced">
          <summary>{t("conversation.search.options")}</summary>
          <p>{t("conversation.search.rebuildHelp")}</p>
          <button disabled={starting} onClick={() => void start(true)}>
            {t("conversation.search.rebuild")}
          </button>
        </details>
      </section>
      <section className="ns-reader" hidden={!hit}>
        <div className="ns-reader-head">
          <button ref={backButton} disabled={forkPending} onClick={back}>
            {t("conversation.search.back")}
          </button>
          <p className="ns-location">
            {hit && `${hit.target} · ${hit.agent} · ${hit.cwd}`}
          </p>
        </div>
        <div className="ns-page-controls" hidden={forking}>
          <button
            disabled={reading || page?.before == null}
            onClick={() => hit && void read(hit, `?before=${page?.before}`)}
          >
            {t("conversation.search.earlier")}
          </button>
          <button
            disabled={reading || page?.after == null}
            onClick={() => hit && void read(hit, `?after=${page?.after}`)}
          >
            {t("conversation.search.later")}
          </button>
          <button
            disabled={reading}
            onClick={() => hit && void read(hit, "?latest=1")}
          >
            {t("conversation.search.latestIndexed")}
          </button>
          <button disabled={reading} onClick={() => hit && void read(hit)}>
            {t("conversation.search.backToMatch")}
          </button>
          {choices.length > 0 && (
            <button ref={forkButton} disabled={reading} onClick={confirmFork}>
              {t("conversation.search.fork")}
            </button>
          )}
        </div>
        <p className="ns-read-status" role="status">
          {readStatus}
        </p>
        <div className="nh-messages" ref={messages} hidden={forking}>
          {page?.messages.map((message, index) =>
            message.role === "tool" ? (
              <details
                key={index}
                className={`nh-message nh-tool${message.matched ? " ns-match" : ""}`}
                open={message.matched || undefined}
              >
                <summary>
                  {t("conversation.search.toolActivity")}
                  {message.matched ? t("conversation.search.matching") : ""}
                </summary>
                <pre>{message.text}</pre>
              </details>
            ) : (
              <article
                key={index}
                className={`nh-message nh-${message.role}${message.matched ? " ns-match" : ""}`}
              >
                <h3>
                  {message.role === "user" ? t("conversation.search.you") : t("conversation.search.assistant")}
                  {message.matched ? t("conversation.search.matching") : ""}
                </h3>
                <pre>{message.text}</pre>
                {message.truncated && (
                  <small>{t("conversation.search.longShortened")}</small>
                )}
              </article>
            ),
          )}
        </div>
        {forking && (
          <form
            className="ns-fork-form"
            onSubmit={(e) => {
              e.preventDefault();
              void createFork();
            }}
          >
            <h3>{t("conversation.search.forkTitle")}</h3>
            <p className="ns-fork-warning">
              {t("conversation.search.forkWarning")}{" "}
              {isolated
                ? t("conversation.search.forkIsolated")
                : t("conversation.search.forkShared")}{" "}
              {t("conversation.search.forkIntact")}
            </p>
            <label>
              {t("conversation.search.launchSettings")}
              <select
                ref={configInput}
                aria-label={t("conversation.search.launchSettings")}
                required
                disabled={forkPending}
                value={configuration}
                onChange={(e) => setConfiguration(e.target.value)}
              >
                {choices.length > 1 && (
                  <option value="">{t("conversation.search.chooseLaunch")}</option>
                )}
                {choices.map((choice) => (
                  <option key={choice.id} value={choice.id}>
                    {choice.label}
                    {choice.model ? " · " + choice.model : ""}
                  </option>
                ))}
              </select>
            </label>
            <label>
              {t("conversation.search.sessionName")}
              <input
                aria-label={t("conversation.search.sessionName")}
                value={name}
                maxLength={160}
                disabled={forkPending}
                onChange={(e) => setName(e.target.value)}
              />
            </label>
            <label>
              {t("conversation.search.workspace")}
              <select
                aria-label={t("conversation.search.forkWorkspace")}
                disabled={forkPending}
                value={isolated ? "isolated" : "shared"}
                onChange={(e) => setIsolated(e.target.value === "isolated")}
              >
                <option value="shared">{t("conversation.search.sameWorkspace")}</option>
                <option value="isolated">{t("conversation.search.isolatedWorktree")}</option>
              </select>
            </label>
            {isolated && (
              <>
                <label>
                  {t("conversation.search.branch")}
                  <input
                    aria-label={t("conversation.search.branch")}
                    value={branch}
                    disabled={forkPending}
                    onChange={(e) => setBranch(e.target.value)}
                  />
                </label>
                <label>
                  {t("conversation.search.base")}
                  <input
                    aria-label={t("conversation.search.base")}
                    value={base}
                    disabled={forkPending}
                    onChange={(e) => setBase(e.target.value)}
                  />
                </label>
              </>
            )}
            <p className="ns-fork-status" role="status">
              {forkStatus}
            </p>
            <button disabled={forkPending}>{t("conversation.search.createFork")}</button>
            <button
              type="button"
              disabled={forkPending}
              onClick={() => {
                setForking(false);
                requestAnimationFrame(() => forkButton.current?.focus());
              }}
            >
              {t("conversation.search.cancelFork")}
            </button>
          </form>
        )}
      </section>
    </Modal>
  );
}
