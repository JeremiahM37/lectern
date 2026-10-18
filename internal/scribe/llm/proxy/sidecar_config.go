package proxy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// defaultProxyPort is CLIProxyAPI's own default, and the port the provider
// uses when its URL names none.
const defaultProxyPort = 8317

// loopbackHost is where a sidecar Scribe writes is bound. The proxy is never
// exposed to the network by Scribe.
const loopbackHost = "127.0.0.1"

// sidecarConfig is the part of CLIProxyAPI's config.yaml that Scribe reads and
// writes. Other keys in a user's file are left alone, because the file is only
// written when it does not exist.
type sidecarConfig struct {
	Host    string   `yaml:"host"`
	Port    int      `yaml:"port"`
	AuthDir string   `yaml:"auth-dir"`
	APIKeys []string `yaml:"api-keys"`
}

// loadSidecarConfig reads config.yaml. ok is false when the file does not
// exist, which is not an error: the first start creates it.
func loadSidecarConfig(path string) (cfg sidecarConfig, ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, fmt.Errorf("proxy: cannot read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, true, fmt.Errorf("proxy: %s is not valid YAML: %w", path, err)
	}
	return cfg, true, nil
}

// writeSidecarConfig writes a new config.yaml readable only by its owner,
// since it holds the proxy's API keys.
func writeSidecarConfig(path string, cfg sidecarConfig) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# Written by Scribe. Scribe reads host, port, auth-dir and api-keys.\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("proxy: cannot create %s: %w", filepath.Dir(path), err)
	}
	if err := writeFileAtomic(path, append([]byte(header), b...), 0o600); err != nil {
		return fmt.Errorf("proxy: cannot write %s: %w", path, err)
	}
	return nil
}

// ensureConfig returns the config, writing a new one first when none exists.
// A new file gets a fresh random key, so the sidecar is never reachable with
// a key that is known in advance.
func (s *Sidecar) ensureConfig() (sidecarConfig, error) {
	cfg, ok, err := loadSidecarConfig(s.configPath())
	if err != nil {
		return cfg, err
	}
	if !ok {
		key, err := newKey()
		if err != nil {
			return cfg, err
		}
		cfg = sidecarConfig{Host: loopbackHost, Port: s.port, AuthDir: s.authDir, APIKeys: []string{key}}
		if err := writeSidecarConfig(s.configPath(), cfg); err != nil {
			return cfg, err
		}
	}
	port := cfg.Port
	if port == 0 {
		port = defaultProxyPort
	}
	if port != s.port {
		return cfg, fmt.Errorf("proxy: %s listens on port %d, but the provider URL uses %d; make them match", s.configPath(), port, s.port)
	}
	return cfg, nil
}

// newKey returns a random key as hex. It is 192 bits, and 48 characters on
// purpose: a 64-character hex string is a SHA-256 digest to UsableKey, and a
// digest is refused as a bearer token.
func newKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("proxy: cannot make a key: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// writeFileAtomic writes through a temporary file and a rename, so a crash
// never leaves a half-written config or pid record.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// loopbackHostname reports whether a host names this machine.
func loopbackHostname(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
