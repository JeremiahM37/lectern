package a2a

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// ProtocolVersion is the A2A protocol version this server speaks.
const ProtocolVersion = "1.0"

// ProtocolBinding is the transport binding of the interface in the card.
// Lectern serves JSON-RPC 2.0 over HTTP POST at InterfacePath.
const ProtocolBinding = "JSONRPC"

// AgentInterface is one advertised way to reach this agent.
type AgentInterface struct {
	URL             string `json:"url"`
	ProtocolBinding string `json:"protocolBinding"`
	ProtocolVersion string `json:"protocolVersion"`
}

// AgentCapabilities describes optional protocol features. Lectern sets both
// false: a dispatch is a long-running task you poll (or wait on over the REST
// API), so there is no streaming binding and no A2A push registration here.
type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

// AgentSkill is one thing a client can ask this agent for.
type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Examples    []string `json:"examples"`
}

// SecurityScheme is one accepted credential, in the OpenAPI 3 shape A2A
// reuses. SecurityScheme.Type is "http", "apiKey", "openIdConnect",
// "oauth2" or "mutualTLS".
type SecurityScheme struct {
	Type        string `json:"type"`
	Scheme      string `json:"scheme,omitempty"`
	In          string `json:"in,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description"`
}

// AgentCard is the public metadata a client reads before it sends anything:
// who this agent is, where its interface is, what it can do, and what
// credentials it accepts. It carries nothing secret — no project names, no
// ports, no tokens — because it is served unauthenticated.
type AgentCard struct {
	Name                string                    `json:"name"`
	Description         string                    `json:"description"`
	Version             string                    `json:"version"`
	SupportedInterfaces []AgentInterface          `json:"supportedInterfaces"`
	Capabilities        AgentCapabilities         `json:"capabilities"`
	DefaultInputModes   []string                  `json:"defaultInputModes"`
	DefaultOutputModes  []string                  `json:"defaultOutputModes"`
	Skills              []AgentSkill              `json:"skills"`
	SecuritySchemes     map[string]SecurityScheme `json:"securitySchemes"`
	Security            []map[string][]string     `json:"security"`
}

// Card builds Lectern's Agent Card. baseURL is LECTERN_BASE_URL — the address
// a client (and this server's own targets) reach the control plane at — and
// the version comes from internal/version, the same string /api/health
// reports, so a card and a health check can never disagree.
func Card(baseURL string) AgentCard {
	return AgentCard{
		Name: "Lectern",
		Description: "Control plane for AI coding agents. Dispatch a coding task with a chosen " +
			"agent into a fresh git worktree of a registered project, follow it on a task board, " +
			"and read back the diff and the agent's report. Runs Claude Code, Codex, Gemini and " +
			"local models against local or remote targets.",
		Version: version.Version,
		SupportedInterfaces: []AgentInterface{{
			URL:             strings.TrimRight(baseURL, "/") + InterfacePath,
			ProtocolBinding: ProtocolBinding,
			ProtocolVersion: ProtocolVersion,
		}},
		Capabilities:       AgentCapabilities{Streaming: false, PushNotifications: false},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		Skills: []AgentSkill{
			{
				ID:   "dispatch-task",
				Name: "Dispatch a coding task",
				Description: "Run a coding task with a chosen agent in a fresh git worktree of a " +
					"registered project and return the diff summary. Name the project in " +
					"message metadata; the task runs in the background and GetTask reports its " +
					"state and its artifact.",
				Tags: []string{"coding", "task", "git-worktree", "dispatch"},
				Examples: []string{
					"Fix the failing pagination test in the lectern project with the claude agent.",
					"Add a --json flag to the CLI in the mttyd project, then report the diff summary.",
				},
			},
			{
				ID:   "best-of-n",
				Name: "Best of N",
				Description: "Run one task with several agents, or several models, as parallel " +
					"attempts in their own worktrees and pick the best attempt. Set message " +
					"metadata.variants to the agent names, or to [{agent, model}] objects.",
				Tags: []string{"best-of-n", "variants", "parallel", "comparison"},
				Examples: []string{
					"Try the cache refactor with claude and codex in parallel and pick the better attempt.",
					"Run the migration with two models and compare the results.",
				},
			},
			{
				ID:   "project-status",
				Name: "Project status",
				Description: "Report the board for a project: ListTasks with contextId set to the " +
					"project name returns the tasks this client has on it, their states and their " +
					"artifacts.",
				Tags: []string{"board", "status", "tasks", "project"},
				Examples: []string{
					"What is on the board for the lectern project?",
					"List my open tasks for the mttyd project.",
				},
			},
		},
		SecuritySchemes: map[string]SecurityScheme{
			"bearer": {
				Type:   "http",
				Scheme: "bearer",
				Description: "LECTERN_AUTH_TOKEN, sent as `Authorization: Bearer <token>`. Required " +
					"when Lectern resolves to token auth mode (LECTERN_AUTH=token, or auto with a " +
					"listener that is not loopback-only).",
			},
			"tailscale": {
				Type: "apiKey",
				In:   "header",
				Name: "Tailscale-User-Login",
				Description: "Tailscale identity is accepted in place of a token: in tailscale auth " +
					"mode a request from an allow-listed tailnet peer is authenticated as that peer " +
					"with no credential at all. The header form is only honoured behind `tailscale " +
					"serve` with LECTERN_TRUST_SERVE_HEADERS=1; otherwise the identity is taken from " +
					"the connection itself. Declared here because every accepted scheme must appear " +
					"in the card. No credential is asked for at all in auth mode none.",
			},
		},
		Security: []map[string][]string{{"bearer": {}}, {"tailscale": {}}},
	}
}
