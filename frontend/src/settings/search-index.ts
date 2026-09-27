// Every individual setting a person might search for, with the section it
// lives in. Settings search and the command palette both read this list; a
// hit opens the section and brings the control itself into view
// (focusSetting), which carries a matching data-setting id.
import { t } from "../i18n";
import { SHORTCUTS } from "../shortcuts/registry";

export interface SettingEntry {
  id: string;
  section: string;
  sectionLabel: string;
  label: string;
  keywords: string;
  kind?: "setting" | "shortcut";
}

export const SECTIONS: [string, string][] = [
  ["machines", "settings.section.machines"],
  ["projects", "settings.section.projects"],
  ["notifications", "settings.section.notifications"],
  ["devices", "settings.section.devices"],
  ["about", "settings.section.about"],
  ["budgets", "settings.section.budgets"],
  ["accounts", "settings.section.accounts"],
  ["agents", "settings.section.agents"],
  ["appearance", "settings.section.appearance"],
  ["workspace", "settings.section.workspace"],
  ["shortcuts", "settings.section.shortcuts"],
];
export const sectionLabel = (name: string) => t(SECTIONS.find(([key]) => key === name)?.[1] || name);

// [section, id, label key, keywords]
const rows: [string, string, string, string][] = [
  ["appearance", "appearance.theme", "settings.appearance.theme", "dark light mode system colour scheme night"],
  ["appearance", "appearance.accent", "settings.appearance.accent", "color colour brand highlight"],
  ["appearance", "appearance.zoom", "settings.appearance.zoom", "scale size bigger smaller text"],
  ["appearance", "appearance.language", "settings.appearance.language", "locale translation i18n"],
  ["workspace", "workspace.layouts", "settings.workspace.layouts", "saved named layouts panes splits restore"],
  ["workspace", "workspace.quick", "settings.workspace.quick", "snippets saved replies commands keybar"],
  ["workspace", "workspace.terminalTheme", "settings.workspace.terminalTheme", "terminal colours palette theme scheme"],
  ["workspace", "workspace.themeImport", "settings.workspace.themeImport", "iterm windows terminal kitty ghostty alacritty xresources import"],
  ["workspace", "workspace.lineHeight", "settings.workspace.lineHeight", "terminal line spacing height"],
  ["workspace", "workspace.osc52", "settings.workspace.osc52", "clipboard copy osc 52 programs tmux vim"],
  ["workspace", "workspace.find", "settings.workspace.find", "search find regex case sensitive terminal"],
  ["shortcuts", "shortcuts.list", "settings.shortcuts.title", "keyboard keys bindings remap hotkeys"],
  ["machines", "machines.name", "settings.index.machines.name", "target ssh add machine"],
  ["machines", "machines.host", "settings.index.machines.host", "target ssh address"],
  ["machines", "machines.user", "settings.index.machines.user", "ssh login"],
  ["machines", "machines.port", "settings.index.machines.port", "ssh"],
  ["machines", "machines.key", "settings.index.machines.key", "ssh identity private key"],
  ["machines", "machines.connect", "settings.index.machines.connect", "mcp claude codex tools install"],
  ["projects", "projects.import", "settings.index.projects.import", "repositories add scan"],
  ["projects", "projects.mcp", "settings.index.projects.mcp", "mcp servers tools"],
  ["projects", "projects.skills", "settings.index.projects.skills", "skills instructions"],
  ["projects", "projects.workflows", "settings.index.projects.workflows", "workflows automation"],
  ["projects", "projects.triggers", "settings.index.projects.triggers", "triggers webhooks schedule"],
  ["projects", "projects.permission", "settings.index.projects.permission", "permission mode approvals"],
  ["notifications", "notifications.push", "settings.index.notifications.push", "push alerts phone browser"],
  ["notifications", "notifications.sessionAlerts", "settings.index.notifications.sessionAlerts", "waiting finished alerts"],
  ["notifications", "notifications.sinks", "settings.index.notifications.sinks", "ntfy slack discord webhook"],
  ["devices", "devices.paired", "settings.index.devices.paired", "phone pair qr code"],
  ["devices", "devices.relay", "settings.index.devices.relay", "encrypted relay remote"],
  ["about", "about.spend", "settings.index.about.spend", "cost money usage"],
  ["about", "about.outcomes", "settings.index.about.outcomes", "pull requests commits cost per outcome"],
  ["about", "about.signedIn", "settings.index.about.signedIn", "identity whoami login"],
  ["about", "about.build", "settings.index.about.build", "version build revision"],
  ["budgets", "budgets.budgets", "settings.index.budgets.budgets", "spend limit cap"],
  ["budgets", "budgets.prices", "settings.index.budgets.prices", "model token prices"],
  ["budgets", "budgets.limits", "settings.index.budgets.limits", "usage limit rate limit continue"],
  ["agents", "agents.runners", "settings.index.agents.runners", "custom agents providers"],
  ["agents", "agents.profiles", "settings.index.agents.profiles", "launch profiles accounts"],
  ["agents", "agents.starters", "settings.index.agents.starters", "starter profiles presets"],
  ["agents", "agents.menus", "settings.index.agents.menus", "menu visibility"],
];

export function settingsIndex(options: { shortcuts?: boolean } = {}): SettingEntry[] {
  const out: SettingEntry[] = rows.map(([section, id, key, keywords]) => ({
    id,
    section,
    sectionLabel: sectionLabel(section),
    label: t(key),
    // The keywords stay English, so English terms find a control in any language.
    keywords,
    kind: "setting",
  }));
  if (options.shortcuts)
    for (const row of SHORTCUTS)
      out.push({
        id: "shortcut:" + row.id,
        section: "shortcuts",
        sectionLabel: sectionLabel("shortcuts"),
        label: t("shortcut." + row.id, undefined, row.title),
        keywords: [row.category, row.keywords, "shortcut key"].join(" "),
        kind: "shortcut",
      });
  return out;
}

export function searchSettings(query: string, entries = settingsIndex({ shortcuts: true })): SettingEntry[] {
  const terms = query.toLocaleLowerCase().split(/\s+/).filter(Boolean);
  if (!terms.length) return [];
  return entries
    .map((entry, order) => {
      const label = entry.label.toLocaleLowerCase();
      const haystack = [label, entry.keywords, entry.sectionLabel].join(" ").toLocaleLowerCase();
      if (!terms.every((term) => haystack.includes(term))) return null;
      const score = (label.startsWith(terms[0]!) ? 4 : 0) + (terms.every((term) => label.includes(term)) ? 2 : 0) + (entry.kind === "shortcut" ? 0 : 1);
      return { entry, order, score };
    })
    .filter((row): row is { entry: SettingEntry; order: number; score: number } => !!row)
    .sort((a, b) => b.score - a.score || a.order - b.order)
    .map((row) => row.entry);
}

// Scrolls the control into view, marks it briefly and focuses its input.
export function focusSetting(entry: Pick<SettingEntry, "id">, attempts = 20) {
  const target = document.querySelector(`[data-setting="${CSS.escape(entry.id)}"]`);
  if (!target) {
    if (attempts > 0) setTimeout(() => focusSetting(entry, attempts - 1), 100);
    return false;
  }
  target.scrollIntoView({ block: "center" });
  target.classList.add("setting-hit");
  setTimeout(() => target!.classList.remove("setting-hit"), 2400);
  const input = target.matches("input,select,textarea,button") ? target : target.querySelector("input,select,textarea,button") || (target.tagName === "LABEL" ? (target as HTMLLabelElement).control : null);
  (input as HTMLElement | null)?.focus({ preventScroll: true });
  return true;
}
