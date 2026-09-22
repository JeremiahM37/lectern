# Contributing to lectern

## Dev setup

```bash
npm ci --prefix frontend                          # Node 24
npm run build --prefix frontend
python3 frontend/scripts/stage.py                  # refresh Go embeds
go build ./...                                     # Go 1.25+
pip install pytest playwright pyte==0.8.2 && playwright install chromium   # for the e2e suite
```

## Running

```bash
LECTERN_MOCK=1 go run ./cmd/lectern serve    # fake agents, no infra needed
go run ./cmd/lectern serve               # real: needs ssh/git/tmux/claude on targets
```

## Tests

The Go suite is hermetic: the mock executor, a temp database per test, no network
and no real agent anywhere.

```bash
go test ./...                    # unit + API + real-git integration
go test ./internal/agents/       # fast, one package
pytest -q e2e                    # Playwright driving the real UI
```

- `internal/agents/`, `internal/policy/`, `internal/state/` — parsers, permission
  rules, the state machine
- `internal/api/*_test.go` — the full lifecycle against a real HTTP server and a
  scripted target
- `internal/worktree/realgit_test.go` — real `git worktree` through the local
  executor
- `e2e/` — Playwright against a built binary in mock mode

Add a test with any behaviour change. A new execution path should also be
exercised against a real target at least once — the mock suite has (twice) masked
bugs that only surfaced on real infrastructure (see `DESIGN.md` §10).

## Architecture

`DESIGN.md` is the source of truth — read §4 (architecture) and §3 (vocabulary)
first. Key seams to respect:

- **`internal/executor/`** — everything above it is target-kind agnostic. New
  target types (docker, k8s, …) implement the `Executor` interface and nothing
  else changes.
- **`internal/agents/`** — everything CLI-specific per coding agent. A new agent
  adds a launch-command builder and a stream parser here.
- **`internal/scheduler/`** — the run protocol itself: promote, tail, finalise.
  It never knows which CLI it is driving.
- **`internal/store/`** — plain SQL, typed accessors, no ORM.

## Conventions

- Explicit `.verify.yaml` + hermetic tests over mocking internals.
- Secrets never in the repo — config via environment (see `internal/config/`).
- React and strict TypeScript source lives in `frontend/src/`. Run the frontend
  build and staging script after edits, before rebuilding or testing Go.
  Generated assets, app/terminal HTML and the content-versioned service worker
  are embedded in the binary; deployment still needs only that binary.
- `npm run dev --prefix frontend` is a frontend-only development server.
  Use a staged Go build for API, authentication, terminal and offline checks.
