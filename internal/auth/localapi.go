package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
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

// StatusResponse is the subset of GET /localapi/v0/status this package reads:
// enough to learn who owns this node.
type StatusResponse struct {
	Self *struct {
		UserID int64 `json:"UserID"`
	} `json:"Self"`
	User map[string]struct {
		LoginName string `json:"LoginName"`
	} `json:"User"`
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
		Timeout: 5 * time.Second,
	}}
}

func (c *localAPIClient) do(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock"+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tailscaled localapi %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
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
