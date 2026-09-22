# Task: reset and summarise source health in librarr

Repository: librarr (Go). Build with `go build ./...`; tests with
`go test ./internal/search/ ./internal/api/`.

Each search source has a health record (`internal/search/health.go`,
`HealthTracker`): success/failure counters, failure streaks, a circuit that
opens after repeated failures, and a score. Today an operator can read it via
`GET /api/sources` but cannot clear it, and has to scan every source to see
how many are in trouble.

1. `func (h *HealthTracker) Reset(name string) bool` in `internal/search/health.go`:
   for a source the tracker has a record for, clear every counter and streak,
   close the circuit, clear the last error fields and restore the score to
   100, then return true. For a name it has no record for, return false and
   record nothing.
2. `POST /api/sources/{name}/reset`, registered in `registerCoreRoutes`
   next to `GET /api/sources`: resets that source's health. Responds
   `200` with the JSON object `{"name": <name>, "health": <that source's
   entry from Snapshot() after the reset>}`. Responds `404` with the usual
   JSON error shape when the name is not a configured source AND has no
   health record.
3. `GET /api/sources/summary`, registered in the same place: responds `200`
   with `{"total": N, "enabled": N, "circuit_open": N, "degraded": N,
   "healthy": N}` over the configured sources, where `circuit_open` counts
   sources whose circuit is currently open, `degraded` counts sources with a
   score below 50 that are not circuit-open, and `healthy` is the rest.
   A source with no health record yet counts as healthy.
4. Add both routes to the OpenAPI document served at `/api/openapi.json`.
5. Unit tests: `Reset` in `internal/search/health_test.go`; both handlers in a
   new test file in `internal/api/` built the way `settingsTestServer` builds
   a server.

Do not commit.
