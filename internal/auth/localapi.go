package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// DefaultSocket is tailscaled's LocalAPI socket on a standard Linux install.
// LECTERN_TAILSCALE_SOCKET overrides it (e.g. for a userspace/tsnet daemon).
const DefaultSocket = "/var/run/tailscale/tailscaled.sock"

// WhoIsNode and WhoIsUser mirror the subset of tailscaled's whois JSON this
// package needs. Hand-rolled rather than importing tailscale.com/client/local
// (and the tsnet dependency tree behind it) for two read-only HTTP calls.
type WhoIsNode struct {
	Name string   `json:"Name"`
	Tags []string `json:"Tags"`
}

type WhoIsUser struct {
	LoginName string `json:"LoginName"`
}

// WhoIsResponse is GET /localapi/v0/whois?addr=IP:PORT.
type WhoIsResponse struct {
	Node        *WhoIsNode `json:"Node"`
	UserProfile *WhoIsUser `json:"UserProfile"`
}

// StatusSelf is the subset of ipnstate.PeerStatus for this node that
// StatusResponse.Self carries.
type StatusSelf struct {
	UserID       int64    `json:"UserID"`
	TailscaleIPs []string `json:"TailscaleIPs"`
	DNSName      string   `json:"DNSName"`
}

// StatusUser is the subset of tailcfg.UserProfile this package reads.
type StatusUser struct {
	LoginName string `json:"LoginName"`
}

// StatusResponse is the subset of GET /localapi/v0/status this package reads:
// enough to learn who owns this node, and (for the tailscale TLS listener)
// this node's own tailnet addresses and DNS name.
type StatusResponse struct {
	Self *StatusSelf           `json:"Self"`
	User map[string]StatusUser `json:"User"`
}

// OwnerLogin is the login of the account this tailscaled node belongs to
// (Self.UserID looked up in User) — the default allowed identity when
// LECTERN_TAILSCALE_USERS is unset.
func (s *StatusResponse) OwnerLogin() string {
	if s == nil || s.Self == nil {
		return ""
	}
	if u, ok := s.User[fmt.Sprintf("%d", s.Self.UserID)]; ok {
		return u.LoginName
	}
	return ""
}

// LocalAPI is tailscaled's local control socket, narrowed to what identity
// resolution needs. An interface so tests use a fake instead of a real
// daemon.
type LocalAPI interface {
	WhoIs(ctx context.Context, addr string) (*WhoIsResponse, error)
	Status(ctx context.Context) (*StatusResponse, error)
	// Cert fetches a TLS certificate/key pair for dnsName from tailscaled's
	// own cert store (issuing or renewing it via Let's Encrypt as needed).
	// Both return values are PEM-encoded.
	Cert(ctx context.Context, dnsName string) (certPEM, keyPEM []byte, err error)
}

type localAPIClient struct {
	http *http.Client
}

// NewLocalAPIClient dials tailscaled's LocalAPI over its unix socket. Every
// request targets the fixed host "local-tailscaled.sock" regardless of the
// socket's real filesystem path — that is the name tailscaled's LocalAPI
// itself expects over the socket.
func NewLocalAPIClient(socket string) LocalAPI {
	if socket == "" {
		socket = DefaultSocket
	}
	return &localAPIClient{http: &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
		// No blanket Timeout here: a first-time cert issuance can legitimately
		// take tens of seconds (tailscaled talking to Let's Encrypt), far
		// longer than a whois/status call should ever take. Each call site
		// bounds itself with its own context deadline instead.
	}}
}

func (c *localAPIClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscaled localapi %s: status %d", path, resp.StatusCode)
	}
	return body, nil
}

func (c *localAPIClient) do(ctx context.Context, path string, out any) error {
	body, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *localAPIClient) WhoIs(ctx context.Context, addr string) (*WhoIsResponse, error) {
	var out WhoIsResponse
	if err := c.do(ctx, "/localapi/v0/whois?addr="+url.QueryEscape(addr), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *localAPIClient) Status(ctx context.Context) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.do(ctx, "/localapi/v0/status", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Cert fetches this node's cert+key pair for dnsName. `type=pair` asks
// tailscaled for the leaf certificate chain immediately followed by the
// private key, both PEM; splitCertPair separates them by block type rather
// than assuming that exact order, so it isn't brittle to how tailscaled
// concatenates them.
func (c *localAPIClient) Cert(ctx context.Context, dnsName string) ([]byte, []byte, error) {
	body, err := c.get(ctx, "/localapi/v0/cert/"+url.PathEscape(dnsName)+"?type=pair")
	if err != nil {
		return nil, nil, err
	}
	return splitCertPair(body)
}

// splitCertPair separates a PEM blob containing both a certificate chain and
// a private key into the two, by decoding every block and sorting it by type.
func splitCertPair(pair []byte) (certPEM, keyPEM []byte, err error) {
	var certs, keys bytes.Buffer
	rest := pair
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		encoded := pem.EncodeToMemory(block)
		if strings.Contains(block.Type, "PRIVATE KEY") {
			keys.Write(encoded)
		} else {
			certs.Write(encoded)
		}
	}
	if certs.Len() == 0 || keys.Len() == 0 {
		return nil, nil, fmt.Errorf("tailscaled cert response did not contain both a certificate and a key")
	}
	return certs.Bytes(), keys.Bytes(), nil
}
