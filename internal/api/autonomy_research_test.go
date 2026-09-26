package api

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type brokenResearchReader struct{}

func (brokenResearchReader) Read(p []byte) (int, error) {
	return copy(p, "PARTIAL_SOURCE"), io.ErrUnexpectedEOF
}

func TestResearchCompleteBoundedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		body io.Reader
		want []byte
	}{
		{"small binary fixture", bytes.NewReader([]byte{'P', 'K', 0, 255}), []byte{'P', 'K', 0, 255}},
		{"empty complete document", strings.NewReader(""), []byte{}},
		{"exact limit", strings.NewReader(strings.Repeat("x", 2<<20)), bytes.Repeat([]byte("x"), 2<<20)},
		{"over limit", strings.NewReader(strings.Repeat("x", (2<<20)+1)), nil},
		{"broken upstream after bytes", brokenResearchReader{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: autoSearchTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != "https://raw.githubusercontent.com/example/repo/rev/fixture.zip" || r.Method != "GET" || r.Body != nil || len(r.Header) != 0 {
					t.Fatalf("unexpected upstream request: %#v", r)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(tc.body), Header: make(http.Header)}, nil
			})}
			r := httptest.NewRequest("GET", "/research?url=https://raw.githubusercontent.com/example/repo/rev/fixture.zip", nil)
			r.Header.Set("Authorization", "worker-secret")
			r.Header.Set("Cookie", "worker-secret")
			w := httptest.NewRecorder()
			serveAutoResearch(w, r, client)
			if tc.want == nil {
				if w.Code != 502 || w.Body.String() != "research response incomplete or exceeds 2 MiB; no source bytes returned\n" {
					t.Fatalf("incomplete evidence exposed as response: status=%d bytes=%d", w.Code, w.Body.Len())
				}
				return
			}
			if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), tc.want) || w.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("complete evidence changed: status=%d bytes=%d", w.Code, w.Body.Len())
			}
		})
	}
}

func TestResearchInterruptedHTTPBody(t *testing.T) {
	// Exercise net/http's real short-body detection rather than only a mock reader.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write([]byte("PARTIAL_SOURCE"))
	}))
	defer upstream.Close()
	client := &http.Client{Transport: autoSearchTransport(func(r *http.Request) (*http.Response, error) {
		return http.Get(upstream.URL)
	})}
	w := httptest.NewRecorder()
	serveAutoResearch(w, httptest.NewRequest("GET", "/research?url=https://go.dev/doc/", nil), client)
	if w.Code != 502 || strings.Contains(w.Body.String(), "PARTIAL_SOURCE") {
		t.Fatalf("short HTTP body returned as evidence: %d %q", w.Code, w.Body.String())
	}
}
