package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type pythonBrokerTransport func(*http.Request) (*http.Response, error)

func (f pythonBrokerTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func pythonBrokerFixture(t *testing.T, mutate func(string, *http.Response)) (*autoPythonBroker, string, string, *[]string) {
	t.Helper()
	return pythonBrokerNamedFixture(t, "demo_pkg-1.2.3-py2.py3-none-any.whl", mutate)
}

func pythonBrokerNamedFixture(t *testing.T, filename string, mutate func(string, *http.Response)) (*autoPythonBroker, string, string, *[]string) {
	t.Helper()
	wheel := []byte("wheel bytes; extraction validation belongs to isolated installer")
	sum := sha256.Sum256(wheel)
	digest := hex.EncodeToString(sum[:])
	remote := "https://files.pythonhosted.org/packages/aa/bb/cccc/" + filename
	index := map[string]any{"meta": map[string]string{"api-version": "1.4"}, "name": "Demo_Pkg", "files": []any{
		map[string]any{"filename": filename, "url": remote, "hashes": map[string]string{"sha256": digest}, "requires-python": ">=3.9", "yanked": "reason \"quoted\" <script>"},
		map[string]string{"filename": "demo_pkg-1.2.3.tar.gz", "url": "https://evil.test/source"},
	}}
	release := map[string]any{"urls": []any{map[string]any{"filename": filename, "url": remote, "digests": map[string]string{"sha256": digest}, "packagetype": "bdist_wheel"}}}
	calls := []string{}
	tr := pythonBrokerTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.URL.String())
		if r.Method != "GET" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Range") != "" || r.Header.Get("User-Agent") != "Lectern-Offline-Python/1" {
			t.Fatal("request headers/method leaked", r)
		}
		var raw []byte
		ct := "application/json"
		switch r.URL.String() {
		case "https://pypi.org/simple/demo-pkg/":
			raw, _ = json.Marshal(index)
			ct = "application/vnd.pypi.simple.v1+json"
		case "https://pypi.org/pypi/demo-pkg/1.2.3/json":
			raw, _ = json.Marshal(release)
		case remote:
			raw = wheel
			ct = "application/octet-stream"
		default:
			t.Fatalf("unexpected destination %s", r.URL)
			return nil, errors.New("unexpected")
		}
		res := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{ct}}, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}
		if mutate != nil {
			mutate(r.URL.Host, res)
		}
		return res, nil
	})
	b := &autoPythonBroker{client: &http.Client{Transport: tr}, slots: make(chan struct{}, 4)}
	return b, filename, digest, &calls
}

func TestPythonBrokerSanitizedIndexAndRestartSafeWheel(t *testing.T) {
	b, filename, digest, calls := pythonBrokerFixture(t, nil)
	req := httptest.NewRequest("GET", "/python/simple/demo-pkg/", nil)
	req.Header.Set("Authorization", "Bearer must-not-forward")
	req.Header.Set("Cookie", "must-not-forward")
	req.Header.Set("Range", "bytes=0-1")
	w := httptest.NewRecorder()
	b.serveHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	link := "/python/wheel/demo-pkg/1.2.3/" + digest + "/" + filename
	for _, want := range []string{link + "#sha256=" + digest, `data-requires-python="&gt;=3.9"`, `data-yanked="reason &#34;quoted&#34; &lt;script&gt;"`} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatal("missing sanitized field", want, w.Body.String())
		}
	}
	for _, bad := range []string{"evil.test", "files.pythonhosted.org", "tar.gz", "linux_x86_64", "<script>"} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatal("unsafe index", bad)
		}
	}
	if len(*calls) != 1 {
		t.Fatal(*calls)
	}
	// New handler, no previous index request or in-memory authorization map.
	b, _, _, calls = pythonBrokerFixture(t, nil)
	w = httptest.NewRecorder()
	b.serveHTTP(w, httptest.NewRequest("GET", link, nil))
	if w.Code != 200 || len(*calls) != 2 || w.Header().Get("Content-Length") != fmt.Sprint(w.Body.Len()) {
		t.Fatal(w.Code, w.Body.String(), *calls)
	}
	h := sha256.Sum256(w.Body.Bytes())
	if hex.EncodeToString(h[:]) != digest {
		t.Fatal("wrong artifact")
	}
}

func TestPythonBrokerRejectsUntrustedRoutesWithoutNetwork(t *testing.T) {
	b, filename, digest, calls := pythonBrokerFixture(t, nil)
	paths := []string{"/python/simple/", "/python/simple/Demo-Pkg/", "/python/simple/a.b/", "/python/simple/a%2fb/", "//python/simple/foo/", "/python/simple/foo/?secret=1", "/python/simple/foo/?", "http://pypi.org/python/simple/foo/", "/python/wheel/demo-pkg/1.2.4/" + digest + "/" + filename, "/python/wheel/demo-pkg/1.2.3/" + strings.Repeat("z", 64) + "/" + filename, "/python/wheel/demo-pkg/1.2.3/" + digest + "/../../secrets"}
	for _, p := range paths {
		w := httptest.NewRecorder()
		b.serveHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 403 {
			t.Errorf("%s: %d", p, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "HEAD", "CONNECT", "DELETE"} {
		w := httptest.NewRecorder()
		b.serveHTTP(w, httptest.NewRequest(method, "/python/simple/demo-pkg/", nil))
		if w.Code != 403 {
			t.Fatal(method, w.Code)
		}
	}
	if len(*calls) != 0 {
		t.Fatal("rejected input reached registry", *calls)
	}
}

func TestPythonBrokerWheelURLAndCandidatePolicy(t *testing.T) {
	filename := "a-1-py3-none-any.whl"
	for _, raw := range []string{"http://files.pythonhosted.org/packages/" + filename, "https://files.pythonhosted.org.evil.test/packages/" + filename, "https://user:pass@files.pythonhosted.org/packages/" + filename, "https://files.pythonhosted.org:443/packages/" + filename, "https://files.pythonhosted.org/packages/x/../" + filename, "https://files.pythonhosted.org/packages/" + filename + "?secret=1", "https://files.pythonhosted.org/packages/" + filename + "#fragment", "https://files.pythonhosted.org/packages/%2e%2e/" + filename, "https://files.pythonhosted.org/other/" + filename} {
		if _, err := autoPythonWheelURL(raw, filename); err == nil {
			t.Error("accepted", raw)
		}
	}
	for _, name := range []string{"a-1-invalidbuild-cp313-cp313-linux_x86_64.whl", "a-1-cp313--linux_x86_64.whl", "a-1-cp313-cp313-linux..x86_64.whl", "a-1-py3-none-any.whl/other", "../a-1-py3-none-any.whl"} {
		if _, _, ok := autoPythonWheelIdentity(name); ok {
			t.Error("accepted", name)
		}
	}
}

func TestPythonBrokerNeverReturnsPartialOrUnverifiedSuccess(t *testing.T) {
	for _, scenario := range []string{"corrupt", "truncated", "oversized", "encoding", "missing", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			b, filename, digest, calls := pythonBrokerFixture(t, func(host string, res *http.Response) {
				if host != "files.pythonhosted.org" {
					return
				}
				switch scenario {
				case "corrupt":
					res.Body = io.NopCloser(strings.NewReader("different"))
					res.ContentLength = 9
				case "truncated":
					res.ContentLength++
				case "oversized":
					res.ContentLength = autoPythonWheelLimit + 1
				case "encoding":
					res.Header.Set("Content-Encoding", "gzip")
				case "missing":
					res.StatusCode = 404
				case "redirect":
					res.StatusCode = 302
					res.Header.Set("Location", "https://evil.test/")
				}
			})
			b.client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
			w := httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", "/python/wheel/demo-pkg/1.2.3/"+digest+"/"+filename, nil))
			want := 502
			if scenario == "oversized" {
				want = 422
			}
			if w.Code != want || len(*calls) != 2 || strings.Contains(w.Body.String(), "wheel bytes") {
				t.Fatal(w.Code, w.Body.String(), *calls)
			}
		})
	}
}

func TestPythonBrokerRequiresRegistryMembership(t *testing.T) {
	b, filename, _, calls := pythonBrokerFixture(t, nil)
	w := httptest.NewRecorder()
	b.serveHTTP(w, httptest.NewRequest("GET", "/python/wheel/demo-pkg/1.2.3/"+strings.Repeat("a", 64)+"/"+filename, nil))
	if w.Code != 502 || len(*calls) != 1 {
		t.Fatal(w.Code, *calls)
	}
}

func TestPythonBrokerRejectsMalformedRegistryMetadata(t *testing.T) {
	for _, scenario := range []string{"html", "major-version", "wrong-project", "duplicate", "external-host", "missing-hash"} {
		t.Run(scenario, func(t *testing.T) {
			b, _, _, calls := pythonBrokerFixture(t, func(host string, res *http.Response) {
				if host != "pypi.org" {
					return
				}
				if scenario == "html" {
					res.Header.Set("Content-Type", "text/html")
					return
				}
				var data map[string]any
				json.NewDecoder(res.Body).Decode(&data)
				files := data["files"].([]any)
				file := files[0].(map[string]any)
				switch scenario {
				case "major-version":
					data["meta"] = map[string]string{"api-version": "2.0"}
				case "wrong-project":
					data["name"] = "other"
				case "duplicate":
					data["files"] = append(files, file)
				case "external-host":
					file["url"] = "https://evil.test/packages/" + file["filename"].(string)
				case "missing-hash":
					delete(file, "hashes")
				}
				raw, _ := json.Marshal(data)
				res.Body = io.NopCloser(bytes.NewReader(raw))
				res.ContentLength = int64(len(raw))
			})
			w := httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", "/python/simple/demo-pkg/", nil))
			if w.Code != 502 || len(*calls) != 1 || strings.Contains(w.Body.String(), "<a href") {
				t.Fatal(w.Code, w.Body.String(), *calls)
			}
		})
	}
}

func TestPythonBrokerBudgetsAndBoundedBodies(t *testing.T) {
	b, _, _, calls := pythonBrokerFixture(t, nil)
	b.requests = autoPythonRequestLimit
	if _, _, err := b.fetch(context.Background(), "https://pypi.org/simple/demo-pkg/", "application/json", 10); err == nil {
		t.Fatal("request budget ignored")
	}
	b.requests = 0
	b.bytes = autoPythonTransferLimit - 9
	if _, _, err := b.fetch(context.Background(), "https://pypi.org/simple/demo-pkg/", "application/json", 10); err == nil {
		t.Fatal("byte budget ignored")
	}
	if len(*calls) != 0 {
		t.Fatal(*calls)
	}
	b.bytes = 0
	b.client.Transport = pythonBrokerTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("12345678901")), Header: http.Header{}, ContentLength: -1}, nil
	})
	if _, _, err := b.fetch(context.Background(), "https://pypi.org/simple/demo-pkg/", "application/json", 10); err == nil || b.bytes != 11 {
		t.Fatal("unbounded unknown-length response", err, b.bytes)
	}
	for i := 0; i < cap(b.slots); i++ {
		b.slots <- struct{}{}
	}
	w := httptest.NewRecorder()
	b.serveHTTP(w, httptest.NewRequest("GET", "/python/simple/demo-pkg/", nil))
	if w.Code != 429 {
		t.Fatal(w.Code)
	}
}

// Opt-in: read-only public metadata and one small wheel, no package execution.
func TestPythonBrokerLiveRegistry(t *testing.T) {
	if os.Getenv("LECTERN_PYTHON_BROKER_LIVE") != "1" {
		t.Skip("opt-in public registry read")
	}
	h := autoPythonDependencyBroker()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", "/python/simple/iniconfig/", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	body := w.Body.String()
	needle := "/python/wheel/iniconfig/2.3.0/"
	start := strings.Index(body, needle)
	if start < 0 {
		t.Fatal("expected compatible fixture release absent")
	}
	end := strings.Index(body[start:], "#sha256=")
	if end < 0 {
		t.Fatal("wheel digest missing")
	}
	path := body[start : start+end]
	w = httptest.NewRecorder()
	h(w, httptest.NewRequest("GET", path, nil))
	if w.Code != 200 || w.Body.Len() < 1000 {
		t.Fatal(w.Code, w.Body.String())
	}
	t.Logf("read-only PyPI proof: %s, %d hash-verified bytes", path, w.Body.Len())
}

func TestPythonBrokerAuthoritativeMissingPackageIsNotTransportFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		simple bool
		want   int
	}{
		{"missing project", 404, true, 404}, {"registry server error", 503, true, 502}, {"release disappeared", 404, false, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, filename, digest, calls := pythonBrokerFixture(t, func(_ string, r *http.Response) { r.StatusCode = tc.status })
			route := "/python/simple/demo-pkg/"
			if !tc.simple {
				route = "/python/wheel/demo-pkg/1.2.3/" + digest + "/" + filename
			}
			w := httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", route, nil))
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if (w.Header().Get("X-Lectern-Python-Registry") == "package-not-found") != (tc.want == 404) {
				t.Fatal("absence marker incorrect")
			}
			if len(*calls) != 1 || !strings.HasPrefix((*calls)[0], "https://pypi.org/") {
				t.Fatal("absence was not authoritative", calls)
			}
		})
	}
}

func TestPythonBrokerNativeCandidatesRemainRegistryAndHashBound(t *testing.T) {
	for _, filename := range []string{
		"demo_pkg-1.2.3-py3-none-manylinux1_x86_64.manylinux2014_x86_64.whl",
		"demo_pkg-1.2.3-cp39-abi3-manylinux_2_28_x86_64.whl",
		"demo_pkg-1.2.3-2abc-py3-none-manylinux1_x86_64.whl",
		// Compatibility is deliberately owned by the resolver/helper, not broker.
		"demo_pkg-1.2.3-cp313-cp313-win_amd64.whl",
	} {
		t.Run(filename, func(t *testing.T) {
			b, _, digest, _ := pythonBrokerNamedFixture(t, filename, nil)
			w := httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", "/python/simple/demo-pkg/", nil))
			route := "/python/wheel/demo-pkg/1.2.3/" + digest + "/" + filename
			if w.Code != 200 || !strings.Contains(w.Body.String(), route+"#sha256="+digest) {
				t.Fatal(w.Code, w.Body.String())
			}
			// Restart loses no authority: exact release membership is fetched again.
			b, _, _, calls := pythonBrokerNamedFixture(t, filename, nil)
			w = httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", route, nil))
			if w.Code != 200 || len(*calls) != 2 {
				t.Fatal(w.Code, w.Body.String(), *calls)
			}
			sum := sha256.Sum256(w.Body.Bytes())
			if hex.EncodeToString(sum[:]) != digest {
				t.Fatal("native bytes not hash verified")
			}
			w = httptest.NewRecorder()
			b.serveHTTP(w, httptest.NewRequest("GET", strings.Replace(route, digest, strings.Repeat("a", 64), 1), nil))
			if w.Code != 502 {
				t.Fatal("incorrect native digest admitted", w.Code)
			}
		})
	}
}

func TestPythonBrokerLiveNativeRegistry(t *testing.T) {
	if os.Getenv("LECTERN_PYTHON_BROKER_LIVE") != "1" {
		t.Skip("opt-in public registry read")
	}
	for _, tc := range []struct{ name, version, tag string }{
		{"playwright", "1.62.0", "py3-none-manylinux1_x86_64"},
		{"pydantic-core", "2.33.2", "cp313-cp313-manylinux_2_17_x86_64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := autoPythonDependencyBroker()
			w := httptest.NewRecorder()
			h(w, httptest.NewRequest("GET", "/python/simple/"+tc.name+"/", nil))
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var route string
			for _, line := range strings.Split(w.Body.String(), "\n") {
				if strings.Contains(line, "/python/wheel/"+tc.name+"/"+tc.version+"/") && strings.Contains(line, tc.tag) {
					start := strings.Index(line, `href="`) + len(`href="`)
					end := strings.Index(line[start:], "#sha256=")
					if start >= len(`href="`) && end > 0 {
						route = line[start : start+end]
						break
					}
				}
			}
			if route == "" {
				t.Fatal("expected actual native wheel absent")
			}
			w = httptest.NewRecorder()
			h(w, httptest.NewRequest("GET", route, nil))
			if w.Code != 200 || w.Body.Len() < 100000 {
				t.Fatal(w.Code, w.Body.String())
			}
			t.Logf("read-only native PyPI proof: %s, %d hash-verified bytes", route, w.Body.Len())
		})
	}
}

func TestPythonBrokerOversizeIsTypedUnsupported(t *testing.T) {
	for _, declared := range []bool{true, false} {
		b, _, _, _ := pythonBrokerFixture(t, func(host string, res *http.Response) {
			if host != "pypi.org" {
				return
			}
			if declared {
				res.ContentLength = autoPythonMetadataLimit + 1
			} else {
				res.ContentLength = -1
				res.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", autoPythonMetadataLimit+1)))
			}
		})
		w := httptest.NewRecorder()
		b.serveHTTP(w, httptest.NewRequest("GET", "/python/simple/demo-pkg/", nil))
		if w.Code != 422 || w.Header().Get("X-Lectern-Python-Registry") != "unsupported-size" {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
