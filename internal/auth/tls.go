package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// certRenewWindow: a cached cert is refetched once it is within this long of
// expiring, rather than waiting for it to actually lapse — tailscaled itself
// renews on a similar schedule, so this mostly just picks up what it already
// rotated.
const certRenewWindow = 7 * 24 * time.Hour

// certFetchTimeout bounds one fetch. Generous: a first-time issuance means
// tailscaled talking to Let's Encrypt, which can legitimately take tens of
// seconds.
const certFetchTimeout = 45 * time.Second

// CertCache serves TLS certificates for one DNS name from tailscaled's own
// cert store, fetching lazily and refreshing them ahead of expiry. Its
// GetCertificate method plugs directly into tls.Config.
type CertCache struct {
	localAPI LocalAPI
	dnsName  string
	log      *slog.Logger

	mu   sync.Mutex
	cert *tls.Certificate
}

// NewCertCache builds a cache for dnsName (tailscaled's LocalAPI cert
// endpoint, not a general ACME client — it only ever asks tailscaled for a
// cert it already manages for this node).
func NewCertCache(la LocalAPI, dnsName string, log *slog.Logger) *CertCache {
	if log == nil {
		log = slog.Default()
	}
	return &CertCache{localAPI: la, dnsName: strings.TrimSuffix(dnsName, "."), log: log}
}

// GetCertificate is a tls.Config.GetCertificate callback.
func (c *CertCache) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	cert := c.cert
	c.mu.Unlock()
	if cert != nil && !needsRenewal(cert) {
		return cert, nil
	}
	return c.refresh()
}

func needsRenewal(cert *tls.Certificate) bool {
	return cert.Leaf == nil || time.Until(cert.Leaf.NotAfter) < certRenewWindow
}

func (c *CertCache) refresh() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Re-check under the lock: a concurrent handshake may have already
	// refreshed while this call waited.
	if c.cert != nil && !needsRenewal(c.cert) {
		return c.cert, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), certFetchTimeout)
	defer cancel()
	certPEM, keyPEM, err := c.localAPI.Cert(ctx, c.dnsName)
	if err != nil {
		if c.cert != nil {
			// A handshake in progress is better served stale than refused.
			c.log.Warn("tailscale TLS cert refresh failed; serving the previous certificate",
				"dnsname", c.dnsName, "err", err)
			return c.cert, nil
		}
		return nil, fmt.Errorf("fetching tailscale cert for %s: %w", c.dnsName, err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parsing tailscale cert pair for %s: %w", c.dnsName, err)
	}
	if len(pair.Certificate) > 0 {
		if leaf, err := x509.ParseCertificate(pair.Certificate[0]); err == nil {
			pair.Leaf = leaf
		}
	}
	c.cert = &pair
	return c.cert, nil
}

// TLSListenerConfig is what's needed to run a second listener bound to this
// node's own tailnet addresses: real tailnet peer addresses arrive on it, so
// whois there is trustworthy without `tailscale serve` fronting it.
type TLSListenerConfig struct {
	// Addrs are host:port strings, one per tailnet address (v4 and v6, when
	// this node has both) on the requested port.
	Addrs []string
	TLS   *tls.Config
	// DNSName is the certificate's name — what a phone on the tailnet types
	// (with the port) to reach this listener.
	DNSName string
}

// TLSListenerConfig resolves this node's tailnet addresses and DNS name from
// tailscaled's status and builds a config for a TLS listener on port,
// certificate served via CertCache. It only makes sense — and only succeeds —
// when the resolver's LocalAPI actually answers, i.e. effectively tailscale
// mode; callers gate on that themselves so a failure here can be logged as a
// specific, actionable warning rather than a generic startup error.
func (a *Resolver) TLSListenerConfig(ctx context.Context, port int) (*TLSListenerConfig, error) {
	st, err := a.localAPI.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailscaled status: %w", err)
	}
	if st.Self == nil || len(st.Self.TailscaleIPs) == 0 {
		return nil, fmt.Errorf("tailscaled status has no TailscaleIPs for this node")
	}
	dnsName := strings.TrimSuffix(st.Self.DNSName, ".")
	if dnsName == "" {
		return nil, fmt.Errorf("tailscaled status has no DNSName for this node")
	}
	addrs := make([]string, 0, len(st.Self.TailscaleIPs))
	for _, ip := range st.Self.TailscaleIPs {
		addrs = append(addrs, net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	cache := NewCertCache(a.localAPI, dnsName, a.log)
	return &TLSListenerConfig{
		Addrs:   addrs,
		TLS:     &tls.Config{GetCertificate: cache.GetCertificate},
		DNSName: dnsName,
	}, nil
}
