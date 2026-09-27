// Extra result sources for the command palette, asked as the person types —
// files in the active workspace, for instance. A provider returns commands
// like any other; the palette ranks them with the rest.
import type { Command } from "./Palette";

export interface PaletteProvider {
  id: string;
  // Called with the typed query (at least two characters). Resolve to [] when
  // there is nothing to offer; a rejection is ignored.
  search(query: string, signal: AbortSignal): Promise<Command[]>;
}

const providers = new Map<string, PaletteProvider>();

export function registerPaletteProvider(provider: PaletteProvider) {
  providers.set(provider.id, provider);
  return () => {
    if (providers.get(provider.id) === provider) providers.delete(provider.id);
  };
}

export async function searchProviders(query: string, signal: AbortSignal): Promise<Command[]> {
  if (query.trim().length < 2) return [];
  const results = await Promise.allSettled([...providers.values()].map((provider) => provider.search(query, signal)));
  return results.flatMap((result) => (result.status === "fulfilled" ? result.value : []));
}
