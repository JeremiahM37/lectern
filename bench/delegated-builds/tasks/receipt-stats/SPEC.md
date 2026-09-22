# Task: session statistics for action-receipt

Repository: action-receipt (Python, MCP server). Run tests with
`/home/admin/.venvs/action-receipt/bin/python -m pytest tests/unit -q`.

Add a way to summarise everything a session has recorded.

1. `ReceiptSession.stats()` in `action_receipt/session.py` returns a plain dict:
   - `total`: number of receipts recorded so far.
   - `by_verdict`: a dict with a key for EVERY verdict name in
     `action_receipt.verdict.VERDICTS` (all five, even when zero), each the
     count of receipts with that verdict.
   - `by_action`: counts keyed by action name, only for actions that occurred.
   - `settlement_ms`: `{"mean": float | None, "max": float | None}` over each
     receipt's `settlement.elapsed_ms`; both `None` when there are no receipts.
     Mean is rounded to one decimal place.
   - `last_id`: the id of the most recent receipt, or `None`.
2. An MCP tool `receipt_stats` in `action_receipt/server.py`, no arguments,
   registered like `receipt_last` (through `@_structured`), returning exactly
   `stats()` of the current session.
3. Update the tool inventory in `tests/unit/test_server_helpers.py` so the
   registry test still passes, and add unit tests for `stats()` (use the
   builders in `tests/unit/_synth.py`).
4. Mention the tool in the README's tool list.

Do not change the shape of existing receipts or tools. Do not commit.
