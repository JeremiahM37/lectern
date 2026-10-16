// Favorites are the sessions a person has starred. The star lives on the
// session, so the same set shows in the terminal client's Favorites tab and in
// the web list's Favorites filter.

export type SessionScope = "active" | "all" | "archived" | "favorites";

// The card's star says what pressing it does: add, or remove when starred.
export function favoriteLabelKey(starred: boolean): string {
  return starred ? "sessions.card.removeFavorite" : "sessions.card.addFavorite";
}

// The Favorites filter keeps only starred sessions; every other scope is
// untouched by it.
export function matchesFavoriteScope(session: { favorite?: boolean }, scope: SessionScope): boolean {
  return scope !== "favorites" || session.favorite === true;
}

// The Favorites filter lists ended favorites too, so it must ask the server
// for every session, as the "all" scope does.
export function fetchesEndedSessions(scope: SessionScope): boolean {
  return scope === "all" || scope === "favorites";
}
