package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// testCertPair builds a self-signed cert/key PEM pair expiring in notAfter,
// in the same "cert PEM then key PEM" shape tailscaled's LocalAPI returns.
func testCertPair(t *testing.T, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.example.ts.net"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestSplitCertPairSeparatesByBlockType(t *testing.T) {
	certPEM, keyPEM := testCertPair(t, time.Now().Add(90*24*time.Hour))
	pair := append(append([]byte{}, certPEM...), keyPEM...)
	gotCert, gotKey, err := splitCertPair(pair)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCert) != string(certPEM) || string(gotKey) != string(keyPEM) {
		t.Fatalf("split did not round-trip the pair")
	}
	// Order must not matter: tailscaled's own doc doesn't guarantee it.
	reversed := append(append([]byte{}, keyPEM...), certPEM...)
	gotCert, gotKey, err = splitCertPair(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotCert) != string(certPEM) || string(gotKey) != string(keyPEM) {
		t.Fatalf("split must not depend on block order")
	}
}

func TestSplitCertPairRejectsIncomplete(t *testing.T) {
	certPEM, _ := testCertPair(t, time.Now().Add(time.Hour))
	if _, _, err := splitCertPair(certPEM); err == nil {
		t.Fatal("a cert with no key must be rejected")
	}
}

func TestCertCacheFetchesAndCaches(t *testing.T) {
	certPEM, keyPEM := testCertPair(t, time.Now().Add(90*24*time.Hour))
	la := &fakeLocalAPI{certPEM: certPEM, keyPEM: keyPEM}
	cache := NewCertCache(la, "node.tailnet.ts.net.", nil) // trailing dot must be trimmed
	cert, err := cache.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Leaf == nil {
		t.Fatal("expected a parsed leaf")
	}
	if _, err := cache.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if la.certCalls != 1 {
		t.Fatalf("a fresh cert must be cached, not refetched: %d calls", la.certCalls)
	}
}

func TestCertCacheRefreshesWithinRenewWindow(t *testing.T) {
	certPEM, keyPEM := testCertPair(t, time.Now().Add(24*time.Hour)) // inside the 7-day window
	la := &fakeLocalAPI{certPEM: certPEM, keyPEM: keyPEM}
	cache := NewCertCache(la, "node.tailnet.ts.net", nil)
	if _, err := cache.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.GetCertificate(nil); err != nil {
		t.Fatal(err)
	}
	if la.certCalls != 2 {
		t.Fatalf("a soon-to-expire cert must be refetched every call: %d calls", la.certCalls)
	}
}

func TestCertCacheServesStaleCertOnFetchError(t *testing.T) {
	freshCertPEM, freshKeyPEM := testCertPair(t, time.Now().Add(24*time.Hour))
	la := &fakeLocalAPI{certPEM: freshCertPEM, keyPEM: freshKeyPEM}
	cache := NewCertCache(la, "node.tailnet.ts.net", nil)
	first, err := cache.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	la.certErr = errors.New("tailscaled unreachable")
	second, err := cache.GetCertificate(nil)
	if err != nil {
		t.Fatalf("a fetch failure with a cached cert in hand must not fail the handshake: %v", err)
	}
	if second != first {
		t.Fatal("expected the same (stale) certificate to be served")
	}
}

func TestCertCacheFailsWithNoCachedCertAndNoFetch(t *testing.T) {
	la := &fakeLocalAPI{certErr: errors.New("tailscaled unreachable")}
	cache := NewCertCache(la, "node.tailnet.ts.net", nil)
	if _, err := cache.GetCertificate(nil); err == nil {
		t.Fatal("expected an error with nothing cached and the fetch failing")
	}
}

func TestTLSListenerConfigResolvesAddrsAndCert(t *testing.T) {
	certPEM, keyPEM := testCertPair(t, time.Now().Add(90*24*time.Hour))
	la := &fakeLocalAPI{
		self: &StatusSelf{
			UserID:       1,
			TailscaleIPs: []string{"100.64.1.2", "fd7a:115c:a1e0::1"},
			DNSName:      "node.tailnet.ts.net.",
		},
		certPEM: certPEM, keyPEM: keyPEM,
	}
	r := newTestResolver(ModeTailscale, la, "", "")
	cfg, err := r.TLSListenerConfig(context.Background(), 8443)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"100.64.1.2:8443": true, "[fd7a:115c:a1e0::1]:8443": true}
	if len(cfg.Addrs) != 2 {
		t.Fatalf("addrs: %v", cfg.Addrs)
	}
	for _, a := range cfg.Addrs {
		if !want[a] {
			t.Fatalf("unexpected addr %q in %v", a, cfg.Addrs)
		}
	}
	if cfg.TLS == nil || cfg.TLS.GetCertificate == nil {
		t.Fatal("expected a GetCertificate callback")
	}
	cert, err := cfg.TLS.GetCertificate(nil)
	if err != nil || cert == nil {
		t.Fatalf("resolved TLS config did not produce a certificate via the fake: %v", err)
	}
}

func TestTLSListenerConfigFailsWithoutTailscaleIPs(t *testing.T) {
	la := &fakeLocalAPI{self: &StatusSelf{UserID: 1}} // no TailscaleIPs
	r := newTestResolver(ModeTailscale, la, "", "")
	if _, err := r.TLSListenerConfig(context.Background(), 8443); err == nil {
		t.Fatal("expected an error when the node has no tailnet addresses")
	}
}
