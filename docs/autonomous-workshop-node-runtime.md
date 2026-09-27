# Node dependency delivery — implementation in progress

The current candidate contains a data-only lock and tarball verifier, not a registered worker capability. Do not advertise Node recovery until controller provisioning, isolated installation, lifecycle scripts, offline execution, cancellation and retained-assignment recovery are wired and tested.

`tools/node-dependencies.py` checks a version 3 lock against root manifest dependency declarations, preserves every package entry (including nested peer/optional packages), validates exact public npm registry identities and SHA512 integrity, and inspects tar contents without executing package code. It reports lifecycle hooks and implicit node-gyp requirements; it never assumes suppressing hooks produces a working package. Aliases, workspaces and bundled dependencies currently require explicit additional support. Dependency-range/tree correctness and engines/platform compatibility still require the trusted npm resolver/ci in isolation; this verifier does not claim to solve them.

The concrete consumer is action-receipt `tests/e2e/test_playwright_mcp.py`, using `npx -y @playwright/mcp@0.0.80` against a local shared Chromium fixture. Required behaviors: changed click, blocked disabled click, attributed typing, and idle no-op, all through actual MCP clients. A skipped test is not a pass.

Next stages:

1. Snapshot trusted Node/npm and bind their exact hashes/platform to the resolution key. Deliver packages through the fixed credential-free registry broker. Retain the first successful lock bytes; retries must not silently resolve again.
2. Run npm resolution and installation in the bounded namespace, initially without lifecycle execution. Validate the complete inventory, including required optional/peer dependencies. Run any required hooks only inside a second offline credential-free namespace; record failures as prerequisites.
3. Freeze content and provide genuine npm/npx offline execution. An approved pinned command may resolve only from the delivered cache/tree; test that uncached packages cannot fetch or run. Do not replace npx with a shim that changes the consumer test semantics.
4. Wire typed requirements and recovery, immutable runtime delivery receipts and teardown. Resume the retained blocked assignment only after actual probes change its prerequisite state.
5. Run the actual consumer and a deliberately broken behavior control, then observe autonomous request/delivery/independent review. Extend to ordinary locked Node projects rather than hardcoding this one package.

npm semantics references: https://docs.npmjs.com/cli/v11/commands/npm-ci/ and https://github.com/npm/cli/blob/latest/docs/lib/content/using-npm/scripts.md . `npm ci` validates manifest/lock consistency; ignore-scripts is not a runtime compatibility guarantee. Local installed tooling must be tested rather than assuming current documentation exactly matches it.
