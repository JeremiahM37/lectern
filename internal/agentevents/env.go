package agentevents

// Env names injected into every interactive session's process environment at
// launch (see internal/sessions.Manager.launch), read back by whatever the
// agent's own hooks/statusline scripts run with. EnvHookURL is the base for
// one session's hook endpoints — POST $LECTERN_HOOK_URL/<event> and
// $LECTERN_HOOK_URL/statusline — not a single fixed URL.
const (
	EnvHookToken = "LECTERN_HOOK_TOKEN"
	EnvHookURL   = "LECTERN_HOOK_URL"
)
