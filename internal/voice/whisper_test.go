package voice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func wav(n int) []byte {
	b := make([]byte, 44+n)
	copy(b, "RIFF")
	copy(b[8:], "WAVE")
	return b
}

func TestCleanDropsWhisperNoise(t *testing.T) {
	got := Clean("\n [BLANK_AUDIO]\n  Run the tests\n and commit. (silence)\n")
	if got != "Run the tests and commit." {
		t.Fatalf("%q", got)
	}
}

func TestOnlyWAVReachesWhisper(t *testing.T) {
	if CheckWAV([]byte("OggS....")) == nil || CheckWAV(wav(0)) != nil {
		t.Fatal("WAV check")
	}
	for lang, ok := range map[string]bool{"en": true, "auto": true, "deu": true, "-m": false, "en;rm": false, "": false} {
		if ValidLanguage(lang) != ok {
			t.Errorf("language %q", lang)
		}
	}
}

// A stand-in whisper-cli proves the arguments and the cleanup; the real
// binary is exercised by the e2e suite when one is installed.
func TestDetectAndTranscribeWithAStandIn(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "whisper-cli")
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ]; do case $1 in -f) f=$2; shift;; -l) l=$2; shift;; esac; shift; done\n" +
		"[ -s \"$f\" ] || exit 3\n" +
		"echo \" [BLANK_AUDIO]\"; echo \" heard in $l\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	models := filepath.Join(dir, "models")
	os.MkdirAll(models, 0o755)
	for _, m := range []string{"ggml-large-v3.bin", "ggml-base.en.bin", "ggml-tiny.bin"} {
		os.WriteFile(filepath.Join(models, m), []byte("x"), 0o644)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("LECTERN_WHISPER_BIN", "")
	t.Setenv("LECTERN_WHISPER_MODEL", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
	os.MkdirAll(filepath.Join(dir, "xdg", "whisper.cpp"), 0o755)
	os.Symlink(models, filepath.Join(dir, "xdg", "whisper.cpp", "models"))
	e := Detect()
	if e == nil || e.Bin != bin || filepath.Base(e.Model) != "ggml-base.en.bin" {
		t.Fatalf("detected %+v", e)
	}
	got, err := e.Transcribe(context.Background(), wav(64000), "de")
	if err != nil || got != "heard in de" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := e.Transcribe(context.Background(), []byte("not audio"), "en"); err == nil {
		t.Fatal("accepted non-WAV audio")
	}
	// An injected language falls back to the configured one.
	got, _ = e.Transcribe(context.Background(), wav(10), "en -x")
	if !strings.HasSuffix(got, "auto") {
		t.Fatalf("%q", got)
	}
	t.Setenv("LECTERN_WHISPER_MODEL", filepath.Join(dir, "missing.bin"))
	if Detect() != nil {
		t.Fatal("a missing model must hide the option")
	}
}
