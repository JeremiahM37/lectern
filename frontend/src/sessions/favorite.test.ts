import assert from "node:assert/strict";
import { test } from "node:test";
import { favoriteLabelKey, fetchesEndedSessions, matchesFavoriteScope } from "./favorite";

test("the star says what pressing it does", () => {
  assert.equal(favoriteLabelKey(false), "sessions.card.addFavorite");
  assert.equal(favoriteLabelKey(true), "sessions.card.removeFavorite");
});

test("the Favorites filter keeps only starred sessions, and leaves other scopes alone", () => {
  assert.equal(matchesFavoriteScope({ favorite: true }, "favorites"), true);
  assert.equal(matchesFavoriteScope({ favorite: false }, "favorites"), false);
  assert.equal(matchesFavoriteScope({}, "favorites"), false);
  assert.equal(matchesFavoriteScope({}, "active"), true);
  assert.equal(matchesFavoriteScope({ favorite: false }, "all"), true);
});

test("the Favorites filter fetches ended sessions, because a favorite can have ended", () => {
  assert.equal(fetchesEndedSessions("favorites"), true);
  assert.equal(fetchesEndedSessions("all"), true);
  assert.equal(fetchesEndedSessions("active"), false);
  assert.equal(fetchesEndedSessions("archived"), false);
});
