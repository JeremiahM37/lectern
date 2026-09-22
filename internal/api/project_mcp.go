package api

// Project MCP settings have their own endpoint because the values may contain
// credentials. The ordinary project row deliberately omits the credential
// storage; this endpoint is the safe UI/TUI surface: values are redacted,
// edits are conditional, and the server restores only placeholders it minted
// for the current revision.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Match credential-shaped field names, not arbitrary server names such as
// "tokenizer". env/header containers are handled structurally below.
var mcpSecretKey = regexp.MustCompile(`(?i)(^|[_-])(token|secret|password|authorization|api[-_]?key)($|[_-])`)
var mcpName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type projectMCPRequest struct {
	MCP       map[string]any `json:"mcp"`
	Revision  string         `json:"revision"`
	StrictMCP *bool          `json:"strict_mcp"`
}

const mcpRetentionKey = "__lectern_retained"

func (s *Server) mcpRevision(root any, strict bool) string {
	if len(s.mcpKey) == 0 {
		s.mcpKey = make([]byte, 32)
		if _, err := rand.Read(s.mcpKey); err != nil {
			panic("cannot seed MCP revision key")
		}
	}
	b, _ := json.Marshal(struct {
		Root   any  `json:"root"`
		Strict bool `json:"strict"`
	}{root, strict})
	h := hmac.New(sha256.New, s.mcpKey)
	_, _ = h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *Server) mcpMarker(projectID int64, revision, path string) map[string]any {
	// The token is an opaque, keyed reference. The old value is recovered from
	// the conditional old document during PUT, so GETs never accumulate secret
	// copies in server memory and a marker cannot be moved to another field.
	h := hmac.New(sha256.New, s.mcpKey)
	_, _ = h.Write([]byte(fmt.Sprintf("mcp-retention\x00%d\x00%s\x00%s", projectID, revision, path)))
	return map[string]any{mcpRetentionKey: hex.EncodeToString(h.Sum(nil))}
}

func mcpPath(parent, key string) string {
	key = strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	return parent + "/" + key
}

func (s *Server) maskMCP(v any, key string, redact bool, projectID int64, revision, path string) any {
	// env and headers are credential containers. Redact their values without
	// treating arbitrary server names such as "tokenizer" as secret keys.
	if redact && key != "env" && key != "headers" {
		return s.mcpMarker(projectID, revision, path)
	}
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = s.maskMCP(item, key, redact, projectID, revision, mcpPath(path, strconv.Itoa(i)))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			// At path "" this is the server-name map. A server named "token"
			// or "env" is still an ordinary server and must remain readable.
			childRedact := path != "" && (redact || k == "env" || k == "headers" || mcpSecretKey.MatchString(k))
			out[k] = s.maskMCP(item, k, childRedact, projectID, revision, mcpPath(path, k))
		}
		return out
	default:
		return v
	}
}

func (s *Server) restoreMCP(next, old any, key string, redact bool, projectID int64, revision, path string) (any, error) {
	if ref, ok := mcpRetentionToken(next); ok {
		expected := s.mcpMarker(projectID, revision, path)[mcpRetentionKey].(string)
		if ref != expected || old == nil {
			return nil, fmt.Errorf("secret retention token expired or moved; reload MCP settings")
		}
		return old, nil
	}
	if _, ok := next.(string); ok && (redact || mcpSecretKey.MatchString(key)) {
		// A literal value, including placeholder-like text, is data. Retention
		// uses the explicit typed object above, never a colliding string.
		return next, nil
	}
	if nm, ok := next.(map[string]any); ok {
		om, _ := old.(map[string]any)
		out := make(map[string]any, len(nm))
		for k, value := range nm {
			var prior any
			if om != nil {
				prior = om[k]
			}
			childRedact := path != "" && (redact || k == "env" || k == "headers" || mcpSecretKey.MatchString(k))
			restored, err := s.restoreMCP(value, prior, k, childRedact, projectID, revision, mcpPath(path, k))
			if err != nil {
				return nil, err
			}
			out[k] = restored
		}
		return out, nil
	}
	if na, ok := next.([]any); ok {
		oa, _ := old.([]any)
		out := make([]any, len(na))
		for i, value := range na {
			var prior any
			if i < len(oa) {
				prior = oa[i]
			}
			restored, err := s.restoreMCP(value, prior, key, redact, projectID, revision, mcpPath(path, strconv.Itoa(i)))
			if err != nil {
				return nil, err
			}
			out[i] = restored
		}
		return out, nil
	}
	return next, nil
}

func mcpRetentionToken(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return "", false
	}
	token, ok := m[mcpRetentionKey].(string)
	return token, ok && token != ""
}

func isMCPRetentionToken(v any) bool {
	_, ok := mcpRetentionToken(v)
	return ok
}

func parseProjectMCP(raw string) (map[string]any, map[string]any, bool, error) {
	var root map[string]any
	if strings.TrimSpace(raw) == "" {
		root = map[string]any{}
	} else if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return nil, nil, false, fmt.Errorf("stored MCP settings are invalid: %w", err)
	}
	servers := root
	wrapped := false
	if value, ok := root["mcpServers"]; ok {
		var okServers bool
		servers, okServers = value.(map[string]any)
		if !okServers {
			return nil, nil, false, fmt.Errorf("mcpServers must be an object")
		}
		wrapped = true
	}
	return root, servers, wrapped, nil
}

func validateMCP(servers map[string]any) error {
	for name, value := range servers {
		if name == "mcpServers" || !mcpName.MatchString(name) {
			return fmt.Errorf("MCP server names must use letters, digits, _ or -")
		}
		cfg, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("MCP server %q must be an object", name)
		}
		command, hasCommand := cfg["command"]
		endpoint, hasURL := cfg["url"]
		if !hasCommand && !hasURL {
			return fmt.Errorf("MCP server %q needs command (stdio) or url (HTTP)", name)
		}
		if hasCommand && (hasURL || !nonEmptyString(command)) {
			return fmt.Errorf("MCP server %q has an invalid command/url combination", name)
		}
		if hasURL {
			u, ok := endpoint.(string)
			parsed, parseErr := url.Parse(u)
			if !ok || strings.TrimSpace(u) == "" || parseErr != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
				return fmt.Errorf("MCP server %q needs an http(s) URL", name)
			}
		}
		for _, container := range []string{"args", "env", "headers"} {
			if value, ok := cfg[container]; ok {
				if container == "args" {
					items, ok := value.([]any)
					if !ok {
						return fmt.Errorf("MCP server %q args must be an array", name)
					}
					for _, item := range items {
						if _, ok := item.(string); !ok {
							return fmt.Errorf("MCP server %q args must contain strings", name)
						}
					}
				} else {
					obj, ok := value.(map[string]any)
					if !ok {
						return fmt.Errorf("MCP server %q %s must be an object", name, container)
					}
					for _, item := range obj {
						if _, ok := item.(string); !ok && !isMCPRetentionToken(item) {
							return fmt.Errorf("MCP server %q %s values must be strings", name, container)
						}
					}
				}
			}
		}
	}
	return nil
}

func (s *Server) projectMCP(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	p, err := s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	root, servers, wrapped, err := parseProjectMCP(p.MCPJSON)
	if err != nil {
		respondErr(w, err)
		return
	}
	// Revisions are keyed, so the raw credential-bearing document never becomes
	// an offline-guessable value in the response.
	revision := s.mcpRevision(root, p.StrictMCP != 0)
	writeJSON(w, 200, map[string]any{
		"mcp": s.maskMCP(servers, "", false, id, revision, ""), "revision": revision,
		"strict_mcp": p.StrictMCP != 0, "wrapped": wrapped,
		"codex_strict_supported": false,
	})
}

func (s *Server) putProjectMCP(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	var in projectMCPRequest
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if in.MCP == nil || strings.TrimSpace(in.Revision) == "" {
		httpError(w, 422, "mcp and revision are required")
		return
	}

	// The read/compare/write is one critical section so two browser tabs cannot
	// silently overwrite each other. The draft stays client-side on a 409.
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	p, err := s.DB.Project(id)
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	root, oldServers, wrapped, err := parseProjectMCP(p.MCPJSON)
	if err != nil {
		respondErr(w, err)
		return
	}
	currentRevision := s.mcpRevision(root, p.StrictMCP != 0)
	if in.Revision != currentRevision {
		w.Header().Set("X-Lectern-MCP-Revision", currentRevision)
		writeJSON(w, 409, map[string]any{"detail": "MCP settings changed on the server; reload before saving", "current_revision": currentRevision})
		return
	}
	if err := validateMCP(in.MCP); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	restored, restoreErr := s.restoreMCP(in.MCP, oldServers, "", false, id, currentRevision, "")
	if restoreErr != nil {
		httpError(w, 409, "%s", restoreErr)
		return
	}
	servers, _ := restored.(map[string]any)
	if wrapped {
		root["mcpServers"] = servers
	} else {
		root = servers
	}
	newRaw, err := json.Marshal(root)
	if err != nil {
		respondErr(w, err)
		return
	}
	nextStrict := p.StrictMCP
	if in.StrictMCP != nil {
		if *in.StrictMCP && p.DefaultAgent == "codex" {
			// Codex's additive -c settings cannot implement strict replacement.
			// Keep the setting unchanged and report the limitation honestly.
			httpError(w, 422, "strict MCP replacement is unsupported for Codex; use additive MCP settings")
			return
		}
		if *in.StrictMCP {
			nextStrict = 1
		} else {
			nextStrict = 0
		}
	}
	// Keep the compare in SQLite as well as the in-process mutex. Other writers
	// can update a project through a legacy PATCH or another process between our
	// read and write; the old MCP and strict values make that race a safe 409.
	result, err := s.DB.Exec(`UPDATE projects SET mcp_json=?, strict_mcp=?
		WHERE id=? AND mcp_json=? AND strict_mcp=?`, string(newRaw), nextStrict, id, p.MCPJSON, p.StrictMCP)
	if err != nil {
		respondErr(w, err)
		return
	}
	if changed, rowsErr := result.RowsAffected(); rowsErr != nil || changed != 1 {
		latest, latestErr := s.DB.Project(id)
		if latestErr != nil {
			httpError(w, 404, "no such project")
			return
		}
		latestRoot, _, _, parseErr := parseProjectMCP(latest.MCPJSON)
		if parseErr != nil {
			respondErr(w, parseErr)
			return
		}
		latestRevision := s.mcpRevision(latestRoot, latest.StrictMCP != 0)
		w.Header().Set("X-Lectern-MCP-Revision", latestRevision)
		writeJSON(w, 409, map[string]any{"detail": "MCP settings changed on the server; reload before saving", "current_revision": latestRevision})
		return
	}
	out, err := s.DB.Project(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	root, servers, wrapped, err = parseProjectMCP(out.MCPJSON)
	if err != nil {
		respondErr(w, err)
		return
	}
	newRevision := s.mcpRevision(root, out.StrictMCP != 0)
	writeJSON(w, 200, map[string]any{"mcp": s.maskMCP(servers, "", false, id, newRevision, ""), "revision": newRevision, "strict_mcp": out.StrictMCP != 0, "wrapped": wrapped, "codex_strict_supported": false})
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}
