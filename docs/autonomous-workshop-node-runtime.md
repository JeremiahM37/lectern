# Node dependency delivery — implementation in progress

The candidate implements typed Node requirements, a fixed registry broker, immutable tooling and package bundles, offline lifecycle execution, cancellation generations, and retained-assignment recovery. Independent expert probes and private integration tests select the same four pinned Node identities. It is not yet a deployed worker capability; actual unattended request, delivery, repair and independent review remain unproven.

`tools/node-dependencies.py` checks a version 3 lock against root manifest dependency declarations, preserves every package entry (including nested peer/optional packages), validates exact public npm registry identities and SHA512 integrity, and inspects tar contents without executing package code. It reports lifecycle hooks and implicit node-gyp requirements; it never assumes suppressing hooks produces a working package. Aliases, workspaces and bundled dependencies currently require explicit additional support. Dependency-range/tree correctness and engines/platform compatibility still require the trusted npm resolver/ci in isolation; this verifier does not claim to solve them.

The concrete consumer is action-receipt `tests/e2e/test_playwright_mcp.py`, using `npx -y @playwright/mcp@0.0.80` against a local shared Chromium fixture. Required behaviors: changed click, blocked disabled click, attributed typing, and idle no-op, all through actual MCP clients. A skipped test is not a pass.

Implementation and acceptance:

1. Snapshot trusted Node/npm and bind their exact hashes/platform to the resolution key. Deliver packages through the fixed credential-free registry broker. Retain the first successful lock bytes; retries must not silently resolve again.
2. Run npm resolution and installation in the bounded namespace, initially without lifecycle execution. Validate the complete inventory, including required optional/peer dependencies. Run any required hooks only inside a second offline credential-free namespace; record failures as prerequisites.
3. Freeze content and provide genuine npm/npx offline execution. An approved pinned command may resolve only from the delivered cache/tree; test that uncached packages cannot fetch or run. Do not replace npx with a shim that changes the consumer test semantics.
4. Wire typed requirements and recovery, immutable runtime delivery receipts and teardown. Resume the retained blocked assignment only after actual probes change its prerequisite state.
5. Run the actual consumer and a deliberately broken behavior control, then observe autonomous request/delivery/independent review. Extend to ordinary locked Node projects rather than hardcoding this one package.

npm semantics references: https://docs.npmjs.com/cli/v11/commands/npm-ci/ and https://github.com/npm/cli/blob/latest/docs/lib/content/using-npm/scripts.md . `npm ci` validates manifest/lock consistency; ignore-scripts is not a runtime compatibility guarantee. Local installed tooling must be tested rather than assuming current documentation exactly matches it.

## Runtime contract and limits

Workers request `node_packages` schema 1 with condition `offline_node_available`: either exact package pins or an exact archived project manifest/lock pair, plus import or binary probes. Archived root lifecycle scripts run with the selected source subtree in an offline namespace. Their outputs remain in `/opt/node-project`; the evolving worker checkout receives the installed `node_modules` tree. These are distinct paths: source changes after provisioning do not silently rerun archived root hooks.

Delivery binds `node_bundle_key`, `node_input_key`, `node_lock_sha256` and `node_runtime_digest`. Expert tests freeze their helper source before launch and verify the full selected bundle inventory. Node tooling is mounted read-only under `/opt/node`; npm receives a writable copy of the verified cache with offline settings. A scratch `node_modules` link exposes the immutable installed packages through normal npm ancestor lookup, so genuine `npx` works from the default scratch directory and nested test checkouts without a command shim. The namespace has no network or worker bridge. Host-local administration directories are masked. Existing Python-only request serialization is preserved.

The registry broker accepts only fixed public npm metadata and tarball requests. Missing packages and unsupported closure/budget conditions become diagnosis inputs; transient transport failures stay retryable. It never forwards credentials or follows registry redirects. Aliases, workspaces, bundled dependencies and external git/file dependencies remain explicit unsupported inputs rather than silently incomplete installations.

The ordinary expert profile retains its 512 MiB scratch limit, which includes the writable npm cache. A larger cache can fail preparation; successfully provisioning a bundle alone does not prove every execution profile can run it.

Current evidence includes real systemd provisioning of pinned MCP packages and an archived lock project with an actual root postinstall hook, offline `npx` execution, and the original action-receipt MCP consumer passing with a deliberately broken implementation failing. These are integration fixtures, not autonomous workshop outcomes. Deployment requires the complete project verification suite and preservation of the already deployed server-conflict recovery changes.
