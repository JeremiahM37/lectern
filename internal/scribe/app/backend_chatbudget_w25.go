package app

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// ChatBudget reports this chat's spend today against its per-chat limit. The
// spend is read under budgetMu because recordDailyTokens writes the same file.
func (b *scribeBackend) ChatBudget() tui.ChatBudget {
	a := b.current()
	budgetMu.Lock()
	spent := loadDailyUse(budgetNow()).Chats[a.chatKey()].Tokens
	budgetMu.Unlock()
	return tui.ChatBudget{Today: spent, Limit: a.liveConfig().ChatDailyBudget()}
}

// SetChatBudget saves chat_daily_token_budget to the global config and makes
// it live for the running session. It writes the typed field directly because
// SetConfig has no case for this key.
func (b *scribeBackend) SetChatBudget(n int) error {
	if n < 0 {
		return fmt.Errorf("the per-chat token budget cannot be negative")
	}
	a := b.current()
	raw, err := loadConfigFile(config.GlobalPath())
	if err != nil {
		return err
	}
	raw.ChatDailyTokenBudget = &n
	if err := config.SaveGlobal(raw); err != nil {
		return err
	}
	merged, err := config.Load(a.Workdir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Cfg = merged
	a.mu.Unlock()
	return nil
}
