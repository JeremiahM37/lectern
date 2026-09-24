package push

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// The owner must never have to configure keys by hand: a fresh install has no
// LECTERN_VAPID_PRIVATE/PUBLIC and no settings row, so the very first
// ResolveKeys call has to generate a working pair on its own.
func TestResolveKeysGeneratesOnFirstStart(t *testing.T) {
	db := testDB(t)
	sender, err := ResolveKeys(db, "", "", "", slog.New(slog.NewTextHandler(nilWriter{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if !sender.Enabled() {
		t.Fatal("a generated pair must leave push enabled by default")
	}
	if db.Setting(SettingPrivateKey) == "" || db.Setting(SettingPublicKey) == "" {
		t.Fatal("the generated pair was not persisted")
	}
	if db.Setting(SettingPrivateKey) != sender.PrivateKey || db.Setting(SettingPublicKey) != sender.PublicKey {
		t.Fatal("the persisted pair does not match what the sender is using")
	}
}

// A restart (or a second call against the same database, which is the part
// that is actually testable here) must reuse the same identity rather than
// mint a new one: a browser subscription is bound to the public key it
// subscribed against, so rotating it silently would orphan every existing
// subscription.
func TestResolveKeysReusesThePersistedPairAcrossStarts(t *testing.T) {
	db := testDB(t)
	first, err := ResolveKeys(db, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ResolveKeys(db, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.PrivateKey != second.PrivateKey || first.PublicKey != second.PublicKey {
		t.Fatal("a second start generated a different key pair instead of reusing the persisted one")
	}
}

// LECTERN_VAPID_PRIVATE/PUBLIC are an operator's explicit choice and must win
// over anything in the database, without themselves being written back to it
// — an operator revoking the env vars later should fall through to whatever
// was there before, not accidentally adopt the env pair as permanent.
func TestEnvKeysWinOverPersisted(t *testing.T) {
	db := testDB(t)
	if _, err := ResolveKeys(db, "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	generatedPriv := db.Setting(SettingPrivateKey)

	sender, err := ResolveKeys(db, "env-priv-key", "env-pub-key", "ops@example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sender.PrivateKey != "env-priv-key" || sender.PublicKey != "env-pub-key" {
		t.Fatalf("env keys were not used: %+v", sender)
	}
	if sender.Email != "ops@example.com" {
		t.Errorf("email: %q", sender.Email)
	}
	if db.Setting(SettingPrivateKey) != generatedPriv {
		t.Error("env keys must not overwrite the persisted database pair")
	}
}

// Both halves must be present in the database for it to count as configured
// — a half-written row (crash between the two SetSetting calls, or hand
// editing) must not be trusted as-is.
func TestResolveKeysRegeneratesAHalfWrittenPair(t *testing.T) {
	db := testDB(t)
	if err := db.SetSetting(SettingPublicKey, "only-half-here"); err != nil {
		t.Fatal(err)
	}
	sender, err := ResolveKeys(db, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sender.PrivateKey == "" || sender.PublicKey == "only-half-here" {
		t.Fatalf("a half-written pair should be replaced with a full one: %+v", sender)
	}
}

// Falls back to config's own documented default when no email is given
// anywhere (defensive: config.Load already applies this default itself, but
// a hand-built Config, as every test in this repo uses, may leave it empty).
func TestResolveKeysDefaultsEmail(t *testing.T) {
	db := testDB(t)
	sender, err := ResolveKeys(db, "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if sender.Email != defaultEmail {
		t.Errorf("email: %q", sender.Email)
	}
}

// The private key must never reach the log, generated or not — logging its
// source is fine, logging the key itself is a credential leak.
func TestResolveKeysNeverLogsThePrivateKey(t *testing.T) {
	db := testDB(t)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	sender, err := ResolveKeys(db, "", "", "", log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), sender.PrivateKey) {
		t.Fatalf("private key leaked into the log: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "generated") {
		t.Errorf("expected the source to be logged, got: %s", buf.String())
	}
}

func TestGenerateKeyPairProducesDistinctUsablePairs(t *testing.T) {
	priv1, pub1, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	priv2, pub2, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if priv1 == priv2 || pub1 == pub2 {
		t.Fatal("two generated pairs collided")
	}
	s := &Sender{PrivateKey: priv1, PublicKey: pub1, Email: "a@b"}
	if !s.Enabled() {
		t.Fatal("a generated pair must report the sender enabled")
	}
	if _, err := s.vapidJWT("https://push.example/x"); err != nil {
		t.Fatalf("generated private key does not sign: %v", err)
	}
}

// nilWriter discards everything — used where a test needs a non-nil *slog.Logger
// but does not care about its output.
type nilWriter struct{}

func (nilWriter) Write(p []byte) (int, error) { return len(p), nil }
