package nvim

import "sync"

// Settings the editor takes from the config (see config.Config.EditorConfig and
// EditorSync). They are read when the editor starts or an edit arrives, so a
// change applies to the next editor, and to the next edit for sync.
var (
	setMu      sync.Mutex
	startCfg   = "user"
	syncOnEdit = true
)

// SetStartConfig picks the Neovim config a new editor starts with. "none"
// starts Neovim with -u NONE; anything else, including "", means the user's own
// config. (A LazyVim bootstrap mode is not built: it needs the network on first
// run and a shipped init.lua.)
func SetStartConfig(mode string) {
	setMu.Lock()
	defer setMu.Unlock()
	if mode == "none" {
		startCfg = "none"
		return
	}
	startCfg = "user"
}

// StartConfig is the Neovim config mode a new editor starts with.
func StartConfig() string {
	setMu.Lock()
	defer setMu.Unlock()
	return startCfg
}

// configArgs are the Neovim arguments the start config adds, before --listen.
func configArgs() []string {
	if StartConfig() == "none" {
		return []string{"-u", "NONE"}
	}
	return nil
}

// SetSyncOnEdit turns the follow of agent edits into the open buffer on or off.
func SetSyncOnEdit(on bool) {
	setMu.Lock()
	defer setMu.Unlock()
	syncOnEdit = on
}

// SyncOnEdit reports whether agent edits follow into the open buffer.
func SyncOnEdit() bool {
	setMu.Lock()
	defer setMu.Unlock()
	return syncOnEdit
}
