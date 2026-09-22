# Task: a `tasks` command group for the lectern CLI

Repository: lectern (Go). Build with `go build ./...`; unit tests with
`go test ./cmd/... ./internal/console/`. (Some tests in other packages print
"real-process test requires the reviewed isolated runner" on this machine; that
is expected and not something to fix.)

The CLI can file nothing and see nothing about tasks. Add `lectern tasks`
with three subcommands, all implemented in a new file `cmd/lectern/tasks_cmd.go`
through one entry point
`tasksCommand(c *console.Client, args []string) ([]byte, error)`:

1. `lectern tasks` (alias `lectern tasks list`) fetches `GET /api/tasks` and
   prints a table with header `ID  STATUS  PROJECT  TITLE  AGENT` and one row per
   task in the order the API returned them. PROJECT is the task's
   `project_name`. Flags: `--status <s>` keeps only that status; `--project <name>`
   keeps only that project; `--json` prints the (filtered) array as JSON.
   Flags may be combined.
2. `lectern tasks show <id>` fetches `GET /api/tasks/{id}` and prints, one per
   line, `Title: …`, `Status: …`, `Project: …`, `Agent: …`, `Branch: …` (from
   `attempt.branch`, or `-` when there is no attempt) and then a blank line
   and the task's `prompt`. `--json` prints the object unchanged.
3. `lectern tasks diff <id>` fetches `GET /api/tasks/{id}/diff` and prints
   each file's `patch` in order, separated by a blank line. With no files it
   prints `no diff` and exits 0.
4. Errors: an unknown subcommand or flag is an error naming it; a missing id
   is an error; a non-numeric id is an error.
5. Wire `tasks` into `clientCommandAt`, the command list in `main.go`, and
   `clientHelp`; add tests in `cmd/lectern/tasks_cmd_test.go` with `httptest`
   covering the table, the filters, `show`, `diff` and the errors; document the
   command group where the other client commands are documented.

Do not commit.
