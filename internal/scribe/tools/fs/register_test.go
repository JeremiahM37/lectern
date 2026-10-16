package fs

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestRegisterAllTools(t *testing.T) {
	r := tools.NewRegistry()
	Register(r)
	want := []string{"read", "write_file", "edit_file", "multi_edit", "undo_edit", "list_dir", "glob", "grep"}
	got := r.Names()
	if len(got) != len(want) {
		t.Fatalf("registered %v", got)
	}
	for i, n := range want {
		if got[i] != n {
			t.Fatalf("order %v", got)
		}
		tl, ok := r.Get(n)
		if !ok {
			t.Fatalf("missing %s", n)
		}
		if !json.Valid(tl.Spec().Schema) {
			t.Fatalf("%s schema invalid", n)
		}
		ro := n == "read" || n == "list_dir" || n == "glob" || n == "grep"
		if tl.ReadOnly() != ro {
			t.Fatalf("%s ReadOnly = %v", n, tl.ReadOnly())
		}
	}
}

func TestToolRecoversFromPanic(t *testing.T) {
	env, _, _, _ := newEnv(t)
	bad := &tool{spec: newSpec("boom", "", nil), run: func(_ context.Context, _ *tools.Env, _ json.RawMessage) tools.Result { panic("x") }}
	res := bad.Run(context.Background(), env, nil)
	if !res.IsError {
		t.Fatal("panic should become an error result")
	}
}
