//go:build linux

package api

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSourceActivityRejectsNonregularIndexes(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			_, _, dir := sourceFixture(t)
			index := filepath.Join(dir, ".git", "index")
			if err := os.Rename(index, index+".saved"); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(index+".saved", index); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := syscall.Mkfifo(index, 0600); err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			if _, err := autoReadActivityIndex(index); err == nil {
				t.Fatal("nonregular index accepted")
			}
			if time.Since(start) > time.Second {
				t.Fatal("nonregular open blocked")
			}
			if row := autoSourceActivity(context.Background(), dir); row["status"] != "unknown" {
				t.Fatal(row)
			}
		})
	}
}
