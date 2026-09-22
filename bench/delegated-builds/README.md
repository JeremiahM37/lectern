# Delegated-builds benchmark harness

The harness behind `docs/benchmarks/delegated-builds-2026-09-22.md`.

- `bench.py run <task> <arm> <rep>` clones a pinned base repository, runs the
  lead (`codex exec`) with the arm's Codex home, grades, and collects token
  usage from the session records; `bench.py summary` and `report.py` tabulate.
- `tasks/<task>/SPEC.md` is exactly what the lead was given.
- `flash_builder.py` is the one substitution in the upstream arm: the worker
  as a separate Codex process, because native subagents on a non-OpenAI model
  are refused on a ChatGPT login (`ROUTER-FINDING.md`).

The hidden acceptance tests and graders are deliberately **not** in this
repository: two of the tasks target this repository itself, and an agent
working in a clone would otherwise be able to read the test it is graded
by. They live beside the harness on the benchmark machine
(`/mnt/bulk/codex-bench/tasks/<task>/{accept,grade.sh}`); ask for them.

Paths in `bench.py` are the benchmark machine's; it is a record of what ran,
not a portable tool.
