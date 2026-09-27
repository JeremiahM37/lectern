import "../terminal/native-history.css";
import { Modal } from "./Modal";
import { useEffect, useRef, useState } from "react";
import type { SessionView, Target } from "../types";
import type { JsonValue } from "../api";
import type { SessionsApi } from "./Sessions";
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
  const [data, setData] = useState<HistoryList>(),
    [cid, setCid] = useState(""),
    [messages, setMessages] = useState<Msg[]>([]),
    [before, setBefore] = useState<number | null>(null),
    [action, setAction] = useState<"fork" | "resume">(),
    [isolated, setIsolated] = useState(false),
    [branch, setBranch] = useState(""),
    [base, setBase] = useState(""),
    [name, setName] = useState(session.name + " · fork"),
    [pending, setPending] = useState(false),
    [status, setStatus] = useState("Finding saved conversations…");
  const generation = useRef(0);
  async function list() {
    const g = ++generation.current;
    setStatus("Finding saved conversations…");
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
            ? "Loading saved messages…"
            : "Choose a saved conversation to see available actions."
          : "No saved conversations found in this workspace.",
      );
      if (selected && selected === cid) void read();
    } catch (e) {
      if (g === generation.current) setStatus(String(e));
    }
  }
  async function read(older = false) {
    if (!cid) return;
    const g = ++generation.current;
    setStatus("Loading saved messages…");
    try {
      const page = await api.request<HistoryPage>(
        `/sessions/${session.id}/conversations/${encodeURIComponent(cid)}${older && before != null ? `?before=${before}` : ""}`,
      );
      if (g !== generation.current) return;
      setMessages((v) => (older ? [...page.messages, ...v] : page.messages));
      setBefore(page.before);
      setStatus(
        page.messages_readable === false
          ? `${page.conversation.agent} · ${cid} — Lectern can resume and fork this conversation but does not read its messages.`
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
        ? "The current terminal’s conversation is marked in the list."
        : "The current terminal has not saved readable messages yet."
      : data?.current?.state === "ambiguous"
        ? "Several conversations are active in this terminal. Choose one explicitly."
        : "The current conversation could not be identified. Choose one explicitly.";
  return (
    <Modal
      className="native-history"
      aria-label="Saved conversations"
      onCancel={(e) => {
        if (pending) e.preventDefault();
        else onClose();
      }}
    >
      <header>
        <div>
          <h2>Saved conversations</h2>
          <p className="nh-name">{session.name}</p>
        </div>
        <button className="nh-close" disabled={pending} onClick={onClose}>
          Close
        </button>
      </header>
      <p className="nh-explain" hidden={!!action}>
        {explanation}
        {data?.scan_limited &&
          " Showing up to 500 discovered transcript files, prioritizing the current terminal."}
      </p>
      <div className="nh-controls" hidden={!!action}>
        <select
          hidden={!!action}
          className="nh-select"
          aria-label="Conversation"
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
          <option value="">Choose a saved conversation</option>
          {data?.conversations.map((c) => (
            <option value={c.id} key={c.id}>
              {c.id === data.current?.id ? "Current terminal · " : ""}
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
          Refresh
        </button>
        <button
          className="nh-fork"
          hidden={!!action}
          disabled={!cid || pending || !data?.fork_supported}
          onClick={() => {
            setAction("fork");
            setName(session.name + " · fork");
          }}
        >
          Fork conversation
        </button>
        {data?.resume_supported && (
          <button
            className="nh-resume"
            hidden={!!action}
            disabled={!cid || pending}
            onClick={() => {
              setAction("resume");
              setName(session.name + " · resumed");
            }}
          >
            Resume conversation
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
              <summary>Tool activity</summary>
              <pre>{m.text}</pre>
              {m.truncated && (
                <small>Long message shortened in this view.</small>
              )}
            </details>
          ) : (
            <article key={i} className={`nh-message nh-${m.role}`}>
              <b>
                {m.role === "user"
                  ? "You"
                  : m.role === "assistant"
                    ? "Assistant"
                    : "Tool activity"}
              </b>
              <pre>{m.text}</pre>
              {m.truncated && (
                <small>Long message shortened in this view.</small>
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
          Load earlier messages
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
              ? "Continue this same saved conversation in its original workspace? The previous terminal must be stopped."
              : isolated
                ? "Fork into a new Git worktree. Uncommitted changes stay in the original workspace."
                : "Create an independent conversation using the same workspace files."}
          </p>
          <label>
            New session name
            <input
              className="nh-fork-name"
              aria-label="Session name"
              disabled={pending}
              value={name}
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          {action === "fork" && (
            <>
              <label>
                Workspace
                <select
                  aria-label="Workspace"
                  className="nh-workspace"
                  disabled={pending}
                  value={isolated ? "isolated" : "shared"}
                  onChange={(e) => setIsolated(e.target.value === "isolated")}
                >
                  <option value="shared">Use the same files</option>
                  <option value="isolated">New isolated Git worktree</option>
                </select>
              </label>
              {isolated && (
                <>
                  <label>
                    New branch (blank = automatic)
                    <input
                      aria-label="New branch"
                      className="nh-branch"
                      disabled={pending}
                      value={branch}
                      onChange={(e) => setBranch(e.target.value)}
                    />
                  </label>
                  <label>
                    Base commit or branch (blank = HEAD)
                    <input
                      aria-label="Base commit or branch"
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
            {action === "resume" ? "Start resumed session" : "Create fork"}
          </button>
          <button
            disabled={pending}
            type="button"
            className="nh-cancel"
            onClick={() => setAction(undefined)}
          >
            Cancel
          </button>
        </form>
      )}
    </Modal>
  );
}

export { NativeSearch } from "./NativeSearch";
