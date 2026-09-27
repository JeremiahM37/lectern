package host

import (
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flynn/noise"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/pairing"
	"github.com/JeremiahM37/lectern/v2/internal/relay"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// PairingTTL is how long a relay pairing QR code stays valid: the same five
// minutes ordinary pairing uses.
const PairingTTL = pairing.CodeTTL

// clock is overridden by tests that move idle and expiry timers. It is
// atomic because live sessions read it while a test changes it.
var clock atomic.Pointer[func() time.Time]

func setNow(fn func() time.Time) { clock.Store(&fn) }

// Now is the store's notion of the current time.
func Now() time.Time {
	if fn := clock.Load(); fn != nil {
		return (*fn)()
	}
	return time.Now()
}

func nowUnix() float64 { return float64(Now().UnixNano()) / 1e9 }

// ErrNotFound covers an unknown, used or expired pairing code and an unknown
// device alike, so no caller can tell which.
var ErrNotFound = errors.New("relay: not found")

// Device is one phone paired over the relay, as Settings lists it.
type Device struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Fingerprint string  `json:"fingerprint"`
	OwnerLogin  string  `json:"owner_login,omitempty"`
	PairedAt    float64 `json:"paired_at"`
	LastSeenAt  float64 `json:"last_seen_at"`
	Connected   bool    `json:"connected"`

	publicKey string
	routeHash string
	owner     auth.Principal
}

// Principal is the identity requests from this device carry.
func (d Device) Principal() auth.Principal {
	return auth.Principal{Kind: auth.KindRelayDevice, Login: d.owner.Login, Node: d.owner.Node, Human: d.owner.Human}
}

// Store keeps the host's relay keys, pending pairings and paired devices in
// the shared lectern.db.
type Store struct {
	db *store.DB
	mu sync.Mutex
	id *relay.Identity
}

// NewStore binds a Store to db.
func NewStore(db *store.DB) *Store { return &Store{db: db} }

func hashCode(code string) string {
	sum := sha256.Sum256([]byte("lectern-relay-pair-v1\x00" + code))
	return hex.EncodeToString(sum[:])
}

// Identity loads the host's relay keys, creating them on first use.
func (s *Store) Identity() (*relay.Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.id != nil {
		return s.id, nil
	}
	var route, npriv, npub, shell []byte
	err := s.db.QueryRow(`SELECT route_key, noise_private, noise_public, shell_key FROM relay_identity WHERE id=1`).
		Scan(&route, &npriv, &npub, &shell)
	switch {
	case err == nil:
		if len(route) != ed25519.PrivateKeySize || len(shell) != ed25519.PrivateKeySize || len(npriv) != 32 || len(npub) != 32 {
			return nil, errors.New("relay: stored identity is corrupt")
		}
		s.id = &relay.Identity{Route: route, Shell: shell, Noise: noise.DHKey{Private: npriv, Public: npub}}
	case errors.Is(err, sql.ErrNoRows):
		id, err := relay.GenerateIdentity()
		if err != nil {
			return nil, err
		}
		if _, err := s.db.Exec(`INSERT INTO relay_identity(id, route_key, noise_private, noise_public, shell_key, created_at) VALUES(1,?,?,?,?,?)`,
			[]byte(id.Route), id.Noise.Private, id.Noise.Public, []byte(id.Shell), nowUnix()); err != nil {
			return nil, err
		}
		s.id = id
	default:
		return nil, err
	}
	return s.id, nil
}

// Pairing is a freshly minted pairing: the secrets go into the QR code and
// are never stored in the clear.
type Pairing struct {
	Code       string
	RouteToken string
	RouteHash  string
	ExpiresAt  time.Time
}

// MintPairing records a single-use pairing bound to owner, who must already
// have passed CanDecide.
func (s *Store) MintPairing(owner auth.Principal) (Pairing, error) {
	code, err := relay.RandomToken()
	if err != nil {
		return Pairing{}, err
	}
	route, err := relay.RandomToken()
	if err != nil {
		return Pairing{}, err
	}
	p := Pairing{Code: code, RouteToken: route, RouteHash: relay.TokenHash(route), ExpiresAt: Now().Add(PairingTTL)}
	_, err = s.db.Exec(`INSERT INTO relay_pairings(code_hash, route_hash, owner_kind, owner_login, owner_node, owner_human, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?)`, hashCode(code), p.RouteHash, owner.Kind, owner.Login, owner.Node, boolInt(owner.Human),
		nowUnix(), float64(p.ExpiresAt.UnixNano())/1e9)
	if err != nil {
		return Pairing{}, err
	}
	_, _ = s.db.Exec(`DELETE FROM relay_pairings WHERE expires_at < ?`, nowUnix())
	return p, nil
}

// PendingRoutes lists the route hashes of unexpired pairings with the seconds
// each has left, so a reconnecting host can register them again.
func (s *Store) PendingRoutes() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT route_hash, expires_at FROM relay_pairings WHERE expires_at > ?`, nowUnix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var h string
		var exp float64
		if err := rows.Scan(&h, &exp); err != nil {
			return nil, err
		}
		if left := int(exp - nowUnix()); left > 0 {
			out[h] = left
		}
	}
	return out, rows.Err()
}

// Pair redeems a pairing code for a device key. The code row is deleted
// whether or not it turns out to be expired: it is single use either way. If
// the key was already paired, its old entry (and route) is replaced.
func (s *Store) Pair(code string, devicePub []byte, name string) (dev Device, routeToken, replacedRoute string, err error) {
	if len(devicePub) != 32 {
		return Device{}, "", "", ErrNotFound
	}
	h := hashCode(strings.TrimSpace(code))
	s.mu.Lock()
	defer s.mu.Unlock()
	var owner auth.Principal
	var human int
	var expires float64
	err = s.db.QueryRow(`SELECT owner_kind, owner_login, owner_node, owner_human, expires_at FROM relay_pairings WHERE code_hash=?`, h).
		Scan(&owner.Kind, &owner.Login, &owner.Node, &human, &expires)
	if err != nil {
		return Device{}, "", "", ErrNotFound
	}
	if _, err := s.db.Exec(`DELETE FROM relay_pairings WHERE code_hash=?`, h); err != nil {
		return Device{}, "", "", err
	}
	if nowUnix() > expires {
		return Device{}, "", "", ErrNotFound
	}
	owner.Human = human == 1
	routeToken, err = relay.RandomToken()
	if err != nil {
		return Device{}, "", "", err
	}
	pub := relay.B64(devicePub)
	_ = s.db.QueryRow(`SELECT route_hash FROM relay_devices WHERE public_key=?`, pub).Scan(&replacedRoute)
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Relay device"
	}
	if len(name) > 120 {
		name = name[:120]
	}
	now := nowUnix()
	_, err = s.db.Exec(`INSERT INTO relay_devices(public_key, route_hash, name, owner_kind, owner_login, owner_node, owner_human, paired_at, last_seen_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(public_key) DO UPDATE SET route_hash=excluded.route_hash, name=excluded.name, owner_kind=excluded.owner_kind,
		  owner_login=excluded.owner_login, owner_node=excluded.owner_node, owner_human=excluded.owner_human,
		  paired_at=excluded.paired_at, last_seen_at=excluded.last_seen_at`,
		pub, relay.TokenHash(routeToken), name, owner.Kind, owner.Login, owner.Node, human, now, now)
	if err != nil {
		return Device{}, "", "", err
	}
	dev, err = s.deviceWhere(`public_key=?`, pub)
	if err != nil {
		return Device{}, "", "", err
	}
	return dev, routeToken, replacedRoute, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

const deviceCols = `id, public_key, route_hash, name, owner_kind, owner_login, owner_node, owner_human, paired_at, last_seen_at`

func scanDevice(sc interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var human int
	if err := sc.Scan(&d.ID, &d.publicKey, &d.routeHash, &d.Name, &d.owner.Kind, &d.owner.Login, &d.owner.Node, &human,
		&d.PairedAt, &d.LastSeenAt); err != nil {
		return Device{}, err
	}
	d.owner.Human = human == 1
	d.OwnerLogin = d.owner.Login
	if pub, err := relay.UnB64(d.publicKey); err == nil {
		d.Fingerprint = relay.Fingerprint(pub)
	}
	return d, nil
}

func (s *Store) deviceWhere(where string, args ...any) (Device, error) {
	return scanDevice(s.db.QueryRow(`SELECT `+deviceCols+` FROM relay_devices WHERE `+where, args...))
}

// idleLimit reuses ordinary pairing's idle setting (Settings → Devices), so
// one knob governs how long any paired phone may sit unused.
func (s *Store) idleLimit() float64 { return pairing.IdleDays(s.db) * 24 * 3600 }

// Active returns the paired device with this key, or ErrNotFound if it is
// unknown, revoked, or idle past the limit (in which case it is removed).
func (s *Store) Active(devicePub []byte) (Device, error) {
	d, err := s.deviceWhere(`public_key=?`, relay.B64(devicePub))
	if err != nil {
		return Device{}, ErrNotFound
	}
	if nowUnix()-d.LastSeenAt > s.idleLimit() {
		_, _ = s.db.Exec(`DELETE FROM relay_devices WHERE id=?`, d.ID)
		return Device{}, ErrNotFound
	}
	return d, nil
}

// ActiveByID is Active for a device already identified by its row id, used
// on every tunnelled request so a revocation applies immediately.
func (s *Store) ActiveByID(id int64) (Device, error) {
	d, err := s.deviceWhere(`id=?`, id)
	if err != nil {
		return Device{}, ErrNotFound
	}
	if nowUnix()-d.LastSeenAt > s.idleLimit() {
		_, _ = s.db.Exec(`DELETE FROM relay_devices WHERE id=?`, d.ID)
		return Device{}, ErrNotFound
	}
	return d, nil
}

// Touch records use, which is what keeps a device from idling out.
func (s *Store) Touch(id int64) {
	_, _ = s.db.Exec(`UPDATE relay_devices SET last_seen_at=? WHERE id=?`, nowUnix(), id)
}

// Devices lists every relay device, most recently paired first.
func (s *Store) Devices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT ` + deviceCols + ` FROM relay_devices ORDER BY paired_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RouteHashes are the relay routes of every paired device.
func (s *Store) RouteHashes() ([]string, error) {
	rows, err := s.db.Query(`SELECT route_hash FROM relay_devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// Revoke deletes a device and returns what was deleted, so the caller can
// drop its route and close its connections.
func (s *Store) Revoke(id int64) (Device, error) {
	d, err := s.deviceWhere(`id=?`, id)
	if err != nil {
		return Device{}, ErrNotFound
	}
	if _, err := s.db.Exec(`DELETE FROM relay_devices WHERE id=?`, id); err != nil {
		return Device{}, err
	}
	return d, nil
}
