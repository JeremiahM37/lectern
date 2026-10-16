package telegram

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
)

const fakeToken = "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"

func useHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	t.Setenv(TokenEnv, "")
	return home
}

func TestValidTokenShape(t *testing.T) {
	for _, s := range []string{fakeToken, "  " + fakeToken + "\n"} {
		if !ValidToken(s) {
			t.Errorf("ValidToken(%q) = false", s)
		}
	}
	for _, s := range []string{"", "123", "abc:def", "123456789-AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", "123456789:short"} {
		if ValidToken(s) {
			t.Errorf("ValidToken(%q) = true", s)
		}
	}
}

func TestMaskTokenNeverShowsTheWholeSecret(t *testing.T) {
	got := MaskToken(fakeToken)
	if got != "123456789:…Dsaw" {
		t.Fatalf("MaskToken = %q", got)
	}
	if strings.Contains(got, "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDs") {
		t.Fatal("mask leaks the secret")
	}
	if got := MaskToken("not a token at all"); strings.Contains(got, "not a token") {
		t.Fatalf("mask echoes bad input: %q", got)
	}
}

func TestSaveTokenWritesKeyStoreMode0600AndKeepsOtherKeys(t *testing.T) {
	home := useHome(t)
	path := filepath.Join(home, "keys.json")
	if err := os.WriteFile(path, []byte(`{"openai":"sk-keep"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveToken(fakeToken); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	var keys map[string]string
	if err := json.Unmarshal(b, &keys); err != nil {
		t.Fatal(err)
	}
	if keys[TokenKey] != fakeToken || keys["openai"] != "sk-keep" {
		t.Fatalf("keys = %v", keys)
	}
	if got := CurrentToken(); got != fakeToken {
		t.Fatalf("CurrentToken = %q", got)
	}
	if TokenSource() != "key store" {
		t.Fatalf("TokenSource = %q", TokenSource())
	}
}

func TestSaveTokenRejectsBadInputWithoutEchoingIt(t *testing.T) {
	useHome(t)
	bad := "oops-not-a-token-XYZ"
	err := SaveToken(bad)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), bad) {
		t.Fatalf("error echoes the input: %v", err)
	}
}

func TestEnvironmentTokenWinsAndClearOnlyTouchesTheStore(t *testing.T) {
	useHome(t)
	if err := SaveToken(fakeToken); err != nil {
		t.Fatal(err)
	}
	envTok := "987654321:ZZHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	t.Setenv(TokenEnv, envTok)
	if TokenSource() != "environment" || CurrentToken() != envTok {
		t.Fatalf("env should win: source=%q", TokenSource())
	}
	had, err := ClearToken()
	if err != nil || !had {
		t.Fatalf("ClearToken = %v, %v", had, err)
	}
	if CurrentToken() != envTok {
		t.Fatal("clearing the store must not change the environment token")
	}
	if had, _ := ClearToken(); had {
		t.Fatal("second clear should report nothing stored")
	}
}

func TestAllowForgetAndPairingsWhileOff(t *testing.T) {
	useHome(t)
	s := &Surface{}
	if _, err := s.Allow("12 3"); err == nil {
		t.Fatal("a non-numeric chat id must be refused")
	}
	id, err := s.Allow("-1001234")
	if err != nil || id != "-1001234" {
		t.Fatalf("Allow = %q, %v", id, err)
	}
	chats, err := s.Pairings()
	if err != nil || len(chats) != 1 || chats[0].ID != "-1001234" || chats[0].Config {
		t.Fatalf("Pairings = %+v, %v", chats, err)
	}
	ok, err := s.Forget("-1001234")
	if err != nil || !ok {
		t.Fatalf("Forget = %v, %v", ok, err)
	}
	if ok, _ := s.Forget("-1001234"); ok {
		t.Fatal("forgetting twice should report not paired")
	}
}

func TestConfigChatCannotBeForgotten(t *testing.T) {
	home := useHome(t)
	cfg := `{"remote":{"telegram":{"enabled":true,"chat_ids":[555]}}}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Surface{}
	chats, err := s.Pairings()
	if err != nil || len(chats) != 1 || !chats[0].Config {
		t.Fatalf("Pairings = %+v, %v", chats, err)
	}
	if _, err := s.Forget("555"); err != remote.ErrStaticChat {
		t.Fatalf("Forget config chat = %v, want ErrStaticChat", err)
	}
}

func TestParseChatID(t *testing.T) {
	if id, err := ParseChatID(" -100123 "); err != nil || id != "-100123" {
		t.Fatalf("ParseChatID = %q, %v", id, err)
	}
	for _, bad := range []string{"", "0", "abc", "1.5"} {
		if _, err := ParseChatID(bad); err == nil {
			t.Errorf("ParseChatID(%q) accepted", bad)
		}
	}
}

func TestLogRingKeepsTheNewestLines(t *testing.T) {
	var r logRing
	logf := r.wrap(nil)
	for i := 0; i < logKeep+10; i++ {
		logf("line %d", i)
	}
	got := r.snapshot()
	if len(got) != logKeep {
		t.Fatalf("kept %d lines, want %d", len(got), logKeep)
	}
	if got[0] != "line 10" || got[len(got)-1] != fmt.Sprintf("line %d", logKeep+9) {
		t.Fatalf("ring order wrong: first=%q last=%q", got[0], got[len(got)-1])
	}
}

func TestLogRingCallsTheNextLoggerToo(t *testing.T) {
	var seen []string
	var r logRing
	logf := r.wrap(func(format string, args ...any) { seen = append(seen, fmt.Sprintf(format, args...)) })
	logf("poll failed: %v", "timeout")
	if len(seen) != 1 || seen[0] != "poll failed: timeout" {
		t.Fatalf("next logger got %v", seen)
	}
}
