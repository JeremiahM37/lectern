package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func TestChosenModelTellsAChoiceFromTheFallback(t *testing.T) {
	cases := []struct {
		name string
		a    *App
		want bool
	}{
		{"fallback", &App{}, false},
		{"flag", &App{opts: Options{Model: "ollama/qwen"}}, true},
		{"config", &App{Cfg: config.Config{Model: "ollama/qwen"}}, true},
		{"resume", &App{opts: Options{Resume: "abc"}}, true},
	}
	for _, c := range cases {
		if got := c.a.chosenModel(); got != c.want {
			t.Errorf("%s: chosenModel = %v, want %v", c.name, got, c.want)
		}
	}
}
