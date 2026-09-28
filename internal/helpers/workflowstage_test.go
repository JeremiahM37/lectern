//go:build unix

package helpers

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"
)

func TestWorkflowStageParity(t *testing.T) {
	script := pythonConst(t, "workflows/workflows.go", "stageScript")
	body := base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho hi\n"))
	req := func(root, parts, data, mode string) []string {
		s := `{"data":"` + data + `","parts":` + parts + `,"root":"` + root + `/stage"`
		if mode != "" {
			s += `,"mode":` + mode
		}
		return []string{s + "}"}
	}
	cases := []struct {
		name  string
		setup func(root string)
		steps func(root string) [][]string
	}{
		{name: "create then verify", steps: func(r string) [][]string {
			return [][]string{req(r, `["bin","run.sh"]`, body, "493"), req(r, `["bin","run.sh"]`, body, "493"), req(r, `["top"]`, body, "")}
		}},
		{name: "different content", steps: func(r string) [][]string {
			return [][]string{req(r, `["f"]`, body, "420"), req(r, `["f"]`, base64.StdEncoding.EncodeToString([]byte("other")), "420")}
		}},
		{name: "different mode", steps: func(r string) [][]string {
			return [][]string{req(r, `["f"]`, body, "420"), req(r, `["f"]`, body, "384")}
		}},
		{name: "symlink at name", setup: func(r string) {
			os.MkdirAll(r+"/stage", 0o755)
			os.WriteFile(r+"/target", []byte("x"), 0o644)
			os.Symlink(r+"/target", r+"/stage/f")
		}, steps: func(r string) [][]string { return [][]string{req(r, `["f"]`, body, "420")} }},
		{name: "symlinked directory", setup: func(r string) {
			os.MkdirAll(r+"/stage", 0o755)
			os.MkdirAll(r+"/elsewhere", 0o755)
			os.Symlink(r+"/elsewhere", r+"/stage/d")
		}, steps: func(r string) [][]string { return [][]string{req(r, `["d","f"]`, body, "420")} }},
		{name: "file in the way", setup: func(r string) {
			os.MkdirAll(r+"/stage", 0o755)
			os.WriteFile(r+"/stage/d", []byte("x"), 0o644)
		}, steps: func(r string) [][]string { return [][]string{req(r, `["d","f"]`, body, "420")} }},
		{name: "unsafe parts", steps: func(r string) [][]string {
			return [][]string{req(r, `[]`, body, ""), req(r, `["..","x"]`, body, ""), req(r, `["a/b"]`, body, ""),
				req(r, `["a\\b"]`, body, ""), req(r, `[""]`, body, ""), req(r, `[1]`, body, "")}
		}},
		{name: "bad input", steps: func(r string) [][]string {
			return [][]string{req(r, `["f"]`, "not base64", ""), {`{"parts":["f"]}`}, {`nope`}, req(r, `["f"]`, body, `"493"`)}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			twinSteps(t, script, "workflow-stage", nil, func(t *testing.T, root string) (runSpec, [][]string) {
				if c.setup != nil {
					c.setup(root)
				}
				return runSpec{dir: root}, c.steps(root)
			})
		})
	}
	// The created file carries the pinned mode whatever the umask.
	_, got := twin(t, script, "workflow-stage", func(t *testing.T, root string) (runSpec, []string) {
		return runSpec{dir: root}, req(root, `["x"]`, body, "448")
	})
	if !strings.Contains(got.stdout, `{"created":true}`) {
		t.Fatalf("stage printed %q", got.stdout)
	}
}
