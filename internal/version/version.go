// Package version carries the single source of truth for the release string.
package version

// Version is stamped into the API's /api/health payload and the UI footer.
// A release build overrides it from the tag (-X …/internal/version.Version);
// this value is what a plain `go build` reports.
var Version = "2.3.0"
