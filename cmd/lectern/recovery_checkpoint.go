package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/JeremiahM37/lectern/internal/config"
	"github.com/JeremiahM37/lectern/internal/executor"
	"github.com/JeremiahM37/lectern/internal/sessions"
	"github.com/JeremiahM37/lectern/internal/store"
)

func recoveryCheckpoint(cfg *config.Config, args []string) error {
	if len(args) != 2 || (args[0] != "export" && args[0] != "import") {
		return errors.New("usage: lectern recovery-checkpoint export|import PATH")
	}
	path, err := filepath.Abs(args[1])
	if err != nil {
		return err
	}
	if args[0] == "export" {
		// Export opens the source DB read-only and uses real target executors. In
		// particular, do not call store.Open here: it would migrate the old DB
		// before the checkpoint has captured it.
		reg := executor.NewRegistry(false, 0)
		m, err := sessions.ExportCheckpoint(context.Background(), cfg.DBPath, func(t *store.Target) (executor.Executor, error) {
			return reg.For(t)
		})
		if err != nil {
			return err
		}
		if err = sessions.WriteCheckpoint(path, m); err != nil {
			return err
		}
		fmt.Printf("exported %d session checkpoints to %s\n", len(m.Sessions), path)
		for _, s := range m.Sessions {
			if s.IdentityState == sessions.IdentityUnknown {
				fmt.Printf("unsupported native identity for session %d; checkpoint coverage is incomplete\n", s.ID)
			}
		}
		return nil
	}
	report, err := sessions.ImportCheckpoint(path, cfg.DBPath)
	if err != nil {
		return err
	}
	fmt.Printf("imported %d session checkpoints\n", report.Imported)
	for _, skipped := range report.Skipped {
		fmt.Printf("skipped session %d: %s\n", skipped.ID, skipped.Reason)
	}
	return nil
}
