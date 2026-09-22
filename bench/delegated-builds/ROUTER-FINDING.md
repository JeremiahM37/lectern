# Why the "theirs" arm cannot use native Codex subagents here (2026-09-21)

astra-flash-orchestrator needs codex-router with the DeepSeek Flash route
advertised `multi_agent_version: "v2"`. Attempted in an isolated CODEX_HOME
(/mnt/bulk/codex-bench/home) with codex-router 0.6.0 (81b9aa6), Codex 0.155.1
and 0.154.0, DeepSeek key, Node 24:

- Signed in with the ChatGPT account: Codex refuses to spawn a non-OpenAI
  child ("The 'deepseek-flash' model is not supported when using Codex with a
  ChatGPT account"). The router's `subagents certify` defers for that reason
  on both Codex versions.
- Router login-free mode (`auth-mode on`, Codex signed out, router holding the
  session via MODEL_ROUTER_CODEX_AUTH): certification's parent never ran, and a
  direct `codex exec -m gpt-6-astra` was routed to deepseek/deepseek-v4-flash
  by the router's native-slug aliasing. There is no Astra when signed out.

So with today's tools the pair "Astra on the subscription + Flash as a native
subagent" is not reachable. The benchmark's "theirs" arm therefore runs their
skill, policy and templates verbatim with Astra signed in natively, and the
worker as a separate `codex exec` process on DeepSeek Flash
(model_providers.deepseek, wire_api=responses), i.e. the same workflow with a
process transport instead of spawn_agent.
