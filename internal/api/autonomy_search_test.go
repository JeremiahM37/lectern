package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type autoSearchTransport func(*http.Request) (*http.Response, error)

func (f autoSearchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestRepositorySearchFixedRequestAndEvidence(t *testing.T) {
	body := `{"total_count":1,"incomplete_results":false,"items":[{"full_name":"example/tool","html_url":"https://github.com/example/tool","description":"untrusted repo description","default_branch":"main"}]}`
	client := &http.Client{Transport: autoSearchTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "api.github.com" || r.URL.Path != "/search/repositories" || r.URL.Query().Get("q") != "reproducible experiments" || r.URL.Query().Get("per_page") != "10" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Body != nil {
			t.Fatalf("unsafe request: %#v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	req := httptest.NewRequest("GET", "/research/search?q=reproducible+experiments", nil)
	req.Header.Set("Authorization", "SECRET")
	req.Header.Set("Cookie", "SECRET")
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	serveAutoRepositorySearch(w, req, client, &autoSearchBudget{})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out map[string]any
	if json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal(w.Body.String())
	}
	if out["response_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(body))) || out["query"] != "reproducible experiments" || out["upstream_status"] != float64(200) {
		t.Fatal(out)
	}
	if _, err := time.Parse(time.RFC3339Nano, out["retrieved_at"].(string)); err != nil {
		t.Fatal(err)
	}
}
func TestRepositorySearchFailureIsNotEmptySuccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"rate", 403, `{"message":"rate limit"}`, 502},
		{"redirect", 302, "", 502},
		{"schema", 200, `{}`, 502},
		{"invalid", 200, `<html>`, 502},
		{"oversize", 200, strings.Repeat("x", (2<<20)+1), 502},
		{"empty", 200, `{"total_count":0,"incomplete_results":false,"items":[]}`, 200},
		{"partial", 200, `{"total_count":20,"incomplete_results":true,"items":[]}`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: autoSearchTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			w := httptest.NewRecorder()
			serveAutoRepositorySearch(w, httptest.NewRequest("GET", "/research/search?q=test", nil), client, &autoSearchBudget{})
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.want != 200 && (!strings.Contains(w.Body.String(), `"error"`) || strings.Contains(w.Body.String(), `"results"`)) {
				t.Fatal(w.Body.String())
			}
			if tc.name == "partial" && !strings.Contains(w.Body.String(), `"incomplete_results":true`) {
				t.Fatal(w.Body.String())
			}
		})
	}
}
func TestRepositorySearchRejectsOverridesAndBoundsCalls(t *testing.T) {
	client := &http.Client{Transport: autoSearchTransport(func(*http.Request) (*http.Response, error) { t.Fatal("provider called"); return nil, nil })}
	for _, target := range []string{"/research/search", "/research/search?q=x&url=http://localhost", "/research/search?q=x&q=y", "/research/search?q=%0A", "/research/search?q=" + strings.Repeat("x", 301)} {
		w := httptest.NewRecorder()
		serveAutoRepositorySearch(w, httptest.NewRequest("GET", target, nil), client, &autoSearchBudget{})
		if w.Code != 400 {
			t.Fatal(target, w.Code)
		}
	}
	for _, method := range []string{"POST", "DELETE", "PUT"} {
		w := httptest.NewRecorder()
		serveAutoRepositorySearch(w, httptest.NewRequest(method, "/research/search?q=x", nil), client, &autoSearchBudget{})
		if w.Code != 405 {
			t.Fatal(method, w.Code)
		}
	}
	budget := &autoSearchBudget{}
	now := time.Now()
	for i := 0; i < 4; i++ {
		if !budget.allow(now) {
			t.Fatal(i)
		}
	}
	w := httptest.NewRecorder()
	serveAutoRepositorySearch(w, httptest.NewRequest("GET", "/research/search?q=x", nil), client, budget)
	if w.Code != 429 || w.Header().Get("Retry-After") != "60" {
		t.Fatal(w.Code, w.Header())
	}
	if !budget.allow(now.Add(time.Minute)) {
		t.Fatal("budget did not recover")
	}
}

func TestIssueSearchFixedScopeAndUnicodeExcerpts(t *testing.T) {
	body := strings.Repeat("🙂", 4001)
	payload, _ := json.Marshal(map[string]any{"total_count": 2, "incomplete_results": true, "items": []any{
		map[string]any{"title": "Existing discussion", "number": 42, "html_url": "https://github.com/example/tool/issues/42", "body": body, "comments": 7, "state": "closed"},
		map[string]any{"title": "Related change", "number": 43, "html_url": "https://github.com/example/tool/pull/43", "body": nil, "pull_request": map[string]any{"url": "https://api.github.com/repos/example/tool/pulls/43"}},
	}})
	client := &http.Client{Transport: autoSearchTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Scheme != "https" || r.URL.Host != "api.github.com" || r.URL.Path != "/search/issues" || r.URL.Query().Get("q") != "repo:example/tool extraction" || r.URL.Query().Get("per_page") != "10" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Body != nil {
			t.Fatalf("unsafe issue request: %#v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(payload))), Header: make(http.Header)}, nil
	})}
	req := httptest.NewRequest("GET", "/research/issues?q=repo%3Aexample%2Ftool+extraction", nil)
	req.Header.Set("Authorization", "secret")
	req.Header.Set("Cookie", "secret")
	w := httptest.NewRecorder()
	serveAutoRepositorySearch(w, req, client, &autoSearchBudget{})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var out struct {
		Provider   string `json:"provider"`
		Incomplete bool   `json:"incomplete_results"`
		Results    []struct {
			Body      string `json:"body_excerpt"`
			Truncated bool   `json:"body_truncated"`
			PR        bool   `json:"is_pull_request"`
			Comments  int    `json:"comments_count"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Provider != "github_public_issues" || !out.Incomplete || len(out.Results) != 2 || len([]rune(out.Results[0].Body)) != 4000 || !out.Results[0].Truncated || out.Results[0].PR || out.Results[0].Comments != 7 || !out.Results[1].PR || out.Results[1].Truncated {
		t.Fatalf("bad evidence projection: %+v", out)
	}
}
func TestIssueSearchSharesBudgetAndRejectsMalformedResults(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: autoSearchTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"total_count":1,"incomplete_results":false,"items":[{}]}`)), Header: make(http.Header)}, nil
	})}
	b := &autoSearchBudget{}
	for i := 0; i < 3; i++ {
		b.allow(time.Now())
	} // prior repository searches share this same budget
	w := httptest.NewRecorder()
	serveAutoRepositorySearch(w, httptest.NewRequest("GET", "/research/issues?q=x", nil), client, b)
	if w.Code != 502 || strings.Contains(w.Body.String(), `"results"`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	serveAutoRepositorySearch(w, httptest.NewRequest("GET", "/research/search?q=x", nil), client, b)
	if w.Code != 429 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
	for _, target := range []string{"/research/issues?q=x&url=http://localhost", "/research/issues?q=x&kind=other", "/research/issues?q=x&q=y"} {
		w = httptest.NewRecorder()
		serveAutoRepositorySearch(w, httptest.NewRequest("GET", target, nil), client, &autoSearchBudget{})
		if w.Code != 400 {
			t.Fatal(w.Code, target)
		}
	}
	if calls != 1 {
		t.Fatal("invalid override reached provider")
	}
	w = httptest.NewRecorder()
	serveAutoRepositorySearch(w, httptest.NewRequest("GET", "/research/private?q=x", nil), client, &autoSearchBudget{})
	if w.Code != 404 || calls != 1 {
		t.Fatal(w.Code, calls)
	}
}
