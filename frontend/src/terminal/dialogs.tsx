import { errorMessage } from "./model";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { t } from "../i18n";
import { usePref } from "../prefs/store";
import { allTerminalThemes } from "../theme/terminal-themes";
import { saveTerminalPrefs, THEMES_KEY, useTerminalPrefs } from "../theme/terminal-prefs";
import { cleanQuickCommands, GLOBAL_KEY, projectKey, quickId, readGlobal, writeScope, type QuickCommand, type QuickScope } from "../quick/commands";
const NO_THEMES: unknown[] = [];
import {
  copyClipboard,
  downloadBlob,
  json,
  quote,
  type History,
  type Prefs,
  type TerminalInfo,
} from "./model";
export function Dialog({
  id,
  title,
  children,
  onClose,
  actions,
}: {
  id: string;
  title: string;
  children: ReactNode;
  onClose: () => void;
  actions?: ReactNode;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    ref.current?.showModal();
    return () => ref.current?.close();
  }, []);
  return (
    <dialog
      id={id}
      ref={ref}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <div className="dialog-head">
        <h2>{title}</h2>
        {actions}
        <button data-close onClick={onClose}>
          Close
        </button>
      </div>
      {children}
    </dialog>
  );
}
export function Appearance({
  prefs,
  minFont = 10,
  onPrefs,
  onClose,
}: {
  prefs: Prefs;
  minFont?: number;
  onPrefs: (prefs: Prefs) => void;
  onClose: () => void;
}) {
  // The colour scheme and spacing follow the person to every device; the
  // font size is this device's own.
  const person = useTerminalPrefs();
  const [custom] = usePref<unknown>(THEMES_KEY, NO_THEMES);
  const list = allTerminalThemes(custom);
  return (
    <Dialog id="settings-dialog" title={t("terminal.appearance")} onClose={onClose}>
      <label>
        {t("terminal.fontSize")}
        <input
          id="font-size"
          type="number"
          min={minFont}
          max={30}
          value={prefs.fontSize}
          onChange={(event) =>
            onPrefs({
              ...prefs,
              fontSize: Math.max(
                minFont,
                Math.min(30, Number(event.target.value) || 15),
              ),
            })
          }
        />
      </label>
      <label>
        {t("settings.workspace.lineHeight")}
        <select
          id="line-height"
          value={prefs.lineHeight}
          onChange={(event) => {
            onPrefs({ ...prefs, lineHeight: Number(event.target.value) });
            saveTerminalPrefs({ lineHeight: Number(event.target.value) });
          }}
        >
          {[
            [1, t("settings.workspace.compact")],
            [1.15, t("settings.workspace.comfortable")],
            [1.3, t("settings.workspace.spacious")],
          ].map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </select>
      </label>
      <label>
        {t("terminal.theme")}
        <select
          id="theme"
          value={prefs.theme}
          onChange={(event) => {
            onPrefs({ ...prefs, theme: event.target.value });
            saveTerminalPrefs({ theme: event.target.value });
          }}
        >
          <optgroup label={t("terminal.darkThemes")}>
            {list.filter((row) => !row.light).map((row) => <option key={row.id} value={row.id}>{row.name}</option>)}
          </optgroup>
          <optgroup label={t("terminal.lightThemes")}>
            {list.filter((row) => row.light).map((row) => <option key={row.id} value={row.id}>{row.name}</option>)}
          </optgroup>
        </select>
      </label>
      <label className="appearance-check">
        <input type="checkbox" checked={person.osc52} onChange={(event) => saveTerminalPrefs({ osc52: event.target.checked })} />
        {t("settings.workspace.osc52")}
      </label>
      <p>
        {t("terminal.appearanceHint")}{" "}
        <a href="/#targets" target="_top">{t("terminal.moreThemes")}</a>
      </p>
    </Dialog>
  );
}
export function HistoryDialog({
  url,
  shell,
  onClose,
}: {
  url: string;
  shell: boolean;
  onClose: () => void;
}) {
  const [data, setData] = useState<History>();
  const [query, setQuery] = useState("");
  const [index, setIndex] = useState(0);
  const [refresh, setRefresh] = useState(0);
  const [error, setError] = useState("");
  const container = useRef<HTMLPreElement>(null);
  useEffect(() => {
    const controller = new AbortController();
    setError("");
    void json<History>(url.replace("/term/", "/api/term/") + "/history", {
      signal: controller.signal,
    })
      .then(setData)
      .catch((error) => {
        if (!controller.signal.aborted) setError(errorMessage(error));
      });
    return () => controller.abort();
  }, [url, refresh]);
  const ranges = useMemo(() => {
    const text = data?.text || "",
      hay = text.toLowerCase(),
      needle = query.toLowerCase(),
      result: number[] = [];
    if (!needle) return result;
    let from = 0,
      at;
    while ((at = hay.indexOf(needle, from)) !== -1 && result.length < 2000) {
      result.push(at);
      from = at + query.length;
    }
    return result;
  }, [data, query]);
  useEffect(() => {
    container.current
      ?.querySelector("mark.current")
      ?.scrollIntoView({ block: "center" });
  }, [index, ranges]);
  function step(delta: number) {
    if (ranges.length)
      setIndex((old) => (old + delta + ranges.length) % ranges.length);
  }
  let from = 0;
  const highlighted: ReactNode[] = [];
  for (const [i, at] of ranges.entries()) {
    highlighted.push(
      (data?.text || "").slice(from, at),
      <mark key={at} className={i === index ? "current" : ""}>
        {data?.text.slice(at, at + query.length)}
      </mark>,
    );
    from = at + query.length;
  }
  highlighted.push(data?.text.slice(from) || "");
  return (
    <Dialog
      id="history-dialog"
      title="Session history"
      onClose={onClose}
      actions={
        <>
          <button
            id="history-refresh"
            onClick={() => setRefresh((old) => old + 1)}
          >
            Refresh
          </button>
          <button
            id="history-save"
            disabled={!data}
            onClick={() =>
              downloadBlob(
                new Blob([data?.text || ""], { type: "text/plain" }),
                "terminal-history.txt",
              )
            }
          >
            Download
          </button>
        </>
      }
    >
      <div className="history-controls">
        <input
          id="history-query"
          autoFocus
          placeholder="Search full tmux history"
          aria-label="Search full history"
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setIndex(0);
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter") step(event.shiftKey ? -1 : 1);
          }}
        />
        <button id="history-prev" onClick={() => step(-1)}>
          ↑
        </button>
        <button id="history-next" onClick={() => step(1)}>
          ↓
        </button>
        <span id="history-count">
          {query
            ? ranges.length
              ? `${index + 1} / ${ranges.length}${ranges.length === 2000 ? "+" : ""}`
              : "No matches"
            : ""}
        </span>
      </div>
      <p id="history-note">
        {error ||
          (!data
            ? "Loading history…"
            : `Snapshot of ${shell ? "companion shell" : "agent"} tmux history · up to ${data.limit_lines.toLocaleString()} retained lines${data.truncated ? " · limited to final 8 MiB" : ""}`)}
      </p>
      <pre ref={container} id="history-text">
        {highlighted}
      </pre>
    </Dialog>
  );
}
export function Desktop({
  info,
  onClose,
  onNotice,
}: {
  info: TerminalInfo;
  onClose: () => void;
  onNotice: (text: string) => void;
}) {
  return (
    <Dialog id="desktop-dialog" title="Open in your terminal" onClose={onClose}>
      <p>
        Attach to this same session in your default terminal. Closing either
        terminal leaves the session running.
      </p>
      <a id="desktop-open" className="button primary" href={info.desktop_uri}>
        Open in terminal
      </a>
      <h3>First-time setup</h3>
      <div className="desktop-platforms">
        <section>
          <span className="platform-label">Linux</span>
          <h3>Your default terminal</h3>
          <p>Download the launcher, then run:</p>
          <code>bash setup-lectern-terminal.sh</code>
          <a href="/desktop/setup-lectern-terminal.sh" download>
            Download Linux setup
          </a>
        </section>
        <section>
          <span className="platform-label">Windows</span>
          <h3>Your default terminal</h3>
          <p>Download the launcher, then run in PowerShell:</p>
          <code>
            powershell -ExecutionPolicy Bypass -File .\setup-lectern.ps1
          </code>
          <a href="/desktop/setup-lectern.ps1" download>
            Download Windows setup
          </a>
        </section>
      </div>
      <p>
        Both use your <code>lectern</code> SSH alias. Your terminal theme and
        existing sessions stay intact.
      </p>
      <a href="/desktop/README.txt" target="_blank" rel="noopener">
        Connection and setup instructions ↗
      </a>
      <details className="manual-connection">
        <summary>Manage Lectern entirely from a terminal</summary>
        <p>
          Install the terminal client, then run <code>lectern</code>.
          Sessions, tasks, routines, settings and context uploads are available
          without the web UI.
        </p>
        <p>
          <a href="/desktop/install-lectern-cli.sh" download>
            Linux client installer
          </a>{" "}
          ·{" "}
          <a href="/desktop/install-lectern-cli.ps1" download>
            Windows client installer
          </a>
        </p>
        <code id="cli-install-command">
          {/Win/i.test(navigator.platform)
            ? "powershell -ExecutionPolicy Bypass -File .\\install-lectern-cli.ps1 -Server lectern"
            : "bash install-lectern-cli.sh --server lectern --api " +
              quote(location.origin)}
        </code>
      </details>
      <details className="manual-connection">
        <summary>Connect from an already-open terminal</summary>
        <p>Run this in your terminal:</p>
        <textarea
          id="desktop-command"
          readOnly
          rows={3}
          aria-label="Manual SSH command"
          value={info.desktop_command}
        />
        <button
          id="desktop-copy"
          onClick={() => {
            void copyClipboard(info.desktop_command)
              .then(() => onNotice("SSH command copied."))
              .catch((error) => onNotice(errorMessage(error)));
          }}
        >
          Copy command
        </button>
      </details>
    </Dialog>
  );
}

// Snippets: tap one to send it, or manage the list.
// Quick commands for the key bar: this project's own first, then the ones
// kept for everywhere. Both lists live on the server (quick/commands.ts), so
// a reply saved on the phone is on the desk too.
export function Snippets({
  projectId,
  onSend,
  onClose,
}: {
  projectId: number | null;
  onSend: (command: QuickCommand) => void;
  onClose: () => void;
}) {
  const [globalRaw] = usePref<unknown>(GLOBAL_KEY, undefined);
  const [projectRaw] = usePref<unknown>(projectId ? projectKey(projectId) : "quick-commands:none", NO_THEMES);
  const everywhere = globalRaw === undefined ? readGlobal() : cleanQuickCommands(globalRaw);
  const project = projectId ? cleanQuickCommands(projectRaw) : [];
  const [text, setText] = useState(""),
    [enter, setEnter] = useState(true),
    [scope, setScope] = useState<"global" | "project">("global"),
    [editing, setEditing] = useState(false);
  const groups: { scope: QuickScope; title: string; rows: QuickCommand[] }[] = [
    ...(projectId ? [{ scope: { kind: "project", id: projectId } as QuickScope, title: t("quick.thisProject"), rows: project }] : []),
    { scope: { kind: "global" }, title: t("quick.everywhere"), rows: everywhere },
  ];
  const numbered = [...project, ...everywhere];
  return (
    <Dialog
      id="snippets-dialog"
      title={t("quick.title")}
      onClose={onClose}
      actions={
        <button id="snippets-edit" aria-pressed={editing} onClick={() => setEditing(!editing)}>
          {editing ? t("quick.done") : t("quick.edit")}
        </button>
      }
    >
      <div className="snippet-list">
        {groups.map((group) => (
          (group.rows.length > 0 || projectId) && (
            <div className="snippet-group" key={group.title}>
              {projectId && <h3 className="snippet-group-title">{group.title}</h3>}
              {group.rows.map((command) => {
                const number = numbered.indexOf(command) + 1;
                return (
                  <div className="snippet-row" key={command.id}>
                    <button
                      className="snippet-send"
                      disabled={editing}
                      title={number <= 9 ? t("quick.numberHint", { number }) : undefined}
                      onClick={() => {
                        onSend(command);
                        onClose();
                      }}
                    >
                      {command.label && <span className="snippet-label">{command.label}</span>}
                      <code>{command.text}</code>
                      {command.enter && <span aria-label={t("quick.thenEnter")}>⏎</span>}
                    </button>
                    {editing && (
                      <button
                        className="snippet-remove"
                        aria-label={t("settings.remove", { name: command.label || command.text })}
                        onClick={() => writeScope(group.scope, group.rows.filter((row) => row.id !== command.id))}
                      >
                        ×
                      </button>
                    )}
                  </div>
                );
              })}
              {projectId && !group.rows.length && <p className="snippet-empty">{t("quick.noneHere")}</p>}
            </div>
          )
        ))}
        {!numbered.length && <p>{t("quick.empty")}</p>}
      </div>
      <form
        className="snippet-add"
        onSubmit={(event) => {
          event.preventDefault();
          if (!text) return;
          const target: QuickScope = scope === "project" && projectId ? { kind: "project", id: projectId } : { kind: "global" };
          const rows = target.kind === "project" ? project : everywhere;
          writeScope(target, [...rows, { id: quickId(), label: "", text, enter }]);
          setText("");
        }}
      >
        <input
          id="snippet-text"
          aria-label={t("quick.new")}
          placeholder={t("quick.new")}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          value={text}
          onChange={(event) => setText(event.target.value)}
        />
        <label className="snippet-enter">
          <input type="checkbox" checked={enter} onChange={(event) => setEnter(event.target.checked)} />⏎
        </label>
        {projectId && (
          <select aria-label={t("quick.saveFor")} value={scope} onChange={(event) => setScope(event.target.value === "project" ? "project" : "global")}>
            <option value="global">{t("quick.everywhere")}</option>
            <option value="project">{t("quick.thisProject")}</option>
          </select>
        )}
        <button id="snippet-add" disabled={!text}>
          {t("settings.add")}
        </button>
      </form>
    </Dialog>
  );
}
