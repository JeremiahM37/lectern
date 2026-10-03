package mods

import (
	"errors"
	"strings"
)

// checkAPI decides whether a mod may make a request. "read" allows GET,
// "write" also POST, PUT and DELETE. Some of the API is never reachable from a
// mod at any level: plugin management (a mod could install or trust itself),
// approval decisions (agents ask a person, not a mod), sign-in, and secrets.
func checkAPI(level, method, path string) error {
	switch level {
	case "read", "write":
	default:
		return errors.New("$.api needs `api: read` in the plugin's capabilities")
	}
	if method != "GET" && level != "write" {
		return errors.New("$.api." + strings.ToLower(method) + " needs `api: write` in the plugin's capabilities")
	}
	return checkPath(path)
}

var forbiddenPrefixes = []string{"/api/plugins", "/api/auth", "/api/secrets", "/api/hook/", "/api/push/"}

func checkPath(path string) error {
	if !strings.HasPrefix(path, "/api/") {
		return errors.New("$.api paths start with /api/")
	}
	p, _, _ := strings.Cut(path, "?")
	lower := strings.ToLower(path)
	lp := strings.ToLower(p)
	// Encoded or relative segments could walk to a route the checks below
	// never saw, so the path part must be written out plainly.
	if strings.ContainsAny(p, "%\\#") || strings.Contains(p, "//") {
		return errors.New("$.api paths may not contain encoded characters or empty segments")
	}
	segments := strings.Split(strings.TrimPrefix(lp, "/"), "/")
	for _, s := range segments {
		if s == "." || s == ".." {
			return errors.New("$.api paths may not contain . or .. segments")
		}
	}
	for _, prefix := range forbiddenPrefixes {
		if lp == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(lp, prefix) {
			return errors.New(p + " is not reachable from a mod")
		}
	}
	for _, s := range segments {
		// POST /api/approvals/{id}/decision, and anything shaped like it.
		if s == "decision" || s == "decide" || s == "login" || s == "logout" {
			return errors.New(p + " is not reachable from a mod")
		}
	}
	if strings.Contains(lower, "secret") || strings.Contains(lower, "token") {
		return errors.New(p + " is not reachable from a mod")
	}
	return nil
}
