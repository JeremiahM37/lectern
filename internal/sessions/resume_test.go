package sessions

import (
	"context"
	"github.com/JeremiahM37/lectern/internal/store"
	"os"
	"path/filepath"
	"testing"
)

func TestResumeRequiresAuthoritativeStoppedTerminal(t *testing.T) {
	for _, script := range []string{
		"#!/bin/sh\nexit 124\n",
		"#!/bin/sh\nprintf partial\n",
		"#!/bin/sh\nprintf 'cG9sbC10ZXN0\\terror\\tcGVybWlzc2lvbiBkZW5pZWQ=\\nADK-POLL-END-v2\\n'\n",
		"#!/bin/sh\nprintf 'cG9sbC10ZXN0\\tok\\t\\nADK-POLL-END-v2\\n'\n",
	} {
		t.Run(script, func(t *testing.T) {
			m, s := pollRig(t)
			if err := m.DB.Update("sessions", s.ID, map[string]any{"ended_at": store.Now(), "status": StatusDead}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "bash"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			_, err := m.ResumeConversation(context.Background(), s.ID, "11111111-1111-4111-8111-111111111111", "")
			if err == nil {
				t.Fatal("unverified stopped terminal accepted")
			}
			rows, e := m.DB.Sessions(true)
			if e != nil || len(rows) != 1 {
				t.Fatalf("failed check created a session: %v %v", rows, e)
			}
		})
	}
}
