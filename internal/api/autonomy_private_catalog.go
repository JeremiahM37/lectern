package api

import (
	"net/http"
	"net/url"
	"sort"
)

// Catalog input is the controller's curated ledger projection, never a worker
// report or filesystem walk. Stable attempt cursors retain older rejected attempts.
func autoPrivateIntegrationDiscovery(rows []map[string]any, query url.Values) (any, int) {
	for key, values := range query {
		if len(values) != 1 || (key != "root_id" && key != "integration_id" && key != "after") {
			return map[string]string{"error": "Use integration_id for details, root_id to filter, or after for pagination"}, http.StatusBadRequest
		}
		if !autoHash256(values[0]) {
			return map[string]string{"error": "Expected a full integration identity"}, http.StatusBadRequest
		}
	}
	root, after := query.Get("root_id"), query.Get("after")
	selected := query.Get("integration_id")
	if selected != "" && (after != "" || root != "") {
		return map[string]string{"error": "Select details or pagination, not both"}, http.StatusBadRequest
	}
	ordered := append([]map[string]any(nil), rows...)
	for _, row := range ordered {
		id, ok := row["integration_id"].(string)
		if !ok || !autoHash256(id) {
			return map[string]string{"error": "Integration catalog identity unavailable"}, http.StatusServiceUnavailable
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i]["integration_id"].(string) < ordered[j]["integration_id"].(string)
	})
	items := []map[string]any{}
	for _, row := range ordered {
		id := row["integration_id"].(string)
		if root != "" && row["root_id"] != root {
			continue
		}
		if selected != "" {
			if id == selected {
				return map[string]any{"integration": row, "scope": "Managed private revision history; canonical application and deployment require separate evidence"}, http.StatusOK
			}
			continue
		}
		if id <= after {
			continue
		}
		if len(items) == 25 {
			return map[string]any{"items": items, "next_after": items[24]["integration_id"], "total": len(ordered)}, http.StatusOK
		}
		copy := make(map[string]any, len(row)+1)
		for k, v := range row {
			copy[k] = v
		}
		copy["details_uri"] = "/private-integrations?integration_id=" + id
		items = append(items, copy)
	}
	if selected != "" {
		return map[string]string{"error": "Private integration not found"}, http.StatusNotFound
	}
	return map[string]any{"items": items, "next_after": "", "total": len(ordered)}, http.StatusOK
}
