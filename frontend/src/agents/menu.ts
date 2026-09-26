// Shared "shown agents" menu logic for every agent picker (new session,
// switch, task create/quick-dispatch, best-of-N, delegate, Board filters).
// The registry (GET /api/agents) can grow to a dozen-plus entries once an
// operator has added a few catalog presets — this is what keeps a picker a
// short list instead of a long scroll, per Settings → Agents' "Show in
// menus" toggle and drag/up-down order (GET/PUT /api/agents/menu).
export interface AgentMenuApi {
  request<T>(path: string, opts?: { method?: string; body?: unknown }): Promise<T>;
}

export interface AgentLike {
  name: string;
}

// fetchAgentMenu reads the saved shown/ordered agent list. A failure (the
// endpoint being briefly unreachable, or a fresh install with nothing saved
// yet) degrades to an empty list rather than throwing — splitAgentMenu's own
// fallback then shows every agent, which is exactly today's pre-menu
// behavior, never a picker that looks broken.
export async function fetchAgentMenu(api: AgentMenuApi): Promise<string[]> {
  try {
    const res = await api.request<{ agents?: string[] }>("/agents/menu");
    return Array.isArray(res.agents) ? res.agents : [];
  } catch {
    return [];
  }
}

export async function saveAgentMenu(api: AgentMenuApi, agents: string[]): Promise<string[]> {
  const res = await api.request<{ agents: string[] }>("/agents/menu", {
    method: "PUT",
    body: { agents },
  });
  return Array.isArray(res.agents) ? res.agents : agents;
}

// splitAgentMenu partitions the full agent list into what a picker renders
// directly (in the saved order) and everything else, which belongs behind a
// "More agents…" entry instead of growing the picker. When shown is empty —
// nothing saved yet, or the menu fetch failed — every agent is treated as
// shown, so a fresh install or a flaky request never hides an agent nobody
// asked to hide.
export function splitAgentMenu<T extends AgentLike>(
  all: T[],
  shown: string[],
): { shown: T[]; more: T[] } {
  if (shown.length === 0) return { shown: all, more: [] };
  const byName = new Map(all.map((a) => [a.name, a] as const));
  const seen = new Set<string>();
  const shownList: T[] = [];
  for (const name of shown) {
    const a = byName.get(name);
    if (a && !seen.has(name)) {
      shownList.push(a);
      seen.add(name);
    }
  }
  if (shownList.length === 0) return { shown: all, more: [] };
  const more = all.filter((a) => !seen.has(a.name));
  return { shown: shownList, more };
}
