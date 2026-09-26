package pairing

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// ErrNotFound covers an unknown or already-consumed code, or a device token
// nothing recognizes — deliberately one error, matching internal/oauth's
// ErrNotFound, so neither endpoint becomes an enumeration oracle. ErrExpired
// is kept distinct only for callers (tests, mainly) that care which one
// happened; the HTTP layer collapses both to the same response.
var (
	ErrNotFound = errors.New("pairing: not found")
	ErrExpired  = errors.New("pairing: expired")
)

const (
	settingEnabled  = "pairing_enabled"
	settingIdleDays = "pairing_idle_days"
)

// Enabled reports whether device pairing is turned on. envSet is
// LECTERN_DEVICE_PAIRING=1; the persisted "pairing_enabled" setting is the
// Settings → Devices toggle. Either one turns it on; there is no way to force
// it off from the database once the env var is set, matching how every other
// env-vs-setting knob in this codebase treats an explicit env value as an
// operator override.
func Enabled(envSet bool, db *store.DB) bool {
	if envSet {
		return true
	}
	if db == nil {
		return false
	}
	return db.Setting(settingEnabled) == "1"
}

// SetEnabled persists the live toggle.
func SetEnabled(db *store.DB, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return db.SetSetting(settingEnabled, v)
}

// IdleDays is how long a paired device may go unused before its token stops
// authenticating — DefaultIdleDays unless overridden by the
// "pairing_idle_days" setting.
func IdleDays(db *store.DB) float64 {
	if db == nil {
		return DefaultIdleDays
	}
	if raw := strings.TrimSpace(db.Setting(settingIdleDays)); raw != "" {
		if n, err := strconv.ParseFloat(raw, 64); err == nil && n > 0 {
			return n
		}
	}
	return DefaultIdleDays
}

// SetIdleDays persists the idle-expiry setting. A non-positive value resets
// to the default rather than disabling idle expiry — this is a phone that
// can decide approvals unattended; there is no legitimate reason to make its
// credential immortal.
func SetIdleDays(db *store.DB, days float64) error {
	if days <= 0 {
		days = DefaultIdleDays
	}
	return db.SetSetting(settingIdleDays, strconv.FormatFloat(days, 'f', -1, 64))
}

// Device is one paired device, as shown on Settings → Devices.
type Device struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	OwnerLogin string  `json:"owner_login,omitempty"`
	OwnerKind  string  `json:"owner_kind,omitempty"`
	UserAgent  string  `json:"user_agent,omitempty"`
	PairedAt   float64 `json:"paired_at"`
	LastSeenAt float64 `json:"last_seen_at"`
}

// Store is device pairing's persistence — pending codes and paired devices —
// kept in the shared lectern.db (internal/store) rather than a database of
// its own, unlike internal/oauth (which genuinely can run in a separate
// process against a remote control plane). Pairing only ever runs inside the
// one process that already owns this database.
type Store struct {
	db      *store.DB
	limiter *RateLimiter
	// mu serializes a code's read-then-delete: store.DB pins the connection
	// pool to one connection (see store.Open), which makes each individual
	// statement atomic, but two goroutines could otherwise both read the
	// same still-present code before either deletes it.
	mu sync.Mutex
}

// New builds a Store bound to db.
func New(db *store.DB) *Store {
	return &Store{db: db, limiter: NewRateLimiter()}
}

// Limiter is the exchange endpoint's rate limiter — one per Store, so tests
// and the production server share the same instance that guards the DB.
func (s *Store) Limiter() *RateLimiter { return s.limiter }

// MintCode issues a fresh single-use code bound to owner, valid for CodeTTL.
// owner is whatever internal/auth resolved for the minting request — its
// Kind/Login/Node travel with the code and become the paired device's own
// identity once exchanged.
func (s *Store) MintCode(owner auth.Principal) (code string, expiresAt time.Time, err error) {
	raw, err := newCode()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = Now().Add(CodeTTL)
	_, err = s.db.Exec(
		`INSERT INTO pairing_codes(code_hash, owner_kind, owner_login, owner_node, created_at, expires_at)
		 VALUES(?,?,?,?,?,?)`,
		hashOf(raw), owner.Kind, owner.Login, owner.Node, nowUnix(), float64(expiresAt.Unix()))
	if err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

// PruneExpiredCodes drops codes nobody redeemed. Best-effort housekeeping,
// mirroring internal/oauth's pruneExpiredCodes; nothing here depends on it
// running promptly since ExchangeCode itself checks expiry.
func (s *Store) PruneExpiredCodes() {
	_, _ = s.db.Exec("DELETE FROM pairing_codes WHERE expires_at < ?", float64(Now().Unix()))
}

// ExchangeCode consumes a single-use code and mints a device token for it.
// The code row is deleted whether or not it turns out to be expired — it is
// single-use either way, and leaving an expired one behind would only let it
// be looked up again for no benefit (see internal/oauth.ConsumeAuthCode,
// the same shape of fix for the same replay concern).
func (s *Store) ExchangeCode(rawCode, name, userAgent string) (token string, dev Device, err error) {
	h := hashOf(NormalizeCode(rawCode))
	s.mu.Lock()
	defer s.mu.Unlock()
	row := s.db.QueryRow(
		`SELECT owner_kind, owner_login, owner_node, expires_at FROM pairing_codes WHERE code_hash=?`, h)
	var ownerKind, ownerLogin, ownerNode string
	var expires float64
	if scanErr := row.Scan(&ownerKind, &ownerLogin, &ownerNode, &expires); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return "", Device{}, ErrNotFound
		}
		return "", Device{}, scanErr
	}
	if _, delErr := s.db.Exec("DELETE FROM pairing_codes WHERE code_hash=?", h); delErr != nil {
		return "", Device{}, delErr
	}
	if float64(Now().Unix()) > expires {
		return "", Device{}, ErrExpired
	}
	raw, err := newDeviceToken()
	if err != nil {
		return "", Device{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Paired device"
	}
	name = truncate(name, 120)
	userAgent = truncate(strings.TrimSpace(userAgent), 300)
	now := nowUnix()
	result, err := s.db.Exec(
		`INSERT INTO pairing_devices(token_hash, name, owner_kind, owner_login, owner_node, user_agent, paired_at, last_seen_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		hashOf(raw), name, ownerKind, ownerLogin, ownerNode, userAgent, now, now)
	if err != nil {
		return "", Device{}, err
	}
	id, _ := result.LastInsertId()
	dev = Device{
		ID: id, Name: name, OwnerKind: ownerKind, OwnerLogin: ownerLogin,
		UserAgent: userAgent, PairedAt: now, LastSeenAt: now,
	}
	return raw, dev, nil
}

// LookupDevice implements auth.DeviceLookup: it resolves a raw device token
// to the principal it authenticates as, touching last_seen_at on success. An
// idle-expired token is treated exactly like an unknown one and its row is
// removed — there is no reason to keep proving the same idle check on every
// later request once a token has already failed it once.
func (s *Store) LookupDevice(ctx context.Context, rawToken string) (auth.Principal, bool) {
	rawToken = strings.TrimSpace(rawToken)
	if rawToken == "" {
		return auth.Principal{}, false
	}
	h := hashOf(rawToken)
	row := s.db.QueryRowContext(ctx,
		`SELECT id, owner_kind, owner_login, owner_node, last_seen_at FROM pairing_devices WHERE token_hash=?`, h)
	var id int64
	var ownerKind, ownerLogin, ownerNode string
	var lastSeen float64
	if err := row.Scan(&id, &ownerKind, &ownerLogin, &ownerNode, &lastSeen); err != nil {
		return auth.Principal{}, false
	}
	now := nowUnix()
	idleLimit := IdleDays(s.db) * 24 * 3600
	if now-lastSeen > idleLimit {
		_, _ = s.db.ExecContext(ctx, "DELETE FROM pairing_devices WHERE id=?", id)
		return auth.Principal{}, false
	}
	// Best-effort: a lost "last seen" update costs nothing but a slightly
	// stale admin-console column, never worth failing the request over.
	_, _ = s.db.ExecContext(ctx, "UPDATE pairing_devices SET last_seen_at=? WHERE id=?", now, id)
	return auth.Principal{Kind: auth.KindDevice, Login: ownerLogin, Node: ownerNode, Human: true}, true
}

// ListDevices returns every paired device, most recently paired first —
// Settings → Devices' whole data source.
func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(
		`SELECT id, name, owner_kind, owner_login, owner_node, user_agent, paired_at, last_seen_at
		 FROM pairing_devices ORDER BY paired_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		var ownerNode string
		if err := rows.Scan(&d.ID, &d.Name, &d.OwnerKind, &d.OwnerLogin, &ownerNode, &d.UserAgent, &d.PairedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RevokeDevice deletes a device's token immediately — its next request gets
// a plain 401, same as any other unrecognized credential.
func (s *Store) RevokeDevice(id int64) error {
	res, err := s.db.Exec("DELETE FROM pairing_devices WHERE id=?", id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
