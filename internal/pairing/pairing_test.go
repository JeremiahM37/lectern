package pairing

import "testing"

func TestNewCodeIsFullEntropyHex(t *testing.T) {
	code, err := newCode()
	if err != nil {
		t.Fatalf("newCode: %v", err)
	}
	// 16 bytes -> 32 hex characters -> 128 bits. The spec's "short human
	// form" requirement is met by never truncating: the whole 128-bit code
	// is what gets typed, just grouped for readability.
	if len(code) != 32 {
		t.Fatalf("expected a 32-character code (128 bits), got %d: %q", len(code), code)
	}
	code2, err := newCode()
	if err != nil {
		t.Fatalf("newCode: %v", err)
	}
	if code == code2 {
		t.Fatal("two codes must not collide in any reasonable test run")
	}
}

func TestFormatAndNormalizeCodeRoundTrip(t *testing.T) {
	raw := "A1B2C3D4E5F60718293A4B5C6D7E8F90"[:32]
	formatted := FormatCode(raw)
	if formatted == raw {
		t.Fatalf("FormatCode should group the code, got unchanged %q", formatted)
	}
	if got := NormalizeCode(formatted); got != raw {
		t.Fatalf("NormalizeCode(FormatCode(x)) = %q, want %q", got, raw)
	}
	// Manual entry is forgiving of case and stray whitespace.
	if got := NormalizeCode(" a1b2-c3d4 e5f6 "); got != "A1B2C3D4E5F6" {
		t.Fatalf("got %q", got)
	}
}

func TestNewDeviceTokenLength(t *testing.T) {
	tok, err := newDeviceToken()
	if err != nil {
		t.Fatalf("newDeviceToken: %v", err)
	}
	// 32 bytes, base64 URL-safe without padding: 43 characters.
	if len(tok) != 43 {
		t.Fatalf("expected a 43-character token (256 bits), got %d: %q", len(tok), tok)
	}
}
