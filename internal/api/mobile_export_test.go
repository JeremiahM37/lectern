package api

import "time"

// ResetVoiceCache makes the next request detect whisper.cpp again, so a test
// can install or remove its stand-in.
func ResetVoiceCache() {
	voiceCache.Lock()
	voiceCache.at = time.Time{}
	voiceCache.Unlock()
}
