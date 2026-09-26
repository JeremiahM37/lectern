package autonomy

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func completionFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	base := filepath.Join(root, "baseline")
	candidate := filepath.Join(root, "candidate")
	out := filepath.Join(root, "output")
	for _, dir := range []string{base, candidate} {
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0755); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{"WORKSHOP.md": "old historical claims\n", "src/main.go": "package main\n", "src/main_test.go": "package main\n", "go.mod": "module example\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Mkdir(filepath.Join(candidate, CompletionSupplementDir), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, CompletionSupplementDir, "provenance.md"), []byte("Historical assertions corrected here.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return base, candidate, out
}
func TestCompletionOverlayReconstructsBaseline(t *testing.T) {
	base, candidate, out := completionFixture(t)
	// A builder's temporary production edit is not a source for independent review.
	if err := os.WriteFile(filepath.Join(candidate, "src/main.go"), []byte("evil transient implementation"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "src/main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "WORKSHOP.md"), []byte("corrected claims\n"), 0644); err != nil {
		t.Fatal(err)
	}
	receipt, err := ReconstructCompletionOverlay(base, candidate, out)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"WORKSHOP.md": "corrected claims\n", CompletionOriginalWorkshop: "old historical claims\n", "src/main.go": "package main\n"} {
		got, err := os.ReadFile(filepath.Join(out, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
	old, err := os.ReadFile(filepath.Join(base, "WORKSHOP.md"))
	if err != nil || string(old) != "old historical claims\n" {
		t.Fatal("baseline modified")
	}
	before, _ := os.Stat(filepath.Join(base, "src/main.go"))
	after, _ := os.Stat(filepath.Join(out, "src/main.go"))
	if os.SameFile(before, after) {
		t.Fatal("output aliases baseline")
	}
	info, _ := os.Stat(filepath.Join(out, CompletionOriginalWorkshop))
	if info.Mode().Perm() != 0444 {
		t.Fatal("original evidence is writable")
	}
	if receipt.Policy != CompletionOverlayPolicy || len(receipt.Documents) != 2 || len(receipt.BaselineSHA256) != 64 || receipt.DerivedSHA256 == receipt.BaselineSHA256 {
		t.Fatalf("invalid receipt: %+v", receipt)
	}
	_, _, out2 := completionFixture(t)
	again, err := ReconstructCompletionOverlay(base, candidate, out2)
	if err != nil || again.DerivedSHA256 != receipt.DerivedSHA256 {
		t.Fatalf("non deterministic reconstruction: %v", err)
	}
}
func TestCompletionOverlayRejectsForbiddenChanges(t *testing.T) {
	tests := map[string]func(string, string) error{
		"production": func(b, c string) error { return os.WriteFile(filepath.Join(c, "src/main.go"), []byte("changed"), 0644) },
		"test": func(b, c string) error {
			return os.WriteFile(filepath.Join(c, "src/main_test.go"), []byte("changed"), 0644)
		},
		"dependency":     func(b, c string) error { return os.WriteFile(filepath.Join(c, "go.mod"), []byte("changed"), 0644) },
		"deletion":       func(b, c string) error { return os.Remove(filepath.Join(c, "src/main.go")) },
		"mode":           func(b, c string) error { return os.Chmod(filepath.Join(c, "src/main.go"), 0755) },
		"directory mode": func(b, c string) error { return os.Chmod(filepath.Join(c, "src"), 0700) },
		"workshop mode":  func(b, c string) error { return os.Chmod(filepath.Join(c, "WORKSHOP.md"), 0755) },
		"new outside":    func(b, c string) error { return os.WriteFile(filepath.Join(c, "new.md"), []byte("new"), 0644) },
		"executable doc": func(b, c string) error {
			return os.Chmod(filepath.Join(c, CompletionSupplementDir, "provenance.md"), 0755)
		},
		"script": func(b, c string) error {
			return os.WriteFile(filepath.Join(c, CompletionSupplementDir, "test.py"), []byte("pass"), 0644)
		},
		"binary": func(b, c string) error {
			return os.WriteFile(filepath.Join(c, CompletionSupplementDir, "data.txt"), []byte{0, 1}, 0644)
		},
		"original spoof": func(b, c string) error {
			return os.WriteFile(filepath.Join(c, CompletionOriginalWorkshop), []byte("rewritten history"), 0644)
		},
		"nested": func(b, c string) error { return os.Mkdir(filepath.Join(c, CompletionSupplementDir, "nested"), 0755) },
		"traversal alias": func(b, c string) error {
			return os.WriteFile(filepath.Join(c, CompletionSupplementDir, "..\\outside.txt"), []byte("new"), 0644)
		},
		"symlink escape": func(b, c string) error {
			return os.Symlink(filepath.Join(b, "WORKSHOP.md"), filepath.Join(c, CompletionSupplementDir, "escape.md"))
		},
		"internal symlink": func(b, c string) error {
			return os.Symlink("provenance.md", filepath.Join(c, CompletionSupplementDir, "alias.md"))
		},
		"directory symlink": func(b, c string) error { return os.Symlink(b, filepath.Join(c, CompletionSupplementDir, "link")) },
		"hardlink": func(b, c string) error {
			return os.Link(filepath.Join(b, "WORKSHOP.md"), filepath.Join(c, CompletionSupplementDir, "linked.md"))
		},
		"baseline symlink": func(b, c string) error { return os.Symlink("WORKSHOP.md", filepath.Join(b, "linked.md")) },
		"oversize": func(b, c string) error {
			return os.WriteFile(filepath.Join(c, CompletionSupplementDir, "large.md"), []byte(strings.Repeat("a", completionMaxDocument+1)), 0644)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			b, c, o := completionFixture(t)
			if err := mutate(b, c); err != nil {
				t.Fatal(err)
			}
			if _, err := ReconstructCompletionOverlay(b, c, o); err == nil {
				t.Fatal("accepted forbidden overlay")
			}
			if _, err := os.Lstat(o); !os.IsNotExist(err) {
				t.Fatal("invalid overlay left an output")
			}
		})
	}
}
func TestCompletionOverlayOutputIsolation(t *testing.T) {
	t.Run("existing output", func(t *testing.T) {
		b, c, o := completionFixture(t)
		if err := os.Mkdir(o, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(o, "keep"), []byte("keep"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconstructCompletionOverlay(b, c, o); err == nil {
			t.Fatal("accepted existing output")
		}
		if _, err := os.Stat(filepath.Join(o, "keep")); err != nil {
			t.Fatal("destroyed existing output")
		}
	})
	t.Run("nested output", func(t *testing.T) {
		b, c, _ := completionFixture(t)
		if _, err := ReconstructCompletionOverlay(b, c, filepath.Join(b, "out")); err == nil {
			t.Fatal("accepted nested output")
		}
	})
	t.Run("linked root", func(t *testing.T) {
		b, c, o := completionFixture(t)
		alias := filepath.Join(filepath.Dir(c), "alias")
		if err := os.Symlink(c, alias); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconstructCompletionOverlay(b, alias, o); err == nil {
			t.Fatal("accepted linked root")
		}
	})
	t.Run("reserved baseline", func(t *testing.T) {
		b, c, o := completionFixture(t)
		if err := os.Mkdir(filepath.Join(b, CompletionSupplementDir), 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := ReconstructCompletionOverlay(b, c, o); err == nil {
			t.Fatal("accepted previously overlaid baseline")
		}
	})
}

func TestCompletionOverlayReadOnlyWorkshop(t *testing.T) {
	b, c, o := completionFixture(t)
	if err := os.WriteFile(filepath.Join(c, "WORKSHOP.md"), []byte("new claims"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{b, c} {
		if err := os.Chmod(filepath.Join(root, "WORKSHOP.md"), 0444); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReconstructCompletionOverlay(b, c, o); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{b, c, o} {
		info, err := os.Stat(filepath.Join(root, "WORKSHOP.md"))
		if err != nil || info.Mode().Perm() != 0444 {
			t.Fatalf("%s permission changed: %v", root, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(o, "WORKSHOP.md"))
	if err != nil || string(data) != "new claims" {
		t.Fatalf("correction missing: %q %v", data, err)
	}
}

func TestCompletionOverlayDocumentBudgets(t *testing.T) {
	for _, tc := range []struct {
		name        string
		count, size int
	}{{"count", completionMaxDocuments + 1, 1}, {"total", 9, completionMaxDocument}} {
		t.Run(tc.name, func(t *testing.T) {
			b, c, o := completionFixture(t)
			for i := 0; i < tc.count; i++ {
				name := filepath.Join(c, CompletionSupplementDir, fmt.Sprintf("doc%d.txt", i))
				if err := os.WriteFile(name, []byte(strings.Repeat("a", tc.size)), 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := ReconstructCompletionOverlay(b, c, o); err == nil {
				t.Fatal("accepted excessive overlay")
			}
		})
	}
}

// Run umask changes in isolated processes; never change this test runner's umask.
func TestCompletionOverlayUmask(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix umask")
	}
	if os.Getenv("LECTERN_COMPLETION_UMASK_HELPER") == "1" {
		receipt, err := ReconstructCompletionOverlay(os.Getenv("LECTERN_COMPLETION_BASE"), os.Getenv("LECTERN_COMPLETION_CANDIDATE"), os.Getenv("LECTERN_COMPLETION_OUTPUT"))
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("LECTERN_COMPLETION_RECEIPT"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	base, candidate, output := completionFixture(t)
	receipts := make([]CompletionOverlayReceipt, 0, 2)
	for _, mask := range []string{"022", "077"} {
		out := output + mask
		record := out + ".json"
		command := exec.Command("/bin/sh", "-c", `umask "$1"; shift; exec "$@"`, "umask-test", mask, os.Args[0], "-test.run=^TestCompletionOverlayUmask$")
		command.Env = append(os.Environ(), "LECTERN_COMPLETION_UMASK_HELPER=1", "LECTERN_COMPLETION_BASE="+base, "LECTERN_COMPLETION_CANDIDATE="+candidate, "LECTERN_COMPLETION_OUTPUT="+out, "LECTERN_COMPLETION_RECEIPT="+record)
		if data, err := command.CombinedOutput(); err != nil {
			t.Fatalf("umask %s: %s %v", mask, data, err)
		}
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		var receipt CompletionOverlayReceipt
		if err = json.Unmarshal(data, &receipt); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
		for name, want := range map[string]os.FileMode{".": 0700, CompletionSupplementDir: 0755, CompletionOriginalWorkshop: 0444, CompletionSupplementDir + "/provenance.md": 0644} {
			info, err := os.Stat(filepath.Join(out, name))
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Fatalf("umask %s %s mode=%o want %o", mask, name, info.Mode().Perm(), want)
			}
		}
	}
	if receipts[0].DerivedSHA256 != receipts[1].DerivedSHA256 || receipts[0].OverlaySHA256 != receipts[1].OverlaySHA256 {
		t.Fatalf("umask changed receipt: %+v", receipts)
	}
}
