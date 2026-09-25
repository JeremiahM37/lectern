package api

import (
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// A push notification's deep link (/session/7) and a reload on any app
// route must get the app page; files, missing assets and unknown API paths
// must not.
func TestAppRouteFallback(t *testing.T) {
	assets := fstest.MapFS{"react/assets/app.js": {Data: []byte("x")}, "sw.js": {Data: []byte("x")}}
	for path, want := range map[string]bool{
		"/session/7":           true,
		"/task/3":              true,
		"/sessions":            true,
		"/react/assets/app.js": false,
		"/sw.js":               false,
		"/missing.css":         false,
		"/api/nope":            false,
		"/api":                 false,
		"/term/7681":           false,
		"/a2a/v1":              false,
		"/.well-known/x":       false,
		"/":                    false,
	} {
		if got := appRoute(assets, httptest.NewRequest("GET", path, nil)); got != want {
			t.Errorf("appRoute(GET %s) = %v, want %v", path, got, want)
		}
	}
	if appRoute(assets, httptest.NewRequest("POST", "/session/7", nil)) {
		t.Error("a POST must never be answered with the app page")
	}
}
