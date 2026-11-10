package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/JeremiahM37/lectern/v2/internal/backup"
	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/version"
)

// backupCommand is `lectern backup …` (internal/backup). It works on this
// machine's database directly, so it needs no running server — which is the
// point when restoring onto a new one.
func backupCommand(cfg *config.Config, args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	sub := "now"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "now", "run":
		r, closeDB, err := backupRunner(cfg)
		if err != nil {
			return err
		}
		defer closeDB()
		fmt.Fprintf(stdout, "Backing up to %s …\n", r.Settings().Repo)
		st, err := r.Run(ctx)
		if err != nil {
			return err
		}
		printBackupStatus(stdout, st)
		return nil
	case "setup":
		return backupSetup(ctx, cfg, args, stdout)
	case "export":
		if len(args) != 1 {
			return errors.New("usage: lectern backup export FILE")
		}
		r, closeDB, err := backupRunner(cfg)
		if err != nil {
			return err
		}
		defer closeDB()
		if !r.Settings().HasPassphrase() {
			return errors.New("no backup passphrase yet: run `lectern backup setup` first")
		}
		f, err := os.OpenFile(args[0], os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		m, err := r.Export(ctx, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(args[0])
			return err
		}
		fmt.Fprintf(stdout, "Wrote %s: %d sessions, %d files, encrypted.\n", args[0], m.Sessions, m.Files)
		return nil
	case "list":
		r, closeDB, err := backupRunner(cfg)
		if err != nil {
			return err
		}
		defer closeDB()
		gh, err := r.Repo(ctx)
		if err != nil {
			return err
		}
		rels, err := gh.List(ctx)
		if err != nil {
			return err
		}
		if len(rels) == 0 {
			fmt.Fprintf(stdout, "No backups in %s yet.\n", gh.Repo)
		}
		for _, rel := range rels {
			fmt.Fprintf(stdout, "%s  %s  %s\n", rel.Tag, rel.CreatedAt.Local().Format("2006-01-02 15:04"), humanBytes(rel.Size()))
		}
		return nil
	case "restore":
		return backupRestore(ctx, cfg, args, stdout, stderr)
	}
	return fmt.Errorf("lectern backup: unknown command %q (see lectern backup --help)", sub)
}

func backupRunner(cfg *config.Config) (*backup.Runner, func(), error) {
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return nil, nil, fmt.Errorf("no Lectern database at %s (set LECTERN_DB, or restore one with `lectern backup restore`)", cfg.DBPath)
	}
	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, nil, err
	}
	reg := executor.NewRegistry(false, 0)
	r := &backup.Runner{Store: db, TempDir: filepath.Dir(cfg.DBPath),
		Source: func(context.Context) (backup.Source, error) {
			src, err := backup.FromLectern(db, cfg.DBPath, cfg.MediaPath, reg)
			src.Version = version.Version
			return src, err
		}}
	return r, func() { db.Close() }, nil
}

func backupSetup(ctx context.Context, cfg *config.Config, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("backup setup", flag.ContinueOnError)
	every := fs.Int("every", 24, "hours between automatic backups (0 = only when asked)")
	keep := fs.Int("keep", 14, "how many backups to keep")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: lectern backup setup OWNER/REPO [--every HOURS] [--keep N]")
	}
	r, closeDB, err := backupRunner(cfg)
	if err != nil {
		return err
	}
	defer closeDB()
	s := r.Settings()
	s.Repo, s.EveryHours, s.Keep = fs.Arg(0), *every, *keep
	generated := ""
	if s.Passphrase == "" {
		generated = backup.NewPassphrase()
		s.Passphrase = generated
	}
	if err := r.SaveSettings(s); err != nil {
		return err
	}
	gh, err := r.Repo(ctx)
	if err != nil {
		return err
	}
	created, err := gh.EnsureRepo(ctx)
	if err != nil {
		return err
	}
	if created {
		fmt.Fprintf(stdout, "Created private repository %s.\n", gh.Repo)
	} else {
		fmt.Fprintf(stdout, "Using private repository %s.\n", gh.Repo)
	}
	if generated != "" {
		fmt.Fprintf(stdout, "\nBackup passphrase (save it in your password manager NOW — without it the backups cannot be opened, and it is not stored anywhere but this machine):\n\n    %s\n\n", generated)
	}
	if s.EveryHours > 0 {
		fmt.Fprintf(stdout, "The server backs up every %d hours and keeps the last %d. Back up now with: lectern backup\n", s.EveryHours, s.Keep)
	}
	return nil
}

func backupRestore(ctx context.Context, cfg *config.Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	repo := fs.String("repo", "", "OWNER/REPO to download from (default: the configured one)")
	force := fs.Bool("force", false, "restore even though a Lectern server is answering, and replace existing files")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		return err
	}
	which := "latest"
	if fs.NArg() > 1 {
		return errors.New("usage: lectern backup restore [latest|TAG|FILE] [--repo OWNER/REPO] [--force]")
	}
	if fs.NArg() == 1 {
		which = fs.Arg(0)
	}
	if !*force && serverAnswering(cfg.Port) {
		return fmt.Errorf("a Lectern server is running on port %d; stop it first (sudo systemctl stop lectern) or pass --force", cfg.Port)
	}
	pass := os.Getenv("LECTERN_BACKUP_PASSPHRASE")
	if pass == "" {
		if r, closeDB, err := backupRunner(cfg); err == nil {
			pass = r.Settings().Passphrase
			closeDB()
		}
	}
	if pass == "" {
		var err error
		if pass, err = askPassphrase(stderr); err != nil {
			return err
		}
	}
	home, _ := os.UserHomeDir()
	to := backup.Target{DBPath: cfg.DBPath, Home: home, MediaDir: cfg.MediaPath, Overwrite: *force}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return err
	}
	var res *backup.Result
	if f, err := os.Open(which); err == nil {
		defer f.Close()
		if res, err = backup.Restore(f, pass, to); err != nil {
			return err
		}
	} else {
		name := *repo
		if name == "" {
			name = strings.TrimSpace(os.Getenv("LECTERN_BACKUP_REPO"))
		}
		if name == "" {
			if r, closeDB, err := backupRunner(cfg); err == nil {
				name = r.Settings().Repo
				closeDB()
			}
		}
		if name == "" {
			return errors.New("which repository? pass --repo OWNER/REPO")
		}
		token, _ := backup.GitHubToken(ctx)
		if token == "" {
			return errors.New("no GitHub login: run `gh auth login` first, or set LECTERN_BACKUP_GITHUB_TOKEN")
		}
		gh := &backup.GitHub{Repo: name, Token: token}
		rel, err := gh.Find(ctx, which)
		if err != nil {
			return err
		}
		res, err = gh.RestoreRelease(ctx, rel, pass, to, filepath.Dir(cfg.DBPath), func(line string) { fmt.Fprintln(stdout, line) })
		if err != nil {
			return err
		}
	}
	m := res.Manifest
	fmt.Fprintf(stdout, "Restored the backup of %s from %s (Lectern %s).\n", m.Host, m.CreatedAt.Local().Format("2006-01-02 15:04"), m.Version)
	if res.Database {
		fmt.Fprintf(stdout, "  database: %s (%d sessions)\n", cfg.DBPath, m.Sessions)
	}
	fmt.Fprintf(stdout, "  files: %d restored", res.Files)
	if res.Kept > 0 {
		fmt.Fprintf(stdout, ", %d already here and kept (--force replaces them)", res.Kept)
	}
	fmt.Fprintln(stdout)
	if res.EnvFile != "" {
		fmt.Fprintf(stdout, "  settings: %s — the old server's LECTERN_* values; copy what you need into /etc/lectern.env\n", res.EnvFile)
	}
	for _, p := range res.Remote {
		fmt.Fprintf(stdout, "  transcripts from another machine: %s (unpack on that machine with tar -xzf FILE -C /)\n", p)
	}
	if len(m.Skipped) > 0 {
		fmt.Fprintf(stdout, "  %d items were not in the backup when it was made (see manifest)\n", len(m.Skipped))
	}
	fmt.Fprintln(stdout, "Start Lectern again to use it.")
	return nil
}

func askPassphrase(stderr io.Writer) (string, error) {
	fmt.Fprint(stderr, "Backup passphrase: ")
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no passphrase given (set LECTERN_BACKUP_PASSPHRASE)")
	}
	return strings.TrimSpace(line), nil
}

func serverAnswering(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func printBackupStatus(w io.Writer, st backup.Status) {
	fmt.Fprintf(w, "Backed up %d sessions and %d files (%s, encrypted): %s\n", st.Sessions, st.Files, humanBytes(st.Bytes), st.URL)
	if len(st.Skipped) > 0 {
		fmt.Fprintf(w, "%d items could not be read:\n", len(st.Skipped))
		for i, s := range st.Skipped {
			if i == 10 {
				fmt.Fprintf(w, "  … and %d more\n", len(st.Skipped)-10)
				break
			}
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// reorderFlags lets flags follow the positional argument
// (`setup me/repo --every 12`), which Go's flag package stops at.
func reorderFlags(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			continue
		}
		flags = append(flags, a)
		if !strings.Contains(a, "=") && a != "--force" && a != "-force" && i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	return append(flags, rest...)
}
