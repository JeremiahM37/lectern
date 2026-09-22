# Task: a `sessions` subcommand for the lectern CLI

Repository: lectern (Go). Build with `go build ./...`; unit tests with
`go test ./cmd/... ./internal/console/`. (Some tests in other packages print
"real-process test requires the reviewed isolated runner" on this machine; that
is expected and not something to fix.)

The CLI can attach to sessions but cannot list them. Add `lectern sessions`:

1. In `cmd/lectern`, a function
   `sessionsCommand(c *console.Client, args []string) ([]byte, error)`
   that fetches `GET /api/sessions` through the client and returns the bytes
   to print:
   - `lectern sessions --json` returns the API response body unchanged.
   - `lectern sessions` returns a text table: a header line
     `ID  NAME  AGENT  STATUS  TARGET` (columns may be padded to align) and
     then one line per session with those fields in that order. TARGET is the
     session's `target_name` field when present, otherwise its `target_id`.
   - `--status <value>` keeps only sessions whose `status` equals the value.
     It composes with `--json`.
   - An unknown flag is an error that names the flag.
2. Wire it into `clientCommandAt` and into the command list in `main.go` so
   `lectern sessions` works end to end (`LECTERN_API` selects the server, as it
   does for the other client commands), and add it to `clientHelp`.
3. Add a unit test next to `TestAgentCLIListsAndSavesRegistry` in
   `cmd/lectern/client_test.go` using `httptest`.
4. Document the command where the other client commands are documented.

Do not commit.
