# Native Codex subagents on a routed model: what works, and what I got wrong first

**Correct picture (2026-09-22, Codex 0.155.1, codex-router 0.6.0 @ 81b9aa6, ChatGPT login):**
a native Astra parent, signed in with a ChatGPT account, CAN spawn a child on
`deepseek/deepseek-v4.1-flash` through codex-router, and astra-flash-orchestrator's
own installer then passes its preflight and installs the real `astra_flash_builder`
role. The steps that matter, beyond `bin/setup` and `bin/enable`:

    bin/control picker set deepseek/deepseek-v4.1-flash show   # DeepSeek routes ship hidden
    bin/control subagents set deepseek/deepseek-v4.1-flash on  # writes the agent definition, publishes v2
    bin/control subagents explain deepseek/deepseek-v4.1-flash # "can be spawned as a subagent"

Verified: a parent turn on gpt-6-astra, three child turns on deepseek/deepseek-v4.1-flash
(router log), hello.txt created by the child, session record for the child thread on the
routed model.

**What I got wrong the first time, and why (kept so nobody repeats it):**

1. My first probe pointed a role TOML at the bare upstream id `deepseek-flash` with
   `model_provider = deepseek`, without the router. Codex's subagent runs on the
   session's provider, so the child went to OpenAI, which answered
   `The 'deepseek-flash' model is not supported when using Codex with a ChatGPT account`.
   That message is about *that* request, not a general rule.
2. `bin/control subagents certify` deferred with the same words. Its probe uses an
   ephemeral home and a `gpt-5.6-sol` parent, and its own draft application for this
   route (`v2_agent/deepseek/deepseek-v4.1-flash/proof.md`) records the delegation
   checks as *pending*. I read the deferral as "Codex refuses", when the route was
   simply hidden and not selected: `promoteNativeMultiAgent` leaves a hidden model
   at v1, so Codex was never offered it.
3. Login-free mode was a red herring: it signs Codex out and aliases native slugs to
   external models (a `codex exec -m gpt-6-astra` was served by deepseek-v4-flash).
   Native GPT models are not available signed out, by design. It is for other
   harnesses, not for this.

Other things that cost time: the router needs Node ≥22; `systemctl --user` is
absent on this box (`MODEL_ROUTER_SKIP_SERVICE_MANAGER=1`, run
`bin/start --foreground` in tmux); the key prompt needs a pty (`script -q -c`);
`bin/enable` writes `~/.config/systemd/user/codex-router.service` even when the
service manager is skipped; a timed-out `enable` leaves
`$CODEX_HOME/codex-router/service-operation.lock`.

**The benchmark's "theirs (process worker)" arm** was run before this was found;
it is kept, and the "theirs (native subagent)" arm is the faithful one.
