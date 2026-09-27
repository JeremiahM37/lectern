package api

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type nodeTestTransport func(*http.Request) (*http.Response, error)

func (f nodeTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func nodeFixture(t *testing.T) (*autoNodeBroker, *[]string, []byte) {
	t.Helper()
	calls := []string{}
	data := []byte("fixture tar bytes, never executed")
	digest := sha512.Sum512(data)
	integrity := "sha512-" + base64.StdEncoding.EncodeToString(digest[:])
	version := map[string]any{"name": "@example/cli", "version": "1.2.3", "dist": map[string]string{"tarball": autoNodeRegistry + "@example/cli/-/cli-1.2.3.tgz", "integrity": integrity}, "dependencies": map[string]string{"child": "^2.0.0"}, "readme": "unneeded text"}
	b := &autoNodeBroker{slots: make(chan struct{}, 4), client: &http.Client{Transport: nodeTestTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.String())
		if r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Fatal("client headers/method leaked")
		}
		var raw []byte
		switch r.URL.String() {
		case autoNodeRegistry + "@example/cli":
			raw, _ = json.Marshal(map[string]any{"name": "@example/cli", "versions": map[string]any{"1.2.3": version}, "dist-tags": map[string]string{"latest": "1.2.3"}})
		case autoNodeRegistry + "@example/cli/1.2.3":
			raw, _ = json.Marshal(version)
		case autoNodeRegistry + "@example/cli/-/cli-1.2.3.tgz":
			raw = data
		default:
			t.Fatal("unexpected destination", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: int64(len(raw)), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
	})}}
	return b, &calls, data
}
func TestNodeBrokerMetadataAndAuthenticatedTarball(t *testing.T) {
	b, calls, data := nodeFixture(t)
	for _, path := range []string{"/npm/@example%2fcli", "/npm/@example/cli/-/cli-1.2.3.tgz"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "secret")
		r.Header.Set("Cookie", "secret")
		w := httptest.NewRecorder()
		b.serveHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		if strings.HasSuffix(path, "tgz") {
			if w.Body.String() != string(data) {
				t.Fatal("wrong tar")
			}
		} else {
			if !strings.Contains(w.Body.String(), autoNodeMirror) || strings.Contains(w.Body.String(), "unneeded text") {
				t.Fatal(w.Body.String())
			}
		}
	}
	if len(*calls) != 3 {
		t.Fatal(*calls)
	}
}
func TestNodeBrokerRefusesForeignRequestsWithoutFetch(t *testing.T) {
	b, calls, _ := nodeFixture(t)
	for _, path := range []string{"/npm/@example/cli?secret=1", "/npm/@example/cli?", "/npm/../secrets", "/npm/@example/cli/-/other-1.2.3.tgz", "/npm/@example/cli/-/cli-latest.tgz", "http://evil.example/npm/pkg", "/npm//evil", "/npm/foo%252fbar"} {
		w := httptest.NewRecorder()
		b.serveHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	b.serveHTTP(w, httptest.NewRequest("POST", "/npm/pkg", strings.NewReader("secret")))
	if w.Code != 403 || len(*calls) != 0 {
		t.Fatal(w.Code, *calls)
	}
}
func TestNodeBrokerTarballTamperingAndTransferBudget(t *testing.T) {
	b, _, _ := nodeFixture(t)
	old := b.client.Transport
	b.client.Transport = nodeTestTransport(func(r *http.Request) (*http.Response, error) {
		res, e := old.RoundTrip(r)
		if strings.HasSuffix(r.URL.Path, "tgz") {
			res.Body = io.NopCloser(strings.NewReader("bad"))
			res.ContentLength = 3
		}
		return res, e
	})
	if _, e := b.tarball(context.Background(), "@example/cli", "1.2.3"); e == nil {
		t.Fatal("tampered archive accepted")
	}
	b.bytes = autoNodeTransferLimit
	if _, e := b.fetch(context.Background(), autoNodeRegistry+"@example/cli", "application/json", 10); e == nil {
		t.Fatal("budget ignored")
	}
}
func TestNodeBrokerPreservesLegacyCatalogWithoutGrantingDownload(t *testing.T) {
	raw := json.RawMessage(`{"name":"pkg","version":"1.0.0","dist":{"tarball":"https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz","shasum":"abc"}}`)
	if _, e := autoNodeRewriteVersion("pkg", "1.0.0", raw); e != nil {
		t.Fatal(e)
	}
	if _, e := autoNodeIntegrity("sha1-abc"); e == nil {
		t.Fatal("legacy download admitted")
	}
	for _, url := range []string{"https://evil.example/pkg.tgz", "https://registry.npmjs.org/pkg/-/pkg-1.0.0.tgz?x=1"} {
		bad := strings.Replace(string(raw), autoNodeRegistry+"pkg/-/pkg-1.0.0.tgz", url, 1)
		if _, e := autoNodeRewriteVersion("pkg", "1.0.0", json.RawMessage(bad)); e == nil {
			t.Fatal("foreign URL accepted")
		}
	}
}

// Explicit live metadata probe only; normal test suites never need the registry.
func TestNodeBrokerRealRegistry(t *testing.T) {
	if os.Getenv("LECTERN_NODE_REGISTRY_PROBE") != "1" {
		t.Skip("explicit public registry probe only")
	}
	h := autoNodeDependencyBroker()
	for _, name := range []string{"lodash", "debug", "react", "typescript"} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", "/npm/"+name, nil))
		if w.Code != 200 {
			t.Fatalf("%s metadata: %d %s", name, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/npm/@playwright%2fmcp", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var doc struct {
		Versions map[string]json.RawMessage `json:"versions"`
	}
	if json.Unmarshal(w.Body.Bytes(), &doc) != nil || len(doc.Versions["0.0.80"]) == 0 {
		t.Fatal("pinned consumer version absent")
	}
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/npm/@playwright/mcp/-/mcp-0.0.80.tgz", nil))
	if w.Code != 200 || w.Body.Len() == 0 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Log("PASS real registry packument and SHA512 authenticated pinned consumer artifact")
}

func TestNodeBrokerFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		content string
		want    int
		marker  string
	}{
		{"absent", 404, "not found", 404, "package-not-found"},
		{"upstream outage", 503, "temporary outage", 502, ""},
		{"unsupported metadata", 200, `{"name":"wrong","versions":{}}`, 422, "unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &autoNodeBroker{slots: make(chan struct{}, 1), client: &http.Client{Transport: nodeTestTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Header: http.Header{}, ContentLength: int64(len(tc.content)), Body: io.NopCloser(strings.NewReader(tc.content))}, nil
			})}}
			w := httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", "/npm/pkg", nil))
			if w.Code != tc.want || w.Header().Get("X-Lectern-Node-Registry") != tc.marker {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
		})
	}
}

func TestNodeBrokerContentNegotiationAndDottedNames(t *testing.T) {
	b, _, _ := nodeFixture(t)
	old := b.client.Transport
	b.client.Transport = nodeTestTransport(func(r *http.Request) (*http.Response, error) {
		want := "application/vnd.npm.install-v1+json"
		if strings.HasSuffix(r.URL.Path, "/1.2.3") {
			want = "application/json"
		}
		if strings.HasSuffix(r.URL.Path, ".tgz") {
			want = "application/octet-stream"
		}
		if r.Header.Get("Accept") != want {
			t.Fatalf("wrong Accept %q wanted %q", r.Header.Get("Accept"), want)
		}
		return old.RoundTrip(r)
	})
	if _, e := b.metadata(context.Background(), "@example/cli"); e != nil {
		t.Fatal(e)
	}
	if _, e := b.tarball(context.Background(), "@example/cli", "1.2.3"); e != nil {
		t.Fatal(e)
	}
	called := false
	b.client.Transport = nodeTestTransport(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.String() != autoNodeRegistry+"foo..bar" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})
	w := httptest.NewRecorder()
	b.serveHTTP(w, httptest.NewRequest("GET", "/npm/foo..bar", nil))
	if !called || w.Code != 404 {
		t.Fatal("valid dotted name refused", w.Code)
	}
}
