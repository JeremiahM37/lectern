package helpers

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// The hook scripts reach Lectern with urllib (hook.py, lec.py) or curl (the
// codex handlers). Both honour the proxy environment for every host,
// loopback included — which the network=deny sandbox relies on, since its
// only way out is the proxy it exports (internal/isolation). Go's
// ProxyFromEnvironment never proxies loopback, so these clients pick the
// proxy themselves.

// envProxy chooses a proxy the way urllib does, or curl when curl is set:
// curl reads only the lowercase http_proxy, and falls back to all_proxy.
func envProxy(u *url.URL, curl bool) (*url.URL, error) {
	first := func(names ...string) string {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				return v
			}
		}
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	var p string
	switch {
	case curl && scheme == "http":
		p = first("http_proxy")
	case curl:
		p = first(scheme+"_proxy", strings.ToUpper(scheme)+"_PROXY")
	case scheme == "http" && os.Getenv("REQUEST_METHOD") != "":
		p = first("http_proxy") // urllib's httpoxy guard
	default:
		p = first(scheme+"_proxy", strings.ToUpper(scheme)+"_PROXY")
	}
	if p == "" && curl {
		p = first("all_proxy", "ALL_PROXY")
	}
	if p == "" || proxyBypassed(u.Host, first("no_proxy", "NO_PROXY")) {
		return nil, nil
	}
	if !strings.Contains(p, "://") {
		p = "http://" + p
	}
	return url.Parse(p)
}

// proxyBypassed is urllib's proxy_bypass_environment.
func proxyBypassed(host, noProxy string) bool {
	if noProxy == "*" {
		return true
	}
	host = strings.ToLower(host)
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}
	for _, name := range strings.Split(noProxy, ",") {
		name = strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "."))
		if name == "" {
			continue
		}
		if hostOnly == name || host == name ||
			strings.HasSuffix(hostOnly, "."+name) || strings.HasSuffix(host, "."+name) {
			return true
		}
	}
	return false
}

// urllibClient behaves like urlopen(..., timeout=t): t bounds each connect
// and each wait for the server, not the whole exchange.
func urllibClient(t time.Duration) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 func(r *http.Request) (*url.URL, error) { return envProxy(r.URL, false) },
		DialContext:           (&net.Dialer{Timeout: t}).DialContext,
		TLSHandshakeTimeout:   t,
		ResponseHeaderTimeout: t,
		DisableKeepAlives:     true,
	}}
}

// curlClient behaves like `curl -s -m t -X POST` (t <= 0: no limit): one
// total deadline, and redirects are not followed.
func curlClient(t time.Duration) *http.Client {
	return &http.Client{
		Timeout: t,
		Transport: &http.Transport{
			Proxy:             func(r *http.Request) (*url.URL, error) { return envProxy(r.URL, true) },
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// errNotHTTP is urllib refusing a URL it has no handler for — a ValueError,
// which none of the scripts catch.
var errNotHTTP = errors.New("unknown url type")

// urlopen is urllib.request.urlopen for a JSON body: it returns the response
// body of a 2xx answer, an httpError for any other status, and a transport
// error otherwise.
func urlopen(c *http.Client, method, rawURL string, body []byte) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errNotHTTP
	}
	var rd io.Reader
	if body != nil {
		rd = strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, rawURL, rd)
	if err != nil {
		return nil, errNotHTTP
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpError{resp.StatusCode, strings.TrimSpace(strings.TrimPrefix(resp.Status, fmt.Sprint(resp.StatusCode)))}
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, readError{err}
	}
	return data, nil
}

// httpError is urllib's HTTPError.
type httpError struct {
	code   int
	reason string
}

func (e httpError) Error() string { return fmt.Sprintf("HTTP Error %d: %s", e.code, e.reason) }

// readError is a failure after the response started, which urllib does not
// wrap in URLError.
type readError struct{ err error }

func (e readError) Error() string { return pyOSErrorText(e.err) }

// urllibErrorText renders err as str(exception) would for the exception
// urlopen raises — what the scripts print when they give up.
func urllibErrorText(err error) string {
	var he httpError
	var re readError
	switch {
	case errors.As(err, &he):
		return he.Error()
	case errors.As(err, &re):
		return re.Error()
	}
	return "<urlopen error " + pyOSErrorText(err) + ">"
}

// pyOSErrorText renders a network error as CPython's OSError text.
func pyOSErrorText(err error) string {
	var dns *net.DNSError
	var errno syscall.Errno
	var ne net.Error
	switch {
	case errors.As(err, &dns):
		if dns.IsNotFound {
			return "[Errno -2] Name or service not known"
		}
		return "[Errno -3] Temporary failure in name resolution"
	case errors.As(err, &ne) && ne.Timeout():
		return "timed out"
	case errors.As(err, &errno):
		msg := []rune(errno.Error())
		if len(msg) > 0 {
			msg[0] = unicode.ToUpper(msg[0])
		}
		return fmt.Sprintf("[Errno %d] %s", int(errno), string(msg))
	}
	return err.Error()
}
