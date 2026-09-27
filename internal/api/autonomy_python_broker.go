package api

// This socket belongs only to the fixed, isolated Python dependency provisioner.
// It is not a worker research proxy. All upstream locations are reconstructed
// from registry identities, never supplied as URLs by the client.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	// pydantic-core has >14,000 wheel records (~9 MiB); keep a bounded
	// 16 MiB envelope while retaining concurrency and aggregate transfer limits.
	autoPythonMetadataLimit = 16 << 20
	autoPythonWheelLimit    = 64 << 20
	autoPythonTransferLimit = 384 << 20
	autoPythonRequestLimit  = 256
)

var (
	autoPythonUnsupportedSize = errors.New("Python registry artifact exceeds supported size")
	autoPythonPackageAbsent   = errors.New("Python registry package not found")
	autoPythonName            = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,126}[a-z0-9])?$`)
	autoPythonVersion         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.!+_]{0,127}$`)
	autoPythonFilename        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.!+\-]{0,239}\.whl$`)
	autoPythonTag             = regexp.MustCompile(`^[A-Za-z0-9_]+(\.[A-Za-z0-9_]+)*$`)
	autoPythonBuild           = regexp.MustCompile(`^[0-9][A-Za-z0-9_]*$`)
	autoPythonNormalize       = regexp.MustCompile(`[-_.]+`)
)

type autoPythonRegistryFile struct {
	Filename       string            `json:"filename"`
	URL            string            `json:"url"`
	Hashes         map[string]string `json:"hashes"`
	Digests        map[string]string `json:"digests"`
	RequiresPython string            `json:"requires-python"`
	Yanked         json.RawMessage   `json:"yanked"`
	PackageType    string            `json:"packagetype"`
}

type autoPythonBroker struct {
	client   *http.Client
	mu       sync.Mutex
	requests int
	bytes    int64 // Includes outstanding reservations, so concurrent requests cannot overrun it.
	slots    chan struct{}
}

func autoPythonDependencyBroker() http.HandlerFunc {
	transport := &http.Transport{DialContext: autoPublicDial, ResponseHeaderTimeout: 20 * time.Second,
		DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10}
	b := &autoPythonBroker{client: &http.Client{Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Python registry redirects refused") }}, slots: make(chan struct{}, 4)}
	return b.serveHTTP
}

func (b *autoPythonBroker) fetch(ctx context.Context, target, accept string, limit int64) ([]byte, string, error) {
	b.mu.Lock()
	if b.requests >= autoPythonRequestLimit || b.bytes+limit > autoPythonTransferLimit {
		b.mu.Unlock()
		return nil, "", errors.New("Python registry transfer budget exhausted")
	}
	b.requests++
	b.bytes += limit
	b.mu.Unlock()
	var received int64
	defer func() { b.mu.Lock(); b.bytes -= limit - received; b.mu.Unlock() }()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "Lectern-Offline-Python/1")
	res, err := b.client.Do(req)
	if err != nil {
		return nil, "", errors.New("Python registry transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound && req.URL.Scheme == "https" && req.URL.Host == "pypi.org" && strings.HasPrefix(req.URL.Path, "/simple/") {
		return nil, "", autoPythonPackageAbsent
	}
	if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Python registry returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit {
		return nil, "", autoPythonUnsupportedSize
	}
	if res.Header.Get("Content-Encoding") != "" && res.Header.Get("Content-Encoding") != "identity" {
		return nil, "", errors.New("Python registry response exceeds supported size or encoding")
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	received = int64(len(raw))
	if received > limit {
		return nil, "", autoPythonUnsupportedSize
	}
	if err != nil || (res.ContentLength >= 0 && received != res.ContentLength) {
		return nil, "", errors.New("Python registry response truncated or oversized")
	}
	return raw, res.Header.Get("Content-Type"), nil
}

func autoPythonJSON(raw []byte, contentType string, out any) error {
	kind, _, err := mime.ParseMediaType(contentType)
	if err != nil || (kind != "application/json" && kind != "application/vnd.pypi.simple.v1+json") {
		return errors.New("Python registry did not return supported JSON")
	}
	return json.Unmarshal(raw, out)
}

// Filename filtering is only candidate selection. The isolated installer must
// also verify actual WHEEL tags, Requires-Python, METADATA, RECORD and members.
func autoPythonWheelIdentity(filename string) (string, string, bool) {
	if !autoPythonFilename.MatchString(filename) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSuffix(filename, ".whl"), "-")
	if len(parts) != 5 && len(parts) != 6 {
		return "", "", false
	}
	name := autoPythonNormalize.ReplaceAllString(strings.ToLower(parts[0]), "-")
	if !autoPythonName.MatchString(name) || !autoPythonVersion.MatchString(parts[1]) ||
		!autoPythonTag.MatchString(parts[len(parts)-3]) || !autoPythonTag.MatchString(parts[len(parts)-2]) || !autoPythonTag.MatchString(parts[len(parts)-1]) ||
		(len(parts) == 6 && !autoPythonBuild.MatchString(parts[2])) {
		return "", "", false
	}
	return name, parts[1], true
}

func autoPythonDigest(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func autoPythonWheelURL(raw, filename string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "files.pythonhosted.org" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" ||
		!strings.HasPrefix(u.Path, "/packages/") || path.Clean(u.Path) != u.Path || path.Base(u.Path) != filename ||
		strings.ContainsAny(u.Path, "\\\x00\r\n%") {
		return "", errors.New("unsupported Python registry wheel location")
	}
	return u.String(), nil
}

func (b *autoPythonBroker) simple(ctx context.Context, name string) ([]byte, error) {
	raw, ct, err := b.fetch(ctx, "https://pypi.org/simple/"+name+"/", "application/vnd.pypi.simple.v1+json", autoPythonMetadataLimit)
	if err != nil {
		return nil, err
	}
	var index struct {
		Name string `json:"name"`
		Meta struct {
			Version string `json:"api-version"`
		} `json:"meta"`
		Files []autoPythonRegistryFile `json:"files"`
	}
	if autoPythonJSON(raw, ct, &index) != nil || autoPythonNormalize.ReplaceAllString(strings.ToLower(index.Name), "-") != name ||
		!strings.HasPrefix(index.Meta.Version, "1.") {
		return nil, errors.New("invalid Python simple registry response")
	}
	if len(index.Files) > 20000 {
		return nil, autoPythonUnsupportedSize
	}
	var out strings.Builder
	out.WriteString("<!doctype html><html><head><meta name=\"pypi:repository-version\" content=\"1.0\"></head><body>\n")
	seen := make(map[string]bool)
	for _, file := range index.Files {
		packageName, version, ok := autoPythonWheelIdentity(file.Filename)
		if !ok || packageName != name {
			continue
		}
		digest := file.Hashes["sha256"]
		if !autoPythonDigest(digest) || len(file.RequiresPython) > 512 {
			return nil, errors.New("invalid wheel metadata")
		}
		if _, err = autoPythonWheelURL(file.URL, file.Filename); err != nil {
			return nil, err
		}
		if seen[file.Filename] {
			return nil, errors.New("duplicate wheel filename")
		}
		seen[file.Filename] = true
		link := "/python/wheel/" + name + "/" + version + "/" + digest + "/" + file.Filename + "#sha256=" + digest
		fmt.Fprintf(&out, "<a href=\"%s\"", html.EscapeString(link))
		if file.RequiresPython != "" {
			fmt.Fprintf(&out, " data-requires-python=\"%s\"", html.EscapeString(file.RequiresPython))
		}
		if len(file.Yanked) > 0 && string(file.Yanked) != "false" {
			var reason string
			if string(file.Yanked) != "true" && json.Unmarshal(file.Yanked, &reason) != nil {
				return nil, errors.New("invalid yanked metadata")
			}
			if len(reason) > 4096 {
				return nil, errors.New("oversized yanked metadata")
			}
			fmt.Fprintf(&out, " data-yanked=\"%s\"", html.EscapeString(reason))
		}
		fmt.Fprintf(&out, ">%s</a>\n", html.EscapeString(file.Filename))
	}
	out.WriteString("</body></html>\n")
	return []byte(out.String()), nil
}

func (b *autoPythonBroker) wheel(ctx context.Context, name, version, digest, filename string) ([]byte, error) {
	// Reconstruct membership after a service restart, without trusting an old
	// in-memory token or permitting a lock to choose an arbitrary CDN path.
	raw, ct, err := b.fetch(ctx, "https://pypi.org/pypi/"+name+"/"+version+"/json", "application/json", autoPythonMetadataLimit)
	if err != nil {
		return nil, err
	}
	var release struct {
		URLs []autoPythonRegistryFile `json:"urls"`
	}
	if autoPythonJSON(raw, ct, &release) != nil || len(release.URLs) > 20000 {
		return nil, errors.New("invalid Python release response")
	}
	var target string
	for _, file := range release.URLs {
		if file.Filename != filename || file.Digests["sha256"] != digest || file.PackageType != "bdist_wheel" {
			continue
		}
		if target != "" {
			return nil, errors.New("ambiguous Python wheel membership")
		}
		target, err = autoPythonWheelURL(file.URL, filename)
		if err != nil {
			return nil, err
		}
	}
	if target == "" {
		return nil, errors.New("wheel is not a member of the exact registry release")
	}
	raw, _, err = b.fetch(ctx, target, "application/octet-stream", autoPythonWheelLimit)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != digest {
		return nil, errors.New("Python wheel SHA256 mismatch")
	}
	return raw, nil
}

func (b *autoPythonBroker) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.IsAbs() || len(r.URL.Path) > 1024 {
		http.Error(w, "fixed read-only Python dependency protocol required", 403)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
	isSimple := len(parts) == 4 && parts[0] == "python" && parts[1] == "simple" && autoPythonName.MatchString(parts[2]) && parts[3] == ""
	isWheel := len(parts) == 6 && parts[0] == "python" && parts[1] == "wheel" && autoPythonName.MatchString(parts[2]) && autoPythonVersion.MatchString(parts[3]) && autoPythonDigest(parts[4])
	if isWheel {
		name, version, ok := autoPythonWheelIdentity(parts[5])
		isWheel = ok && name == parts[2] && version == parts[3]
	}
	if !isSimple && !isWheel {
		http.Error(w, "unsupported Python dependency path", 403)
		return
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	default:
		http.Error(w, "Python registry concurrency limit", 429)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	var raw []byte
	var err error
	if isSimple {
		raw, err = b.simple(ctx, parts[2])
	} else {
		raw, err = b.wheel(ctx, parts[2], parts[3], parts[4], parts[5])
	}
	if err != nil {
		if isSimple && errors.Is(err, autoPythonPackageAbsent) {
			w.Header().Set("X-Lectern-Python-Registry", "package-not-found")
			http.Error(w, autoPythonPackageAbsent.Error(), http.StatusNotFound)
			return
		}
		if errors.Is(err, autoPythonUnsupportedSize) {
			w.Header().Set("X-Lectern-Python-Registry", "unsupported-size")
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		http.Error(w, err.Error(), 502)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if isSimple {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(raw)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
