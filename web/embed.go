// Package web embeds the lectern PWA so the binary is fully self-contained:
// one file to deploy, and the UI works with no internet access.
package web

import "embed"

// IndexHTML is the app shell, served at /.
//
//go:embed index.html
var IndexHTML []byte

// Assets holds every static file (CSS, JS, service worker, icon, manifest and
// the vendored Inter font), served from the site root so the service worker can
// claim '/' as its scope.
//
//go:embed static
var Assets embed.FS
