# Use Lectern from claude.ai (and ChatGPT)

Brainstorm in an ordinary chat on the web, desktop or phone app, then hand the
result straight to a coding agent:

> "Start a Lectern session in the `billing` project that builds what we just
> designed — give it this spec and the PDF I attached."
>
> "Send my session *fixing dashboards* this screenshot and tell it the chart
> axis is wrong."
>
> "What are my Lectern sessions doing?" · "End the session named exactly
> chat-test."

The chat talks to Lectern through a **web connector**: `lectern mcp --http`,
published at a public HTTPS address and protected by OAuth that only you can
approve. This page is the how-to. [web-connector.md](web-connector.md) is the
operator reference (environment variables, exposure, security model).

## What a chat can do

| Ask for | Tool | Notes |
|---|---|---|
| "What's running?" | `list_sessions`, `board_summary`, `list_tasks`, `task_status` | Read-only |
| Start a session that builds something | `start_session` | Claude or Codex, in a project, folder, or a blank scratch workspace |
| Give an open session a message, context or files | `send_to_session` | Can interrupt a running turn first |
| End a session | `end_session` | **Archives** it: the terminal stops, the record and final output are kept, and it can be restored from Lectern. Needs the exact name or id. |
| File or delegate board work | `create_task`, `delegate_build`, … | Same tools the CLI agents have |

What a chat **cannot** do: approve or deny an agent's request
(`decide_approval` is never exposed to the web connector), or read files on
your server by path.

### Context and files

- **What you worked out in the conversation** goes in as `context`. It is
  attached as `lectern-context.md` and, for a new session, also saved to
  Grimoire as a note tagged `brief`, so it stays searchable later.
- **Files you upload to the chat**, including PDFs and images, are handed over
  as the real file. Claude reads the upload in its sandbox and passes the
  bytes, and the session receives a byte-identical copy under
  `.lectern/context/`. Very large files may exceed what one tool call can
  carry. For those, send the file from a terminal with `Ctrl+\` or
  `lectern upload`.
- **Grimoire notes** can be attached by path.

Lectern also adds relevant Grimoire memory to every message it types into a
session, so the agent starts with the project's context.

## Set it up

### 1. Run the connector (once, on the Lectern host)

Run `lectern mcp --http` as a service and publish its public listener at an
HTTPS address, for example with a Cloudflare tunnel. The OAuth approval page
goes on a **separate, tailnet-only** address. The exact environment variables
and a systemd unit are in [web-connector.md](web-connector.md).

When it starts, the connector reports its public URL to Lectern. **Settings →
Connect your AI tools → claude.ai / ChatGPT** then shows the exact URL to
paste, with a copy button, and later "✓ connected · used N ago".

### 2. Add it to claude.ai

1. claude.ai → **Customize → Connectors → Add connector → Add custom
   connector**.
2. Name it `Lectern` and paste the URL from Settings (it ends in `/mcp`).
   claude.ai detects the OAuth sign-in by itself.
3. Press **Connect**. An approval page opens on your tailnet address. Approve
   it **from a device signed in to your Tailscale account**. Anyone else gets
   "Not recognized over Tailscale".
4. Set the tool permissions in the connector's settings. Recommended:
   read-only tools (list sessions/projects/tasks, board summary, task
   status/diff, pending approvals) on **Always allow**, and everything that
   starts, sends, ends or files work on **Needs approval**. Then each of those
   asks you with an Allow button in the chat.

### 3. ChatGPT

ChatGPT custom connectors need **Developer mode**, which is only available on
some plans. With it on, add a connector with the same URL under ChatGPT's
Settings → Apps. The OAuth server already accepts ChatGPT's redirect
addresses, and the approval works the same way.

> Status: ChatGPT is supported by the connector but **not yet tested end to
> end**. claude.ai is.

## Try it

In a **new** claude.ai chat (attach any PDF):

> Using the Lectern connector: list my sessions. Then start a Claude session in
> a blank workspace named "chat-test", give it the attached PDF as a file and a
> short summary as context, and have it reply with the PDF's title. Afterwards
> end the session named exactly chat-test.

Then check in Lectern:

- `chat-test` appeared, and its agent answered.
- The PDF is under `.lectern/context/` in its workspace.
- A Grimoire note tagged `brief` holds the summary.
- `chat-test` ended up archived.

## Troubleshooting

- **A tool is missing** (for example after upgrading Lectern). claude.ai caches
  a connector's tool list per chat. Open the connector's page under Customize
  → Connectors to refresh it, then start a **new** chat.
- **"Not recognized over Tailscale" on the approval page.** Approve from a
  device logged in to your own Tailscale account. Tagged devices and other
  people's devices are refused by design. The page also accepts the
  connector's admin token as an owner fallback.
- **The connector isn't listed.** Reload the connectors page. Custom
  connectors belong to the claude.ai organization you added them in.
- **"Couldn't connect to the server".** Check the service
  (`systemctl status lectern-web-mcp`) and that
  `https://<host>/.well-known/oauth-protected-resource` loads.
- **Revoke access.** Disconnect it in claude.ai, or revoke its tokens with the
  admin commands in [web-connector.md](web-connector.md).
