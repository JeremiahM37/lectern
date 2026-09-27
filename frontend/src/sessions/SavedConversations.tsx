import "../terminal/native-history.css";
import { Modal } from "./Modal";
import { useEffect, useRef, useState } from "react";
import type { SessionView, Target } from "../types";
import type { JsonValue } from "../api";
import type { SessionsApi } from "./Sessions";
import { t, useLocale } from "../i18n";
type Conv = { id: string; modified: number; title: string };
type Msg = {
  role: string;
  text: string;
  matched?: boolean;
  truncated?: boolean;
};
interface HistoryList {
  conversations: Conv[];
  current?: { id: string; state: string; saved: boolean };
  fork_supported: boolean;
  resume_supported: boolean;
  scan_limited?: boolean;
}
interface HistoryPage {
  messages: Msg[];
  before: number | null;
  conversation: { agent: string };
  // false for a catalog agent: Lectern can list, resume and fork its
  // conversations but does not read their messages.
  messages_readable?: boolean;
}
export function NativeHistory({
  api,
  session,
  onClose,
  onSession,
  onNotice,
}: {
  api: SessionsApi;
  session: SessionView;
  onClose(): void;
  onSession(s: SessionView, action?: "fork" | "resume"): void;
  onNotice(t: string, e?: boolean): void;
}) {
  useLocale();
  const [data, setData] = useState<HistoryList>(),
    [cid, setCid] = useState(""),
    [messages, setMessages] = useState<Msg[]>([]),
    [before, setBefore] = useState<number | null>(null),
    [action, setAction] = useState<"fork" | "resume">(),
    [isolated, setIsolated] = useState(false),
    [branch, setBranch] = useState(""),
    [base, setBase] = useState(""),
    [name, setName] = useState(() => t("conversation.saved.forkName", { name: session.name })),
    [pending, setPending] = useState(false),
    [status, setStatus] = useState(() => t("conversation.saved.finding"));
  const generation = useRef(0);
  async function list() {
    const g = ++generation.current;
    setStatus(t("conversation.saved.finding"));
    try {
      const d = await api.request<HistoryList>(
        `/sessions/${session.id}/conversations`,
      );
      if (g !== generation.current) return;
      setData(d);
      const selected =
        cid ||
        (d.current?.state === "identified" && d.current.saved
          ? d.current.id
          : "");
      setCid(selected);
      setStatus(
        d.conversations.length
          ? selected
            ? t("conversation.saved.loading")
            : t("conversation.saved.choose")
          : t("conversation.saved.none"),
      );
      if (selected && selected === cid) void read();
    } catch (e) {
      if (g === generation.current) setStatus(String(e));
    }
  }
  async function read(older = false) {
    if (!cid) return;
    const g = ++generation.current;
    setStatus(t("conversation.saved.loading"));
    try {
      const page = await api.request<HistoryPage>(
        `/sessions/${session.id}/conversations/${encodeURIComponent(cid)}${older && before != null ? `?before=${before}` : ""}`,
      );
      if (g !== generation.current) return;
      setMessages((v) => (older ? [...page.messages, ...v] : page.messages));
      setBefore(page.before);
      setStatus(
        page.messages_readable === false
          ? t("conversation.saved.unreadable", { agent: page.conversation.agent, id: cid })
          : `${page.conversation.agent} · ${cid}`,
      );
    } catch (e) {
      if (g === generation.current) setStatus(String(e));
    }
  }
  useEffect(() => {
    void list();
    return () => {
      generation.current++;
    };
  }, []);
  useEffect(() => {
    if (cid) void read();
  }, [cid]);
  async function create() {
    if (!action || pending) return;
    setPending(true);
    try {
      const body: Record<string, JsonValue> = { conversation_id: cid, name };
      if (action === "fork" && isolated) {
        body.background = true;
        body.worktree = { branch, base };
      }
      const s = await api.request<SessionView>(
        `/sessions/${session.id}/${action}`,
        { method: "POST", body },
      );
      onSession(s, action);
      onClose();
    } catch (e) {
      setStatus(String(e));
    } finally {
      setPending(false);
    }
  }
  const explanation =
    data?.current?.state === "identified"
      ? data.current.saved
        ? t("conversation.saved.currentMarked")
        : t("conversation.saved.currentUnsaved")
      : data?.current?.state === "ambiguous"
        ? t("conversation.saved.ambiguous")
        : t("conversation.saved.unidentified");
  return (
    <Modal
      className="native-history"
      aria-label={t("conversation.saved.title")}
      onCancel={(e) => {
        if (pending) e.preventDefault();
        else onClose();
      }}
    >
      <header>
        <div>
          <h2>{t("conversation.saved.title")}</h2>
          <p className="nh-name">{session.name}</p>
        </div>
        <button className="nh-close" disabled={pending} onClick={onClose}>
          {t("conversation.saved.close")}
        </button>
      </header>
      <p className="nh-explain" hidden={!!action}>
        {explanation}
        {data?.scan_limited &&
          t("conversation.saved.scanLimited")}
      </p>
      <div className="nh-controls" hidden={!!action}>
        <select
          hidden={!!action}
          className="nh-select"
          aria-label={t("conversation.saved.conversation")}
          value={cid}
          disabled={pending}
          onChange={(e) => {
            if (e.target.value === cid) return;
            generation.current++;
            setMessages([]);
            setBefore(null);
            setCid(e.target.value);
          }}
        >
          <option value="">{t("conversation.saved.chooseOption")}</option>
          {data?.conversations.map((c) => (
            <option value={c.id} key={c.id}>
              {c.id === data.current?.id ? t("conversation.saved.currentTerminal") : ""}
              {new Date(c.modified * 1000).toLocaleString()} · {c.title} ·{" "}
              {c.id.slice(0, 8)}
            </option>
          ))}
        </select>
        <button
          className="nh-refresh"
          hidden={!!action}
          disabled={pending}
          onClick={() => void list()}
        >
          {t("conversation.saved.refresh")}
        </button>
        <button
          className="nh-fork"
          hidden={!!action}
          disabled={!cid || pending || !data?.fork_supported}
          onClick={() => {
            setAction("fork");
            setName(t("conversation.saved.forkName", { name: session.name }));
          }}
        >
          {t("conversation.saved.fork")}
        </button>
        {data?.resume_supported && (
          <button
            className="nh-resume"
            hidden={!!action}
            disabled={!cid || pending}
            onClick={() => {
              setAction("resume");
              setName(t("conversation.saved.resumedName", { name: session.name }));
            }}
          >
            {t("conversation.saved.resume")}
          </button>
        )}
      </div>
      <p className="nh-status" role="status">
        {status}
      </p>
      <div className="nh-messages" hidden={!!action}>
        {messages.map((m, i) =>
          m.role === "tool" ? (
            <details key={i} className="nh-message nh-tool">
              <summary>{t("conversation.saved.toolActivity")}</summary>
              <pre>{m.text}</pre>
              {m.truncated && (
                <small>{t("conversation.saved.longShortened")}</small>
              )}
            </details>
          ) : (
            <article key={i} className={`nh-message nh-${m.role}`}>
              <b>
                {m.role === "user"
                  ? t("conversation.saved.you")
                  : m.role === "assistant"
                    ? t("conversation.saved.assistant")
                    : t("conversation.saved.toolActivity")}
              </b>
              <pre>{m.text}</pre>
              {m.truncated && (
                <small>{t("conversation.saved.longShortened")}</small>
              )}
            </article>
          ),
        )}
      </div>
      {!action && before != null && (
        <button
          className="nh-older"
          disabled={pending}
          onClick={() => void read(true)}
        >
          {t("conversation.saved.loadEarlier")}
        </button>
      )}
      {action && (
        <form
          className="nh-confirm"
          onSubmit={(e) => {
            e.preventDefault();
            void create();
          }}
        >
          <p>
            {action === "resume"
              ? t("conversation.saved.confirmResume")
              : isolated
                ? t("conversation.saved.confirmIsolated")
                : t("conversation.saved.confirmShared")}
          </p>
          <label>
            {t("conversation.saved.newName")}
            <input
              className="nh-fork-name"
              aria-label={t("conversation.saved.sessionName")}
              disabled={pending}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          {action === "fork" && (
            <>
              <label>
                {t("conversation.saved.workspace")}
                <select
                  aria-label={t("conversation.saved.workspace")}
                  className="nh-workspace"
                  disabled={pending}
                  value={isolated ? "isolated" : "shared"}
                  onChange={(e) => setIsolated(e.target.value === "isolated")}
                >
                  <option value="shared">{t("conversation.saved.sameFiles")}</option>
                  <option value="isolated">{t("conversation.saved.isolatedWorktree")}</option>
                </select>
              </label>
              {isolated && (
                <>
                  <label>
                    {t("conversation.saved.newBranch")}
                    <input
                      aria-label={t("conversation.saved.newBranchLabel")}
                      className="nh-branch"
                      disabled={pending}
                      value={branch}
                      onChange={(e) => setBranch(e.target.value)}
                    />
                  </label>
                  <label>
                    {t("conversation.saved.base")}
                    <input
                      aria-label={t("conversation.saved.baseLabel")}
                      className="nh-base"
                      disabled={pending}
                      value={base}
                      onChange={(e) => setBase(e.target.value)}
                    />
                  </label>
                </>
              )}
            </>
          )}
          <button className="nh-create" disabled={pending}>
            {action === "resume" ? t("conversation.saved.startResumed") : t("conversation.saved.createFork")}
          </button>
          <button
            disabled={pending}
            type="button"
            className="nh-cancel"
            onClick={() => setAction(undefined)}
          >
            {t("conversation.saved.cancel")}
          </button>
        </form>
      )}
    </Modal>
  );
}

export { NativeSearch } from "./NativeSearch";
