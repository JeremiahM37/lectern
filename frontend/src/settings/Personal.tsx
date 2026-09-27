// The personal Settings sections: Appearance, Workspace & terminal, and
// Keyboard shortcuts. Everything here is stored per person on the server
// (prefs/store.ts), so it follows them to every device.
import { useEffect, useMemo, useRef, useState } from "react";
import type { Project } from "../types";
import { prefsLocalOnly, setPref, usePref, usePrefsState } from "../prefs/store";
import { t, useLocale, LANGUAGES } from "../i18n";
import { ACCENT_PRESETS, ZOOM_STEPS, themeTokens, resolveMode } from "../theme/app-theme";
import { saveAppearance, useAppearance } from "../theme/appearance";
import { allTerminalThemes, importTerminalTheme, type TerminalTheme } from "../theme/terminal-themes";
import { customThemes, saveCustomThemes, saveTerminalPrefs, THEMES_KEY, useTerminalPrefs } from "../theme/terminal-prefs";
import { cleanQuickCommands, quickId, readScope, scopeKey, writeScope, type QuickCommand, type QuickScope } from "../quick/commands";
import { cleanSavedLayouts } from "../workspace/layout";
import { browserReserved, displayChord, eventChord, isMac } from "../shortcuts/chords";
import { bindings, cleanOverrides, conflicts, conflictsFor, remap, SHORTCUTS, shortcutDef, warningFor, type Overrides } from "../shortcuts/registry";

const NOTHING: unknown[] = [];
const NO_OVERRIDES = {};

function SyncNote() {
  usePrefsState();
  return <p className="personal-note">{prefsLocalOnly() ? t("settings.localOnly") : t("settings.synced")}</p>;
}

// ---- Appearance ------------------------------------------------------------------

export function AppearancePanel() {
  useLocale();
  const appearance = useAppearance();
  const [custom, setCustom] = useState(appearance.accent || "#8b5cf6");
  const mode = resolveMode(appearance.theme, typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches);
  return (
    <section className="personal" aria-labelledby="appearance-heading">
      <h3 id="appearance-heading">{t("settings.section.appearance")}</h3>
      <SyncNote />
      <fieldset className="personal-row" data-setting="appearance.theme">
        <legend>{t("settings.appearance.theme")}</legend>
        <div className="segmented" role="radiogroup" aria-label={t("settings.appearance.theme")}>
          {(["system", "dark", "light"] as const).map((value) => (
            <label key={value} className={appearance.theme === value ? "on" : ""}>
              <input type="radio" name="app-theme" value={value} checked={appearance.theme === value} onChange={() => saveAppearance({ theme: value })} />
              {t("settings.appearance.theme." + value)}
            </label>
          ))}
        </div>
        <p className="personal-hint">{t("settings.appearance.themeHint", { mode: t("settings.appearance.theme." + mode) })}</p>
      </fieldset>
      <fieldset className="personal-row" data-setting="appearance.accent">
        <legend>{t("settings.appearance.accent")}</legend>
        <div className="swatches">
          {ACCENT_PRESETS.map((preset) => {
            const color = preset.value || themeTokens(mode).accent;
            return (
              <button key={preset.name} type="button" className="swatch" aria-pressed={appearance.accent === preset.value} aria-label={preset.name} title={preset.name}
                style={{ background: color }} onClick={() => saveAppearance({ accent: preset.value })} />
            );
          })}
          <label className="swatch-custom">
            <input type="color" value={custom} aria-label={t("settings.appearance.customAccent")} onChange={(event) => { setCustom(event.target.value); saveAppearance({ accent: event.target.value }); }} />
            {t("settings.appearance.custom")}
          </label>
        </div>
        <p className="personal-hint">{t("settings.appearance.accentHint")}</p>
      </fieldset>
      <fieldset className="personal-row" data-setting="appearance.zoom">
        <legend>{t("settings.appearance.zoom")}</legend>
        <div className="zoom-row">
          <button type="button" className="b" aria-label={t("shortcut.zoom.out", undefined, "Zoom out")} disabled={appearance.zoom <= ZOOM_STEPS[0]!} onClick={() => saveAppearance({ zoom: ZOOM_STEPS[Math.max(0, ZOOM_STEPS.indexOf(appearance.zoom) - 1)] ?? 1 })}>−</button>
          <select aria-label={t("settings.appearance.zoom")} value={appearance.zoom} onChange={(event) => saveAppearance({ zoom: Number(event.target.value) })}>
            {ZOOM_STEPS.map((step) => <option key={step} value={step}>{Math.round(step * 100)}%</option>)}
          </select>
          <button type="button" className="b" aria-label={t("shortcut.zoom.in", undefined, "Zoom in")} disabled={appearance.zoom >= ZOOM_STEPS.at(-1)!} onClick={() => saveAppearance({ zoom: ZOOM_STEPS[Math.min(ZOOM_STEPS.length - 1, ZOOM_STEPS.indexOf(appearance.zoom) + 1)] ?? 1 })}>+</button>
        </div>
      </fieldset>
      <label className="personal-row" data-setting="appearance.language">
        <span className="personal-label">{t("settings.appearance.language")}</span>
        <select value={appearance.language} onChange={(event) => saveAppearance({ language: event.target.value })}>
          {LANGUAGES.map((language) => <option key={language.tag} value={language.tag}>{language.tag ? language.name : t("settings.appearance.browserLanguage")}</option>)}
        </select>
        <span className="personal-hint">{t("settings.appearance.languageHint")}</span>
      </label>
    </section>
  );
}

// ---- Workspace & terminal ---------------------------------------------------------

function ThemeSwatch({ theme }: { theme: TerminalTheme }) {
  const colors = ["red", "green", "yellow", "blue", "magenta", "cyan"] as const;
  return (
    <span className="term-swatch" style={{ background: theme.theme.background, color: theme.theme.foreground }} aria-hidden="true">
      <b>Aa</b>
      {colors.map((name) => <i key={name} style={{ background: theme.theme[name] }} />)}
    </span>
  );
}

function QuickCommands({ projects }: { projects: Project[] }) {
  const [scopeValue, setScopeValue] = useState("global");
  const scope: QuickScope = scopeValue === "global" ? { kind: "global" } : { kind: "project", id: Number(scopeValue) };
  const [raw] = usePref<unknown>(scopeKey(scope), undefined);
  const commands = raw === undefined && scope.kind === "global" ? readScope(scope) : cleanQuickCommands(raw ?? []);
  const [label, setLabel] = useState(""),
    [text, setText] = useState(""),
    [enter, setEnter] = useState(true);
  const save = (next: QuickCommand[]) => writeScope(scope, next);
  const move = (index: number, delta: number) => {
    const next = [...commands];
    const [row] = next.splice(index, 1);
    next.splice(Math.max(0, Math.min(next.length, index + delta)), 0, row!);
    save(next);
  };
  return (
    <div className="personal-block" data-setting="workspace.quick">
      <h4>{t("settings.workspace.quick")}</h4>
      <p className="personal-hint">{t("settings.workspace.quickHint")}</p>
      <label className="personal-inline">
        {t("settings.workspace.quickScope")}
        <select value={scopeValue} onChange={(event) => setScopeValue(event.target.value)} aria-label={t("settings.workspace.quickScope")}>
          <option value="global">{t("settings.workspace.everywhere")}</option>
          {projects.map((project) => <option key={project.id} value={project.id}>{t("settings.workspace.projectScope", { name: project.name })}</option>)}
        </select>
      </label>
      <ol className="quick-list">
        {commands.map((command, index) => (
          <li key={command.id}>
            <span className="quick-number" aria-hidden="true">{index < 9 ? index + 1 : ""}</span>
            <input className="quick-label" aria-label={t("settings.workspace.quickLabel")} placeholder={t("settings.workspace.quickLabel")} value={command.label}
              onChange={(event) => save(commands.map((row) => (row.id === command.id ? { ...row, label: event.target.value } : row)))} />
            <input className="quick-text" aria-label={t("settings.workspace.quickText")} value={command.text}
              onChange={(event) => save(commands.map((row) => (row.id === command.id ? { ...row, text: event.target.value } : row)))} />
            <label className="quick-enter" title={t("settings.workspace.quickEnter")}>
              <input type="checkbox" checked={command.enter} onChange={(event) => save(commands.map((row) => (row.id === command.id ? { ...row, enter: event.target.checked } : row)))} />⏎
            </label>
            <button type="button" className="icon-button" aria-label={t("settings.moveUp", { name: command.label || command.text })} disabled={index === 0} onClick={() => move(index, -1)}>↑</button>
            <button type="button" className="icon-button" aria-label={t("settings.moveDown", { name: command.label || command.text })} disabled={index === commands.length - 1} onClick={() => move(index, 1)}>↓</button>
            <button type="button" className="icon-button danger" aria-label={t("settings.remove", { name: command.label || command.text })} onClick={() => save(commands.filter((row) => row.id !== command.id))}>×</button>
          </li>
        ))}
        {!commands.length && <li className="quick-empty">{t("settings.workspace.quickEmpty")}</li>}
      </ol>
      <form className="quick-add" onSubmit={(event) => {
        event.preventDefault();
        if (!text) return;
        save([...commands, { id: quickId(), label: label.trim(), text, enter }]);
        setLabel("");
        setText("");
      }}>
        <input aria-label={t("settings.workspace.quickNewLabel")} placeholder={t("settings.workspace.quickLabel")} value={label} onChange={(event) => setLabel(event.target.value)} />
        <input aria-label={t("settings.workspace.quickNewText")} placeholder={t("settings.workspace.quickText")} value={text} autoCapitalize="off" spellCheck={false} onChange={(event) => setText(event.target.value)} required />
        <label className="quick-enter"><input type="checkbox" checked={enter} onChange={(event) => setEnter(event.target.checked)} />⏎</label>
        <button className="b ok" disabled={!text}>{t("settings.add")}</button>
      </form>
    </div>
  );
}

function SavedLayouts({ projects }: { projects: Project[] }) {
  const [raw] = usePref<unknown>("workspace.layouts", NOTHING);
  const layouts = cleanSavedLayouts(raw);
  return (
    <div className="personal-block" data-setting="workspace.layouts">
      <h4>{t("settings.workspace.layouts")}</h4>
      <p className="personal-hint">{t("settings.workspace.layoutsHint")}</p>
      {!layouts.length && <p className="personal-empty">{t("workspace.noLayouts")}</p>}
      <ul className="layout-list">
        {layouts.map((row) => (
          <li key={row.id}>
            <input aria-label={t("workspace.layoutName")} value={row.name} onChange={(event) => setPref("workspace.layouts", layouts.map((other) => (other.id === row.id ? { ...other, name: event.target.value.slice(0, 80) } : other)) as never)} />
            <span className="personal-hint">{row.project_id ? projects.find((project) => project.id === row.project_id)?.name || t("workspace.projectOnly") : t("workspace.anyProject")} · {row.layout.panes.length} {t("workspace.panes")}</span>
            <button type="button" className="icon-button danger" aria-label={t("workspace.deleteLayout", { name: row.name })} onClick={() => setPref("workspace.layouts", layouts.filter((other) => other.id !== row.id) as never)}>×</button>
          </li>
        ))}
      </ul>
    </div>
  );
}

function TerminalSettings() {
  const prefs = useTerminalPrefs();
  const [customRaw] = usePref<unknown>(THEMES_KEY, NOTHING);
  const themes = allTerminalThemes(customRaw);
  const [paste, setPaste] = useState(""),
    [error, setError] = useState("");
  const current = prefs.theme || "slate";
  const add = (text: string, name: string) => {
    try {
      const theme = importTerminalTheme(text, name);
      const list = allTerminalThemes(customThemes()).filter((row) => row.custom && row.id !== theme.id);
      saveCustomThemes([...list, theme]);
      saveTerminalPrefs({ theme: theme.id });
      setError("");
      setPaste("");
    } catch (failure) {
      setError(failure instanceof Error ? failure.message : String(failure));
    }
  };
  return (
    <div className="personal-block">
      <h4>{t("settings.workspace.terminal")}</h4>
      <div className="personal-row" data-setting="workspace.terminalTheme">
        <span className="personal-label" id="terminal-theme-label">{t("settings.workspace.terminalTheme")}</span>
        <div className="term-themes" role="radiogroup" aria-labelledby="terminal-theme-label">
          {themes.map((theme) => (
            <label key={theme.id} className={"term-theme" + (theme.id === current ? " on" : "")}>
              <input type="radio" name="terminal-theme" checked={theme.id === current} onChange={() => saveTerminalPrefs({ theme: theme.id })} />
              <ThemeSwatch theme={theme} />
              <span>{theme.name}{theme.light ? " · " + t("settings.workspace.lightTheme") : ""}</span>
              {theme.custom && <button type="button" className="icon-button danger" aria-label={t("settings.remove", { name: theme.name })} onClick={(event) => { event.preventDefault(); saveCustomThemes(themes.filter((row) => row.custom && row.id !== theme.id)); if (current === theme.id) saveTerminalPrefs({ theme: "" }); }}>×</button>}
            </label>
          ))}
        </div>
      </div>
      <div className="personal-row" data-setting="workspace.themeImport">
        <span className="personal-label">{t("settings.workspace.themeImport")}</span>
        <p className="personal-hint">{t("settings.workspace.themeImportHint")}</p>
        <input type="file" aria-label={t("settings.workspace.themeFile")} accept=".json,.itermcolors,.conf,.toml,.ini,.Xresources,.theme,text/*"
          onChange={(event) => {
            const file = event.target.files?.[0];
            event.target.value = "";
            if (file) void file.text().then((text) => add(text, file.name.replace(/\.[^.]+$/, "")));
          }} />
        <textarea aria-label={t("settings.workspace.themePaste")} placeholder={t("settings.workspace.themePaste")} rows={3} value={paste} onChange={(event) => setPaste(event.target.value)} />
        <button type="button" className="b" disabled={!paste.trim()} onClick={() => add(paste, "Imported")}>{t("settings.workspace.themeAdd")}</button>
        {error && <p role="alert" className="personal-error">{error}</p>}
      </div>
      <label className="personal-row" data-setting="workspace.lineHeight">
        <span className="personal-label">{t("settings.workspace.lineHeight")}</span>
        <select value={prefs.lineHeight} onChange={(event) => saveTerminalPrefs({ lineHeight: Number(event.target.value) })}>
          <option value={0}>{t("settings.workspace.lineHeightDevice")}</option>
          <option value={1}>{t("settings.workspace.compact")}</option>
          <option value={1.15}>{t("settings.workspace.comfortable")}</option>
          <option value={1.3}>{t("settings.workspace.spacious")}</option>
        </select>
      </label>
      <label className="personal-row personal-check" data-setting="workspace.osc52">
        <input type="checkbox" checked={prefs.osc52} onChange={(event) => saveTerminalPrefs({ osc52: event.target.checked })} />
        <span><span className="personal-label">{t("settings.workspace.osc52")}</span><span className="personal-hint">{t("settings.workspace.osc52Hint")}</span></span>
      </label>
      <fieldset className="personal-row" data-setting="workspace.find">
        <legend>{t("settings.workspace.find")}</legend>
        {(["findCase", "findWord", "findRegex"] as const).map((key) => (
          <label key={key} className="personal-check">
            <input type="checkbox" checked={prefs[key]} onChange={(event) => saveTerminalPrefs({ [key]: event.target.checked })} />
            {t("settings.workspace." + key)}
          </label>
        ))}
      </fieldset>
      <p className="personal-hint">{t("settings.workspace.kitty")}</p>
    </div>
  );
}

export function WorkspacePanel({ projects }: { projects: Project[] }) {
  useLocale();
  return (
    <section className="personal" aria-labelledby="workspace-heading">
      <h3 id="workspace-heading">{t("settings.section.workspace")}</h3>
      <SyncNote />
      <SavedLayouts projects={projects} />
      <QuickCommands projects={projects} />
      <TerminalSettings />
    </section>
  );
}

// ---- Keyboard shortcuts -----------------------------------------------------------

function Recorder({ id, current, onDone }: { id: string; current: Map<string, string[]>; onDone: (chord: string | null) => void }) {
  const [chord, setChord] = useState<string | null>(null);
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => box.current?.focus(), []);
  const clashes = chord ? conflictsFor(id, chord, current) : [];
  const context = shortcutDef(id)?.context || "global";
  return (
    <div className="shortcut-recorder" data-shortcut-capture ref={box} tabIndex={0} role="group" aria-label={t("settings.shortcuts.recording")}
      onKeyDown={(event) => {
        if (event.key === "Tab") return;
        event.preventDefault();
        event.stopPropagation();
        if (event.key === "Escape" && !event.ctrlKey && !event.altKey && !event.metaKey && !event.shiftKey && chord === null) return onDone(null);
        const next = eventChord(event.nativeEvent);
        if (next) setChord(next);
      }}>
      <span className="shortcut-live">{chord ? <kbd>{displayChord(chord)}</kbd> : t("settings.shortcuts.pressKeys")}</span>
      {chord && clashes.length > 0 && (
        <p className="shortcut-conflict" role="alert">{t("settings.shortcuts.conflict", { names: clashes.map((other) => t("shortcut." + other, undefined, shortcutDef(other)?.title)).join(", ") })}</p>
      )}
      {chord && warningFor(chord, context) && <p className="personal-hint">{warningFor(chord, context)}</p>}
      <div className="shortcut-recorder-actions">
        <button type="button" className="b ok" disabled={!chord} onClick={() => onDone(chord)}>{clashes.length ? t("settings.shortcuts.saveAnyway") : t("settings.save")}</button>
        <button type="button" className="b" onClick={() => onDone(null)}>{t("settings.cancel")}</button>
      </div>
    </div>
  );
}

export function ShortcutsPanel({ focus }: { focus?: string }) {
  useLocale();
  const mac = isMac();
  const [raw] = usePref<unknown>("shortcuts", NO_OVERRIDES);
  const overrides = useMemo(() => cleanOverrides(raw, mac), [raw, mac]);
  const current = useMemo(() => bindings(overrides, mac), [overrides, mac]);
  const clashes = useMemo(() => conflicts(current), [current]);
  const [query, setQuery] = useState(""),
    [recording, setRecording] = useState<string | null>(null);
  const save = (next: Overrides) => setPref("shortcuts", next as never);
  useEffect(() => {
    if (!focus?.startsWith("shortcut:")) return;
    setQuery(t("shortcut." + focus.slice(9), undefined, shortcutDef(focus.slice(9))?.title));
  }, [focus]);
  const q = query.trim().toLocaleLowerCase();
  const rows = SHORTCUTS.filter((row) => {
    if (!q) return true;
    const chords = (current.get(row.id) || []).map((chord) => displayChord(chord, mac)).join(" ");
    return [t("shortcut." + row.id, undefined, row.title), row.category, row.keywords, chords, row.id].join(" ").toLocaleLowerCase().includes(q);
  });
  const categories = [...new Set(rows.map((row) => row.category))];
  const assigned = SHORTCUTS.filter((row) => (current.get(row.id) || []).length).length;
  return (
    <section className="personal shortcuts-panel" aria-labelledby="shortcuts-heading" data-setting="shortcuts.list">
      <h3 id="shortcuts-heading">{t("settings.shortcuts.title")}</h3>
      <SyncNote />
      <p className="personal-hint">{t("settings.shortcuts.summary", { total: SHORTCUTS.length, assigned })}</p>
      <div className="shortcut-tools">
        <input type="search" aria-label={t("settings.shortcuts.search")} placeholder={t("settings.shortcuts.search")} value={query} onChange={(event) => setQuery(event.target.value)} />
        <button type="button" className="b" disabled={!Object.keys(overrides).length} onClick={() => save({})}>{t("settings.shortcuts.resetAll")}</button>
      </div>
      {clashes.length > 0 && (
        <div className="shortcut-conflicts" role="alert">
          <b>{t("settings.shortcuts.conflicts")}</b>
          <ul>{clashes.map((row) => <li key={row.chord}><kbd>{displayChord(row.chord, mac)}</kbd> — {row.ids.map((id) => t("shortcut." + id, undefined, shortcutDef(id)?.title)).join(", ")}</li>)}</ul>
        </div>
      )}
      {categories.map((category) => (
        <div key={category} className="shortcut-group">
          <h4>{t("settings.shortcuts.category." + category, undefined, category)}</h4>
          <table className="shortcut-table">
            <tbody>
              {rows.filter((row) => row.category === category).map((row) => {
                const chords = current.get(row.id) || [];
                const changed = row.id in overrides;
                const clash = clashes.some((c) => c.ids.includes(row.id));
                return (
                  <tr key={row.id} data-shortcut={row.id} className={clash ? "clash" : ""}>
                    <th scope="row">
                      {t("shortcut." + row.id, undefined, row.title)}
                      {row.context === "terminal" && <span className="shortcut-context">{t("settings.shortcuts.inTerminal")}</span>}
                    </th>
                    <td className="shortcut-chords">
                      {chords.map((chord) => (
                        <span key={chord} className={"shortcut-chord" + (browserReserved(chord) ? " reserved" : "")} title={warningFor(chord, row.context)}>
                          <kbd>{displayChord(chord, mac)}</kbd>
                          <button type="button" aria-label={t("settings.shortcuts.removeChord", { chord: displayChord(chord, mac), name: t("shortcut." + row.id, undefined, row.title) })}
                            onClick={() => save(remap(overrides, row.id, chords.filter((other) => other !== chord), mac))}>×</button>
                        </span>
                      ))}
                      {!chords.length && <span className="shortcut-none">{t("settings.shortcuts.unassigned")}</span>}
                      {recording === row.id && (
                        <Recorder id={row.id} current={current} onDone={(chord) => {
                          setRecording(null);
                          if (chord) save(remap(overrides, row.id, [...chords.filter((other) => other !== chord), chord].slice(-4), mac));
                        }} />
                      )}
                    </td>
                    <td className="shortcut-actions">
                      <button type="button" className="b" aria-label={t("settings.shortcuts.add", { name: t("shortcut." + row.id, undefined, row.title) })} onClick={() => setRecording(row.id)}>{t("settings.shortcuts.addShort")}</button>
                      {changed && (
                        <button type="button" className="b" aria-label={t("settings.shortcuts.reset", { name: t("shortcut." + row.id, undefined, row.title) })}
                          onClick={() => save(remap(overrides, row.id, row.defaults, mac))}>{t("settings.shortcuts.resetShort")}</button>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      ))}
      {!rows.length && <p className="personal-empty">{t("settings.shortcuts.noMatch")}</p>}
    </section>
  );
}

