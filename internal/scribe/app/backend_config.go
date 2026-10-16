package app

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// MoveConfig saves key to the project or the global config and removes it from
// the other scope, so a setting lives in exactly one place after a scope move.
// It is SetConfig followed by clearing the key in the old file. Moving a key
// to the scope it is already in just saves it there.
func (b *scribeBackend) MoveConfig(key string, value any, toProject, fromProject bool) error {
	if toProject == fromProject {
		return b.SetConfig(key, value, toProject)
	}
	// Refuse before writing anything if the old scope could not drop the key.
	if err := clearConfigField(&config.Config{}, key); err != nil {
		return err
	}
	if err := b.SetConfig(key, value, toProject); err != nil {
		return err
	}
	return b.clearConfig(key, fromProject)
}

// clearConfig removes one key from the project or the global file and reloads
// the merged settings. A key that is not in that file is not an error.
func (b *scribeBackend) clearConfig(key string, project bool) error {
	a := b.current()
	path := configPathFor(a.Workdir, project)
	raw, err := loadConfigFile(path)
	if err != nil {
		return err
	}
	if err := clearConfigField(&raw, key); err != nil {
		return err
	}
	if err := saveConfigFile(a.Workdir, raw, project); err != nil {
		return err
	}
	merged, err := reloadConfig(a.Workdir)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.Cfg = merged
	a.mu.Unlock()
	return nil
}

// clearConfigField resets the field that setConfigField writes for key to its
// zero value, so the omitempty JSON tag drops it from the file.
func clearConfigField(c *config.Config, key string) error {
	switch key {
	case "model":
		c.Model = ""
	case "mode":
		c.Mode = ""
	case "theme":
		c.Theme = ""
	case "reasoning":
		c.Reasoning = ""
	case "max_tokens":
		c.MaxTokens = 0
	case "compact_at":
		c.CompactAt = 0
	case "chat_style":
		c.ChatStyle = ""
	case "search_url":
		c.SearchURL = ""
	case "allow":
		c.Allow = nil
	case "deny":
		c.Deny = nil
	case "instructions":
		c.Instructions = nil
	case "router.explore":
		c.Router.Explore = ""
	case "router.code":
		c.Router.Code = ""
	case "router.web_search":
		c.Router.WebSearch = ""
	case "router.compact":
		c.Router.Compact = ""
	case "router.title":
		c.Router.Title = ""
	case "router.fallback":
		c.Router.Fallback = nil
	case "router.max_agents":
		c.Router.MaxAgents = 0
	default:
		ptr := boolField(c, key)
		if ptr == nil {
			return fmt.Errorf("%q cannot be moved between scopes", key)
		}
		*ptr = nil
	}
	return nil
}

// configPathFor returns the project or the global config file.
func configPathFor(root string, project bool) string {
	if project {
		return config.ProjectPath(root)
	}
	return config.GlobalPath()
}

// saveConfigFile writes raw to the project or the global config file.
func saveConfigFile(root string, raw config.Config, project bool) error {
	if project {
		return config.SaveProject(root, raw)
	}
	return config.SaveGlobal(raw)
}

// reloadConfig returns the merged settings for root.
func reloadConfig(root string) (config.Config, error) {
	return config.Load(root)
}
