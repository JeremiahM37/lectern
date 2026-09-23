// Per-device favorites for the switch picker. Only a provider's identity is
// stored — never its environment or keys, which stay server-side.
export type Favorite = {
  agent: string;
  model: string;
  profile: number;
  label: string;
};

export const FAVORITES_KEY = 'lec-switch-favorites';

export const sameFavorite = (a: Favorite, b: Favorite) =>
  a.profile && b.profile
    ? a.profile === b.profile
    : a.profile === b.profile && a.agent === b.agent && a.model === b.model;

export function loadFavorites(): Favorite[] {
  try {
    const raw = JSON.parse(localStorage.getItem(FAVORITES_KEY) || '[]');
    if (!Array.isArray(raw)) return [];
    return raw
      .filter((row) => row && typeof row === 'object')
      .map((row) => ({
        agent: String(row.agent || ''),
        model: String(row.model || ''),
        profile: Number(row.profile) || 0,
        label: String(row.label || row.agent || ''),
      }))
      .filter((row) => row.agent || row.profile);
  } catch {
    return [];
  }
}

export function saveFavorites(list: Favorite[]): void {
  try {
    localStorage.setItem(FAVORITES_KEY, JSON.stringify(list));
  } catch {
    /* private mode or quota: favorites are a convenience, not state */
  }
}

export function toggleFavorite(list: Favorite[], entry: Favorite): Favorite[] {
  const existing = list.find((row) => sameFavorite(row, entry));
  if (existing) return list.filter((row) => row !== existing);
  return [...list, entry];
}
