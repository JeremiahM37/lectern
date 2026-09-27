// Every individual setting a person might search for, with the section it
// lives in. Settings search and the command palette both read this list; a
// hit opens the section and brings the control itself into view
// (focusSetting). Controls in the personal sections carry data-setting ids;
// older sections are found by their visible label.
import { t } from "../i18n";
import { SHORTCUTS } from "../shortcuts/registry";

export interface SettingEntry {
  id: string;
  section: string;
  sectionLabel: string;
  label: string;
  keywords: string;
  // The visible text to find when the control has no data-setting id.
  match?: string;
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

// [section, id, label key, keywords, visible label to match]
const rows: [string, string, string, string, string?][] = [
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
  ["machines", "machines.name", "", "target ssh add machine", "Machine name"],
  ["machines", "machines.host", "", "target ssh address", "Hostname"],
  ["machines", "machines.user", "", "ssh login", "SSH user"],
  ["machines", "machines.port", "", "ssh", "SSH port"],
  ["machines", "machines.key", "", "ssh identity private key", "SSH key path"],
  ["machines", "machines.connect", "", "mcp claude codex tools install", "Connect your AI tools"],
  ["machines", "machines.sshImport", "", "ssh config import alias hosts proxyjump bastion kerberos gssapi fido2 security key", "Import from ~/.ssh/config"],
  ["machines", "machines.sandboxes", "", "sandbox docker container fly modal vercel cloud provider suspend resume destroy", "Sandboxes"],
  ["machines", "machines.ports", "", "port forward forwarding localhost expose tunnel", "Ports"],
  ["machines", "machines.editor", "", "vs code vscode cursor zed windsurf editor open remote ssh download file folder", "Files & editor"],
  ["about", "usage.providers", "", "usage provider claude codex gemini quota limit 80% warning cost estimate account", "By provider"],
  ["projects", "projects.import", "", "repositories add scan", "Import projects"],
  ["projects", "projects.mcp", "", "mcp servers tools", "Project MCP servers"],
  ["projects", "projects.skills", "", "skills instructions", "Project skills"],
  ["projects", "projects.workflows", "", "workflows automation", "Project workflows"],
  ["projects", "projects.triggers", "", "triggers webhooks schedule", "Triggers"],
  ["projects", "projects.permission", "", "permission mode approvals", "New session permission mode"],
  ["notifications", "notifications.push", "", "push alerts phone browser", "This device"],
  ["notifications", "notifications.sessionAlerts", "", "waiting finished alerts", "Session alerts"],
  ["notifications", "notifications.sinks", "", "ntfy slack discord webhook", "Alerts"],
  ["devices", "devices.paired", "", "phone pair qr code", "Paired devices"],
  ["devices", "devices.relay", "", "encrypted relay remote", "Encrypted relay"],
  ["about", "about.spend", "", "cost money usage", "Spend"],
  ["about", "about.outcomes", "", "pull requests commits cost per outcome", "Outcomes"],
  ["about", "about.signedIn", "", "identity whoami login", "Signed in"],
  ["about", "about.build", "", "version build revision", "Running build"],
  ["budgets", "budgets.budgets", "", "spend limit cap", "Budgets"],
  ["budgets", "budgets.prices", "", "model token prices", "Model prices"],
  ["budgets", "budgets.limits", "", "usage limit rate limit continue", "When an agent hits its usage limit"],
  ["agents", "agents.runners", "", "custom agents providers", "Agent runners"],
  ["agents", "agents.profiles", "", "launch profiles accounts", "Launch profiles"],
  ["agents", "agents.starters", "", "starter profiles presets", "Starter profiles"],
  ["agents", "agents.menus", "", "menu visibility", "Show in menus"],
];

export function settingsIndex(options: { shortcuts?: boolean } = {}): SettingEntry[] {
  const out: SettingEntry[] = rows.map(([section, id, key, keywords, match]) => ({
    id,
    section,
    sectionLabel: sectionLabel(section),
    label: key ? t(key) : match!,
    keywords,
    match,
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
export function focusSetting(entry: Pick<SettingEntry, "id" | "match">, attempts = 20) {
  const root = document.querySelector(".settings-page");
  let target: Element | null = root?.querySelector(`[data-setting="${CSS.escape(entry.id)}"]`) || null;
  if (!target && entry.match && root) {
    const want = entry.match.toLocaleLowerCase();
    target = [...root.querySelectorAll("h3,h4,legend,label,summary,button,th")].find((node) => node.textContent?.trim().toLocaleLowerCase().startsWith(want)) || null;
  }
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
