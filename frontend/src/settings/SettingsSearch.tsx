// The search box at the top of Settings: finds individual controls — "accent",
// "ssh port", "split right" — across every section, and takes you to the
// control itself.
import { useEffect, useRef, useState } from "react";
import { t, useLocale } from "../i18n";
import { searchSettings, type SettingEntry } from "./search-index";

export function SettingsSearch({ onPick, focusVersion }: { onPick: (entry: SettingEntry) => void; focusVersion?: number }) {
  useLocale();
  const [query, setQuery] = useState(""),
    [selected, setSelected] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (focusVersion) input.current?.focus();
  }, [focusVersion]);
  const results = searchSettings(query).slice(0, 12);
  const pick = (entry: SettingEntry | undefined) => {
    if (!entry) return;
    setQuery("");
    onPick(entry);
  };
  return (
    <div className="settings-search" role="search">
      <input ref={input} id="settings-search" type="search" placeholder={t("settings.search")} aria-label={t("settings.search")} autoComplete="off"
        role="combobox" aria-expanded={results.length > 0} aria-controls="settings-search-results"
        aria-activedescendant={results.length ? `settings-hit-${selected}` : undefined}
        value={query} onChange={(event) => { setQuery(event.target.value); setSelected(0); }}
        onKeyDown={(event) => {
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            setSelected((old) => Math.max(0, Math.min(results.length - 1, old + (event.key === "ArrowDown" ? 1 : -1))));
          } else if (event.key === "Enter") {
            event.preventDefault();
            pick(results[selected]);
          } else if (event.key === "Escape") setQuery("");
        }} />
      {query.trim() && (
        <div id="settings-search-results" role="listbox" aria-label={t("settings.searchResults")}>
          {results.map((entry, index) => (
            <div key={entry.id} id={`settings-hit-${index}`} role="option" aria-selected={index === selected} className="settings-hit"
              onPointerDown={(event) => event.preventDefault()} onClick={() => pick(entry)}>
              <strong>{entry.label}</strong>
              <span>{entry.sectionLabel}{entry.kind === "shortcut" ? " · " + t("settings.searchShortcut") : ""}</span>
            </div>
          ))}
          {!results.length && <p className="settings-hit-empty">{t("settings.searchNone")}</p>}
        </div>
      )}
    </div>
  );
}
