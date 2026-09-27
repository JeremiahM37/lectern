// Pure helpers behind the searchable "Add agent" catalog. Kept out of
// AgentEditor.tsx so node:test can exercise them without a DOM.

export interface CatalogCapability {
  available: boolean;
  reason?: string;
}

export interface CatalogEntryLike {
  name: string;
  command: string;
  display_name: string;
  vendor?: string;
  group?: string;
  description?: string;
  capabilities?: Record<string, CatalogCapability>;
}

// Server group order (internal/sessions.CatalogGroups); anything unknown
// sorts after these rather than disappearing.
export const CATALOG_GROUPS = [
  "Popular",
  "Vendor agents",
  "Open source & community",
  "ACP adapters",
];

export interface CatalogSection<T> {
  group: string;
  items: T[];
}

// matches is a case-insensitive "every word appears somewhere" search over
// the fields an operator is likely to type: product, binary, vendor.
export function catalogMatches(entry: CatalogEntryLike, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = [entry.display_name, entry.name, entry.command, entry.vendor || "", entry.group || ""]
    .join(" ")
    .toLowerCase();
  return words.every((w) => hay.includes(w));
}

export function groupCatalog<T extends CatalogEntryLike>(entries: T[], query: string): CatalogSection<T>[] {
  const sections = new Map<string, T[]>();
  for (const entry of entries) {
    if (!catalogMatches(entry, query)) continue;
    const group = entry.group || "Other";
    if (!sections.has(group)) sections.set(group, []);
    sections.get(group)!.push(entry);
  }
  const rank = (g: string) => {
    const i = CATALOG_GROUPS.indexOf(g);
    return i < 0 ? CATALOG_GROUPS.length : i;
  };
  return [...sections.entries()]
    .sort(([a], [b]) => rank(a) - rank(b) || a.localeCompare(b))
    .map(([group, items]) => ({
      group,
      items: [...items].sort((a, b) => a.display_name.localeCompare(b.display_name)),
    }));
}

// The capabilities shown as chips on a catalog row, in a fixed order, with the
// server's own reason when one is missing — so an operator sees what an agent
// cannot do before adding it.
const CHIP_LABELS: [string, string][] = [
  ["resume", "Resume"],
  ["fork", "Fork"],
  ["model", "Model"],
  ["yolo", "Auto-approve"],
  ["task", "Tasks"],
  ["acp", "ACP"],
  ["mcp", "MCP"],
  ["skills", "Skills"],
];

export interface CapabilityChip {
  key: string;
  label: string;
  available: boolean;
  title: string;
}

export function capabilityChips(entry: CatalogEntryLike): CapabilityChip[] {
  const caps = entry.capabilities || {};
  return CHIP_LABELS.flatMap(([key, label]) => {
    const c = caps[key];
    if (!c) return [];
    return [{
      key,
      label,
      available: Boolean(c.available),
      title: c.reason || (c.available ? `${label} is supported` : `${label} isn't available`),
    }];
  });
}
