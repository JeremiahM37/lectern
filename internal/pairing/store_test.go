package pairing

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "lectern.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db)
}

func owner() auth.Principal {
	return auth.Principal{Kind: auth.KindTailscale, Login: "owner@example.com", Node: "phone", Human: true}
}

func TestMintAndExchangeHappyPath(t *testing.T) {
	s := openTestStore(t)
	code, expires, err := s.MintCode(owner())
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	if len(code) != 32 {
		t.Fatalf("expected a 32-char code, got %q", code)
	}
	if !expires.After(Now()) {
		t.Fatal("expiry must be in the future")
	}
	token, dev, err := s.ExchangeCode(code, "My Phone", "TestAgent/1.0")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty device token")
	}
	if dev.Name != "My Phone" || dev.OwnerLogin != "owner@example.com" {
		t.Fatalf("got %+v", dev)
	}
	p, ok := s.LookupDevice(context.Background(), token)
	if !ok || p.Kind != auth.KindDevice || !p.Human || p.Login != "owner@example.com" {
		t.Fatalf("LookupDevice: got %+v ok=%v", p, ok)
	}
}

func TestExchangeAcceptsFormattedOrLowercaseCode(t *testing.T) {
	s := openTestStore(t)
	code, _, err := s.MintCode(owner())
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	formatted := FormatCode(code)
	if formatted == code {
		t.Fatal("test fixture assumption broke: FormatCode should change the string")
	}
	if _, _, err := s.ExchangeCode(formatted, "", ""); err != nil {
		t.Fatalf("a hyphenated, differently-cased code must still exchange: %v", err)
	}
}

func TestExchangeRejectsWrongCode(t *testing.T) {
	s := openTestStore(t)
	if _, _, err := s.MintCode(owner()); err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	if _, _, err := s.ExchangeCode("0000000000000000000000000000FF", "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for a wrong code, got %v", err)
	}
}

func TestExchangeRejectsReusedCode(t *testing.T) {
	s := openTestStore(t)
	code, _, err := s.MintCode(owner())
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	if _, _, err := s.ExchangeCode(code, "First", ""); err != nil {
		t.Fatalf("first exchange: %v", err)
	}
	if _, _, err := s.ExchangeCode(code, "Second", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a reused code must be rejected with ErrNotFound, got %v", err)
	}
}

func TestExchangeRejectsExpiredCode(t *testing.T) {
	s := openTestStore(t)
	real := Now
	defer func() { Now = real }()
	Now = func() time.Time { return real() }
	code, _, err := s.MintCode(owner())
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	Now = func() time.Time { return real().Add(CodeTTL + time.Minute) }
	if _, _, err := s.ExchangeCode(code, "", ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
	// And it is gone, not just expired-but-present: a second attempt is
	// ErrNotFound, matching the single-use contract.
	if _, _, err := s.ExchangeCode(code, "", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired code must also be consumed, got %v", err)
	}
}

func TestLookupDeviceRejectsUnknownToken(t *testing.T) {
	s := openTestStore(t)
	if _, ok := s.LookupDevice(context.Background(), "not-a-real-token"); ok {
		t.Fatal("an unknown token must not authenticate")
	}
}

func TestRevokeDeviceInvalidatesToken(t *testing.T) {
	s := openTestStore(t)
	code, _, _ := s.MintCode(owner())
	token, dev, err := s.ExchangeCode(code, "Phone", "")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if _, ok := s.LookupDevice(context.Background(), token); !ok {
		t.Fatal("expected the fresh token to authenticate")
	}
	if err := s.RevokeDevice(dev.ID); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}
	if _, ok := s.LookupDevice(context.Background(), token); ok {
		t.Fatal("a revoked device's token must stop authenticating immediately")
	}
	if err := s.RevokeDevice(dev.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking an already-revoked device should report ErrNotFound, got %v", err)
	}
}

func TestListDevicesReturnsPairedDevices(t *testing.T) {
	s := openTestStore(t)
	code1, _, _ := s.MintCode(owner())
	code2, _, _ := s.MintCode(owner())
	if _, _, err := s.ExchangeCode(code1, "Phone A", ""); err != nil {
		t.Fatalf("exchange 1: %v", err)
	}
	if _, _, err := s.ExchangeCode(code2, "Phone B", ""); err != nil {
		t.Fatalf("exchange 2: %v", err)
	}
	rows, err := s.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 devices, got %d: %+v", len(rows), rows)
	}
}

func TestLookupDeviceExpiresIdleTokens(t *testing.T) {
	s := openTestStore(t)
	if err := SetIdleDays(s.db, 1); err != nil {
		t.Fatalf("SetIdleDays: %v", err)
	}
	code, _, _ := s.MintCode(owner())
	token, _, err := s.ExchangeCode(code, "Phone", "")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	real := Now
	defer func() { Now = real }()
	if _, ok := s.LookupDevice(context.Background(), token); !ok {
		t.Fatal("expected the fresh token to authenticate before any idle time passes")
	}
	Now = func() time.Time { return real().Add(2 * 24 * time.Hour) }
	if _, ok := s.LookupDevice(context.Background(), token); ok {
		t.Fatal("a token idle past the configured limit must stop authenticating")
	}
	Now = real
	rows, err := s.ListDevices()
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("an idle-expired device should be removed, got %+v", rows)
	}
}

func TestEnabledSettingAndEnvOverride(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "lectern.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	if Enabled(false, db) {
		t.Fatal("expected pairing off by default")
	}
	if !Enabled(true, db) {
		t.Fatal("LECTERN_DEVICE_PAIRING=1 must force it on regardless of the setting")
	}
	if err := SetEnabled(db, true); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !Enabled(false, db) {
		t.Fatal("the persisted setting must turn it on with no env var set")
	}
	if err := SetEnabled(db, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if Enabled(false, db) {
		t.Fatal("expected pairing off again after SetEnabled(false)")
	}
}

func TestIdleDaysDefaultAndSetting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "lectern.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer db.Close()
	if got := IdleDays(db); got != DefaultIdleDays {
		t.Fatalf("expected default %v, got %v", DefaultIdleDays, got)
	}
	if err := SetIdleDays(db, 7); err != nil {
		t.Fatalf("SetIdleDays: %v", err)
	}
	if got := IdleDays(db); got != 7 {
		t.Fatalf("expected 7, got %v", got)
	}
	// A non-positive value resets to the default rather than disabling
	// expiry outright.
	if err := SetIdleDays(db, 0); err != nil {
		t.Fatalf("SetIdleDays(0): %v", err)
	}
	if got := IdleDays(db); got != DefaultIdleDays {
		t.Fatalf("expected reset to default, got %v", got)
	}
}

// TestSecretsAreNeverStoredRaw is the hardening requirement made concrete:
// neither a pairing code nor a device token appears anywhere in the
// database, in any column, of any table this package writes to — only their
// SHA-256 hashes do (internal/oauth's exact convention).
func TestSecretsAreNeverStoredRaw(t *testing.T) {
	s := openTestStore(t)
	code, _, err := s.MintCode(owner())
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	token, _, err := s.ExchangeCode(code, "Phone", "UA/1.0")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}

	assertNotFoundAnywhere(t, s, code, "pairing code")
	assertNotFoundAnywhere(t, s, token, "device token")

	// And the hashes that ARE stored are exactly SHA-256 hex of the secret —
	// not, say, the secret itself re-encoded, or a truncated/reversible form.
	var codeHashCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pairing_codes WHERE code_hash=?", hashOf(NormalizeCode(code))).Scan(&codeHashCount); err != nil {
		t.Fatalf("query: %v", err)
	}
	// The code was already consumed by ExchangeCode, so its row is gone —
	// mint a second one and check its hash lands exactly where expected.
	code2, _, err := s.MintCode(owner())
	if err != nil {
		t.Fatalf("MintCode: %v", err)
	}
	var found string
	if err := s.db.QueryRow("SELECT code_hash FROM pairing_codes WHERE code_hash=?", hashOf(NormalizeCode(code2))).Scan(&found); err != nil {
		t.Fatalf("expected to find the code's hash under hashOf(code): %v", err)
	}
	var deviceHashCount int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pairing_devices WHERE token_hash=?", hashOf(token)).Scan(&deviceHashCount); err != nil {
		t.Fatalf("query: %v", err)
	}
	if deviceHashCount != 1 {
		t.Fatalf("expected the device row to be found by hashOf(token), got count=%d", deviceHashCount)
	}
}

// assertNotFoundAnywhere dumps every text-shaped column pairing writes to and
// checks none of them contain secret verbatim.
func assertNotFoundAnywhere(t *testing.T, s *Store, secret, label string) {
	t.Helper()
	tables := []string{"pairing_codes", "pairing_devices"}
	for _, table := range tables {
		rows, err := s.db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatalf("query %s: %v", table, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatalf("columns %s: %v", table, err)
		}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				t.Fatalf("scan %s: %v", table, err)
			}
			for i, v := range vals {
				s := valueToString(v)
				if s == "" || secret == "" {
					continue
				}
				if strings.Contains(s, secret) {
					t.Fatalf("%s: raw %s found in %s.%s: %q", table, label, table, cols[i], s)
				}
			}
		}
		rows.Close()
	}
}

func valueToString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return ""
	}
}
