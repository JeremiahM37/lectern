# Project workflows

Lectern can optionally bootstrap two bundled workflow skills for an individual
project. Open Settings → Projects, choose a project, and use **Project workflows**
to select Claude Code or Codex. Each provider has its own independent enablement;
turning a workflow on for one provider does not enable it for the other.

Workflow integrations support persistent local, SSH, and Proxmox container
(`pct`) targets. Disposable sandbox targets are not supported: their agents run
inside newly created containers, which cannot use workflow sources stored on the
controller. Choose a persistent target to enable either workflow.

The catalog shows the pinned bundled version and links to the upstream project:

- [Spec Kit](https://github.com/github/spec-kit) turns a request into a small set
  of project specifications.
- [Maestro](https://github.com/sharpdeveye/maestro) supports diagnosing an existing
  workflow.

Lectern vendors pinned upstream files under each workflow bundle; both upstream
projects are MIT licensed. Lectern provides project skill wrappers and selected
command guidance. Maestro's full MCP integration, editor extension, and other
upstream tooling are outside this integration.

When enabled, the settings card lists the exact commands supported by that
project and provider. Invoke the bundled skill explicitly in a new agent session:

Spec Kit exposes `constitution`, `specify`, `clarify`, `plan`, `tasks`, `analyze`,
`checklist`, `implement`, and `converge`. Maestro exposes `diagnose`, `fortify`,
`refine`, `reflect`, `agent-workflow`, and `teach-maestro`. The API response and
the settings card remain the source of truth for the installed pinned version.

```text
# Claude Code
/lectern-spec-kit specify <request>
/lectern-spec-kit converge <request>
/lectern-maestro diagnose <request>

# Codex
$lectern-spec-kit specify <request>
$lectern-maestro diagnose <request>
```

The wrapper bootstraps workflow support only after one of these explicit
invocations. Existing sessions are not restarted when a setting changes, so
start a new Claude Code or Codex session after enabling or disabling a workflow.
Disabling a workflow changes future bootstrap behavior and leaves documents it
already generated in the project.

Workflow metadata is available through the project API:

```http
GET /api/projects/{id}/workflows?agent=claude|codex
PUT /api/projects/{id}/workflows/{workflow_id}
Content-Type: application/json

{"agent":"claude","enabled":true}
```

The GET response includes `version`, `upstream_url`, `enabled`, and the provider's
`commands`. Both reads and writes report `reload_required: true` because the
setting applies to newly started sessions.
