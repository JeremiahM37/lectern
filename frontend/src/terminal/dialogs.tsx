import { errorMessage } from "./model";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { t, useLocale } from "../i18n";
import { usePref } from "../prefs/store";
import { allTerminalThemes } from "../theme/terminal-themes";
import { saveTerminalPrefs, THEMES_KEY, useTerminalPrefs } from "../theme/terminal-prefs";
import { cleanQuickCommands, GLOBAL_KEY, pluginCommands, projectKey, quickId, readGlobal, writeScope, type QuickCommand, type QuickScope } from "../quick/commands";
import { usePluginContributions } from "../plugins/contributions";
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
  useLocale();
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
          {t("terminal.close")}
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
  useLocale();
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
        <a href="/#settings/workspace" target="_top">{t("terminal.moreThemes")}</a>
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
  useLocale();
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
      title={t("terminalPage.history.title")}
      onClose={onClose}
      actions={
        <>
          <button
            id="history-refresh"
            onClick={() => setRefresh((old) => old + 1)}
          >
            {t("terminalPage.common.refresh")}
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
            {t("terminalPage.common.download")}
          </button>
        </>
      }
    >
      <div className="history-controls">
        <input
          id="history-query"
          autoFocus
          placeholder={t("terminalPage.history.placeholder")}
          aria-label={t("terminalPage.history.searchLabel")}
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
              : t("terminal.noMatches")
            : ""}
        </span>
      </div>
      <p id="history-note">
        {error ||
          (!data
            ? t("terminalPage.history.loading")
            : t(shell ? "terminalPage.history.snapshotShell" : "terminalPage.history.snapshotAgent", { lines: data.limit_lines.toLocaleString() }) +
              (data.truncated ? t("terminalPage.history.truncated") : ""))}
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
  useLocale();
  return (
    <Dialog id="desktop-dialog" title={t("terminalPage.desktop.title")} onClose={onClose}>
      <p>
        {t("terminalPage.desktop.intro")}
      </p>
      <a id="desktop-open" className="button primary" href={info.desktop_uri}>
        {t("terminalPage.tools.openInTerminal")}
      </a>
      <h3>{t("terminalPage.desktop.firstTime")}</h3>
      <div className="desktop-platforms">
        <section>
          <span className="platform-label">Linux</span>
          <h3>{t("terminalPage.desktop.defaultTerminal")}</h3>
          <p>{t("terminalPage.desktop.runLinux")}</p>
          <code>bash setup-lectern-terminal.sh</code>
          <a href="/desktop/setup-lectern-terminal.sh" download>
            {t("terminalPage.desktop.downloadLinux")}
          </a>
        </section>
        <section>
          <span className="platform-label">Windows</span>
          <h3>{t("terminalPage.desktop.defaultTerminal")}</h3>
          <p>{t("terminalPage.desktop.runWindows")}</p>
          <code>
            powershell -ExecutionPolicy Bypass -File .\setup-lectern.ps1
          </code>
          <a href="/desktop/setup-lectern.ps1" download>
            {t("terminalPage.desktop.downloadWindows")}
          </a>
        </section>
      </div>
      <p>
        {t("terminalPage.desktop.aliasBefore")} <code>lectern</code> {t("terminalPage.desktop.aliasAfter")}
      </p>
      <a href="/desktop/README.txt" target="_blank" rel="noopener">
        {t("terminalPage.desktop.instructions")}
      </a>
      <details className="manual-connection">
        <summary>{t("terminalPage.desktop.cliSummary")}</summary>
        <p>
          {t("terminalPage.desktop.cliBefore")} <code>lectern</code>{t("terminalPage.desktop.cliAfter")}
        </p>
        <p>
          <a href="/desktop/install-lectern-cli.sh" download>
            {t("terminalPage.desktop.linuxInstaller")}
          </a>{" "}
          ·{" "}
          <a href="/desktop/install-lectern-cli.ps1" download>
            {t("terminalPage.desktop.windowsInstaller")}
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
        <summary>{t("terminalPage.desktop.manualSummary")}</summary>
        <p>{t("terminalPage.desktop.runThis")}</p>
        <textarea
          id="desktop-command"
          readOnly
          rows={3}
          aria-label={t("terminalPage.desktop.manualLabel")}
          value={info.desktop_command}
        />
        <button
          id="desktop-copy"
          onClick={() => {
            void copyClipboard(info.desktop_command)
              .then(() => onNotice(t("terminalPage.desktop.copied")))
              .catch((error) => onNotice(errorMessage(error)));
          }}
        >
          {t("terminalPage.desktop.copyCommand")}
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
  useLocale();
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
  const pluginUI = usePluginContributions();
  const fromPlugins = useMemo(() => pluginCommands(projectId).map((row) => row.command), [pluginUI, projectId]);
  const numbered = [...project, ...everywhere, ...fromPlugins];
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
        {fromPlugins.length > 0 && (
          <div className="snippet-group snippet-plugins">
            <h3 className="snippet-group-title">{t("quick.fromPlugins")}</h3>
            {fromPlugins.map((command) => {
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
                </div>
              );
            })}
          </div>
        )}
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
