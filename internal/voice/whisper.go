// Package voice transcribes dictation on the Lectern host with whisper.cpp,
// for phones whose browser has no usable speech recognition (the Android
// app's WebView has none) or whose owner would rather the audio stay on
// their own machine than go to a browser vendor's speech service.
//
// Nothing is installed or downloaded here. When a whisper.cpp binary and a
// model are present (found on PATH and in the usual model folders, or named
// by LECTERN_WHISPER_BIN and LECTERN_WHISPER_MODEL) the option appears in the
// app; otherwise it does not. docs/mobile-sessions.md has the setup.
package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MaxAudioBytes bounds one recording: 16 kHz mono 16-bit WAV is 32 KB a
// second, so this is about five minutes.
const MaxAudioBytes = 10 << 20

// Engine is a found whisper.cpp installation.
type Engine struct {
	Bin     string
	Model   string
	Threads int
	// Language is whisper's -l value: "auto", or a code such as "en".
	Language string
}

// binaries are the names whisper.cpp's CLI is installed under: whisper-cli
// by its own build, whisper-cpp by Homebrew and several distributions.
var binaries = []string{"whisper-cli", "whisper-cpp", "whisper.cpp"}

// modelDirs are where whisper.cpp's download script and packages put models.
func modelDirs(home string) []string {
	dirs := []string{
		filepath.Join(home, ".local", "share", "whisper.cpp", "models"),
		filepath.Join(home, ".cache", "whisper.cpp"),
		filepath.Join(home, ".cache", "whisper"),
		filepath.Join(home, "whisper.cpp", "models"),
		"/usr/local/share/whisper.cpp/models",
		"/usr/share/whisper.cpp/models",
		"/opt/homebrew/share/whisper-cpp",
		"/usr/local/share/whisper-cpp",
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		dirs = append([]string{filepath.Join(xdg, "whisper.cpp", "models")}, dirs...)
	}
	return dirs
}

// modelRank prefers a model that transcribes a phone's short dictation well
// and quickly on a CPU: base, then small, then tiny, then the large ones.
func modelRank(name string) int {
	for i, want := range []string{"base.en", "base", "small.en", "small", "tiny.en", "tiny", "medium.en", "medium", "large"} {
		if strings.Contains(name, "ggml-"+want) {
			return i
		}
	}
	return 99
}

// Detect finds whisper.cpp from the environment, then PATH and the usual
// model folders. It returns nil when either half is missing.
func Detect() *Engine {
	bin := strings.TrimSpace(os.Getenv("LECTERN_WHISPER_BIN"))
	if bin != "" {
		if found, err := exec.LookPath(bin); err == nil {
			bin = found
		} else {
			return nil
		}
	} else {
		for _, name := range binaries {
			if found, err := exec.LookPath(name); err == nil {
				bin = found
				break
			}
		}
	}
	if bin == "" {
		return nil
	}
	model := strings.TrimSpace(os.Getenv("LECTERN_WHISPER_MODEL"))
	if model == "" {
		home, _ := os.UserHomeDir()
		var found []string
		for _, dir := range modelDirs(home) {
			matches, _ := filepath.Glob(filepath.Join(dir, "ggml-*.bin"))
			found = append(found, matches...)
		}
		sort.SliceStable(found, func(i, j int) bool {
			return modelRank(filepath.Base(found[i])) < modelRank(filepath.Base(found[j]))
		})
		if len(found) > 0 {
			model = found[0]
		}
	}
	if st, err := os.Stat(model); model == "" || err != nil || st.IsDir() {
		return nil
	}
	threads := min(8, runtime.NumCPU())
	if n, err := strconv.Atoi(os.Getenv("LECTERN_WHISPER_THREADS")); err == nil && n > 0 {
		threads = n
	}
	lang := strings.TrimSpace(os.Getenv("LECTERN_WHISPER_LANG"))
	if lang == "" {
		lang = "auto"
	}
	return &Engine{Bin: bin, Model: model, Threads: threads, Language: lang}
}

var languageCode = regexp.MustCompile(`^(auto|[a-z]{2,3})$`)

// ValidLanguage reports whether s may be passed to whisper as -l.
func ValidLanguage(s string) bool { return languageCode.MatchString(s) }

// ErrNotWAV is returned for audio that is not a RIFF/WAVE file.
var ErrNotWAV = errors.New("audio must be a WAV file (16 kHz mono PCM is best)")

// CheckWAV rejects anything but a RIFF WAVE file, before it reaches whisper.
func CheckWAV(audio []byte) error {
	if len(audio) < 44 || string(audio[0:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return ErrNotWAV
	}
	return nil
}

// noise is what whisper writes for silence and non-speech.
var noise = regexp.MustCompile(`\[(BLANK_AUDIO|MUSIC|NOISE|SILENCE|INAUDIBLE)[^\]]*\]|\((?i:silence|music|noise)\)`)

// Clean turns whisper's -nt output into the words that were said.
func Clean(out string) string {
	var parts []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(noise.ReplaceAllString(line, ""))
		if line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// Transcribe runs whisper.cpp on one WAV recording. lang overrides the
// engine's language when it is a valid code.
func (e *Engine) Transcribe(ctx context.Context, audio []byte, lang string) (string, error) {
	if err := CheckWAV(audio); err != nil {
		return "", err
	}
	if !ValidLanguage(lang) {
		lang = e.Language
	}
	dir, err := os.MkdirTemp("", "lectern-voice-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "audio.wav")
	if err := os.WriteFile(file, audio, 0o600); err != nil {
		return "", err
	}
	// A recording takes a fraction of its own length to transcribe on a CPU;
	// the allowance is generous so a slow host still finishes.
	seconds := len(audio) / 32000
	ctx, cancel := context.WithTimeout(ctx, time.Duration(60+2*seconds)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Bin, "-m", e.Model, "-f", file, "-l", lang,
		"-t", strconv.Itoa(e.Threads), "-nt", "-np")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("transcription took too long")
		}
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return "", fmt.Errorf("whisper.cpp failed: %v %s", err, msg)
	}
	return Clean(stdout.String()), nil
}
