package api

// The Node registry broker is for the fixed isolated provisioner only. It is
// intentionally not wired into an ordinary worker's research/network surface.
import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const autoNodeMetadataLimit = 16 << 20
const autoNodeTarballLimit = 64 << 20
const autoNodeTransferLimit = 384 << 20
const autoNodeRegistry = "https://registry.npmjs.org/"
const autoNodeMirror = "http://127.0.0.1:18080/npm/"

var autoNodeAbsent = errors.New("npm registry identity not found")
var autoNodeUnsupported = errors.New("npm dependency unsupported")

func autoNodeInvalid(reason string) error { return fmt.Errorf("%w: %s", autoNodeUnsupported, reason) }

var autoNodeName = regexp.MustCompile(`^(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
var autoNodeVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

type autoNodeBroker struct {
	client   *http.Client
	mu       sync.Mutex
	requests int
	bytes    int64
	slots    chan struct{}
}

func autoNodeDependencyBroker() http.HandlerFunc {
	tr := &http.Transport{DialContext: autoPublicDial, ResponseHeaderTimeout: 20 * time.Second, DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: 64 << 10}
	b := &autoNodeBroker{client: &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("npm redirects refused") }}, slots: make(chan struct{}, 4)}
	return b.serveHTTP
}

func (b *autoNodeBroker) fetch(ctx context.Context, target, accept string, limit int64) ([]byte, error) {
	b.mu.Lock()
	if b.requests >= 512 || b.bytes+limit > autoNodeTransferLimit {
		b.mu.Unlock()
		return nil, autoNodeInvalid("npm provisioner request/transfer bound exhausted")
	}
	b.requests++
	b.bytes += limit
	b.mu.Unlock()
	var received int64
	defer func() { b.mu.Lock(); b.bytes -= limit - received; b.mu.Unlock() }()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "Lectern-Offline-Node/1")
	res, e := b.client.Do(req)
	if e != nil {
		return nil, errors.New("npm registry transport unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, autoNodeAbsent
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("npm registry HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit || (res.Header.Get("Content-Encoding") != "" && res.Header.Get("Content-Encoding") != "identity") {
		return nil, autoNodeInvalid("npm response exceeds bounds")
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, limit+1))
	received = int64(len(raw))
	if e != nil || received > limit || (res.ContentLength >= 0 && received != res.ContentLength) {
		return nil, errors.New("npm response truncated or oversized")
	}
	return raw, nil
}

func autoNodeTarPath(name, version string) string {
	short := name[strings.LastIndex(name, "/")+1:]
	return name + "/-/" + short + "-" + version + ".tgz"
}
func autoNodeIntegrity(raw string) ([]byte, error) {
	if !strings.HasPrefix(raw, "sha512-") {
		return nil, autoNodeInvalid("npm SHA512 required")
	}
	b, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(raw, "sha512-"))
	if e != nil || len(b) != 64 || "sha512-"+base64.StdEncoding.EncodeToString(b) != raw {
		return nil, autoNodeInvalid("npm integrity invalid")
	}
	return b, nil
}
func autoNodeRewriteVersion(name, version string, raw json.RawMessage) (json.RawMessage, error) {
	if !autoNodeName.MatchString(name) || len(name) > 214 || !autoNodeVersion.MatchString(version) || len(version) > 128 {
		return nil, autoNodeInvalid("npm identity invalid")
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return nil, autoNodeInvalid("npm version metadata invalid")
	}
	var actualName, actualVersion string
	_ = json.Unmarshal(v["name"], &actualName)
	_ = json.Unmarshal(v["version"], &actualVersion)
	if actualName != name || actualVersion != version {
		return nil, autoNodeInvalid("npm version identity mismatch")
	}
	var dist struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity,omitempty"`
		Shasum    string `json:"shasum,omitempty"`
	}
	if json.Unmarshal(v["dist"], &dist) != nil {
		return nil, autoNodeInvalid("npm dist missing")
	}
	// Preserve legacy versions in the resolver catalog. Rejecting a whole
	// packument for old SHA1-only releases would block modern supported pins.
	// Any selected download still requires SHA512 in tarball(), and the final
	// frozen lock validator independently requires it.
	expected := autoNodeRegistry + autoNodeTarPath(name, version)
	if dist.Tarball != expected {
		return nil, autoNodeInvalid("npm noncanonical artifact URL")
	}
	dist.Tarball = autoNodeMirror + autoNodeTarPath(name, version)
	v["dist"], _ = json.Marshal(dist)
	// Keep resolver inputs; omit unneeded prose and external URLs. Dependency
	// values remain npm inputs, never host commands. Unsupported URLs cannot
	// escape the provisioner's private network namespace.
	allowed := map[string]bool{}
	for _, key := range []string{"name", "version", "dist", "dependencies", "optionalDependencies", "peerDependencies", "peerDependenciesMeta", "bundledDependencies", "bundleDependencies", "engines", "os", "cpu", "libc", "bin", "hasInstallScript", "deprecated"} {
		allowed[key] = true
	}
	for key := range v {
		if !allowed[key] {
			delete(v, key)
		}
	}
	return json.Marshal(v)
}
func (b *autoNodeBroker) metadata(ctx context.Context, name string) ([]byte, error) {
	raw, e := b.fetch(ctx, autoNodeRegistry+name, "application/vnd.npm.install-v1+json", autoNodeMetadataLimit)
	if e != nil {
		return nil, e
	}
	var doc struct {
		Name     string                     `json:"name"`
		Versions map[string]json.RawMessage `json:"versions"`
		Tags     map[string]string          `json:"dist-tags"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Name != name || len(doc.Versions) == 0 || len(doc.Versions) > 20000 {
		return nil, autoNodeInvalid("npm packument invalid")
	}
	// Never silently remove an unsupported version: that changes resolution.
	for version, value := range doc.Versions {
		rewritten, e := autoNodeRewriteVersion(name, version, value)
		if e != nil {
			return nil, e
		}
		doc.Versions[version] = rewritten
	}
	return json.Marshal(doc)
}
func (b *autoNodeBroker) tarball(ctx context.Context, name, version string) ([]byte, error) {
	meta, e := b.fetch(ctx, autoNodeRegistry+name+"/"+version, "application/json", autoNodeMetadataLimit)
	if e != nil {
		return nil, e
	}
	if _, e = autoNodeRewriteVersion(name, version, meta); e != nil {
		return nil, e
	}
	var v struct {
		Dist struct {
			Integrity string `json:"integrity"`
		} `json:"dist"`
	}
	_ = json.Unmarshal(meta, &v)
	expected, e := autoNodeIntegrity(v.Dist.Integrity)
	if e != nil {
		return nil, e
	}
	raw, e := b.fetch(ctx, autoNodeRegistry+autoNodeTarPath(name, version), "application/octet-stream", autoNodeTarballLimit)
	if e != nil {
		return nil, e
	}
	actual := sha512.Sum512(raw)
	if string(actual[:]) != string(expected) {
		return nil, autoNodeInvalid("npm artifact integrity mismatch")
	}
	return raw, nil
}
func (b *autoNodeBroker) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.URL.IsAbs() || r.URL.RawQuery != "" || r.URL.ForceQuery || r.URL.Fragment != "" || !strings.HasPrefix(r.URL.Path, "/npm/") {
		http.Error(w, "fixed read-only npm protocol required", 403)
		return
	}
	decoded := strings.TrimPrefix(r.URL.Path, "/npm/")
	if len(decoded) > 512 || strings.ContainsAny(decoded, "\\\x00\r\n%") || strings.Contains("/"+decoded+"/", "/../") {
		http.Error(w, "invalid npm path", 403)
		return
	}
	name, version := decoded, ""
	if p := strings.Index(decoded, "/-/"); p >= 0 {
		name = decoded[:p]
		short := name[strings.LastIndex(name, "/")+1:]
		file := decoded[p+3:]
		if !strings.HasPrefix(file, short+"-") || !strings.HasSuffix(file, ".tgz") {
			http.Error(w, "invalid npm artifact", 403)
			return
		}
		version = strings.TrimSuffix(strings.TrimPrefix(file, short+"-"), ".tgz")
	}
	if !autoNodeName.MatchString(name) || len(name) > 214 || (version != "" && !autoNodeVersion.MatchString(version)) {
		http.Error(w, "invalid npm identity", 403)
		return
	}
	// URL round-trip forbids encoded query/path ambiguity; scoped %2f is npm's
	// normal protocol and is safe because upstream paths are reconstructed.
	if _, e := url.ParseRequestURI(r.RequestURI); e != nil {
		http.Error(w, "invalid request URI", 403)
		return
	}
	select {
	case b.slots <- struct{}{}:
		defer func() { <-b.slots }()
	default:
		http.Error(w, "npm broker busy", 429)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	var raw []byte
	var e error
	if version == "" {
		raw, e = b.metadata(ctx, name)
		w.Header().Set("Content-Type", "application/json")
	} else {
		raw, e = b.tarball(ctx, name, version)
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if e != nil {
		status := http.StatusBadGateway
		if errors.Is(e, autoNodeAbsent) {
			status = http.StatusNotFound
			w.Header().Set("X-Lectern-Node-Registry", "package-not-found")
		}
		if errors.Is(e, autoNodeUnsupported) {
			status = http.StatusUnprocessableEntity
			w.Header().Set("X-Lectern-Node-Registry", "unsupported")
		}
		http.Error(w, e.Error(), status)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(raw)
}
