package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

// Slash-command ranking. A command's score is its fuzzy match on the typed
// text, plus a boost when a name starts with the text (+100, and +50 more when
// it is exact). The command's place in the frecency store multiplies that:
// the most frecent command gets the largest factor, and commands outside the
// top few get 1. Ties sort by name.

const (
	slashPrefixBoost = 100
	slashExactBoost  = 50
	frecencyCap      = 4
)

// rankSlash returns the commands that match q, best first.
func rankSlash(q string) []*command {
	ranks := frecencyRanks()
	type ranked struct {
		c     *command
		score int
	}
	var hits []ranked
	for _, c := range allCommands() {
		best, ok := 0, false
		for _, name := range append([]string{c.Name}, c.Aliases...) {
			s, hit := fuzzyScore(name, q)
			if !hit {
				continue
			}
			if q != "" && strings.HasPrefix(name, q) {
				s += slashPrefixBoost
				if name == q {
					s += slashExactBoost
				}
			}
			if !ok || s > best {
				best, ok = s, true
			}
		}
		if ok {
			hits = append(hits, ranked{c, best * frecencyFactor(ranks, c.Name)})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].c.Name < hits[j].c.Name
	})
	out := make([]*command, len(hits))
	for i, h := range hits {
		out[i] = h.c
	}
	return out
}

// frecencyRanks maps each frecently picked command to its rank, 0 being the
// most frecent. It reads the shared session frecency store, so the palette,
// the slash popup and the model picker all rank from the same record.
func frecencyRanks() map[string]int {
	migrateLegacyFrecency()
	names, err := session.TopPicks(session.PickCommand, frecencyCap)
	if err != nil {
		return nil
	}
	ranks := make(map[string]int, len(names))
	for i, n := range names {
		ranks[n] = i
	}
	return ranks
}

// frecencyFactor is 1 + the boost for a command's rank: rank 0 gets the most,
// and anything not in the top frecencyCap gets 1.
func frecencyFactor(ranks map[string]int, name string) int {
	r, ok := ranks[name]
	if !ok {
		return 1
	}
	return 1 + (frecencyCap - r)
}

// bumpFrecency records one run of a slash command in the shared store. A
// failed write is ignored: the palette still ranks from this session's order.
func bumpFrecency(name string) {
	migrateLegacyFrecency()
	_ = session.RecordPick(session.PickCommand, name)
}

// Before the session store took over frecency.json, the slash popup kept a
// JSON object of command name to run count in the same file, and the two
// formats cannot be read as each other. This converts the old object into the
// store's list once, so existing counts survive. It is a no-op for a list.
var legacyFrecencyOnce sync.Once

func migrateLegacyFrecency() {
	legacyFrecencyOnce.Do(func() {
		path := filepath.Join(config.Home(), "frecency.json")
		b, err := os.ReadFile(path)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 || strings.TrimSpace(string(b))[0] != '{' {
			return
		}
		counts := map[string]int{}
		if json.Unmarshal(b, &counts) != nil {
			return
		}
		type entry struct {
			Kind  string    `json:"kind"`
			Key   string    `json:"key"`
			Count int       `json:"count"`
			Last  time.Time `json:"last"`
		}
		now := time.Now().UTC()
		var list []entry
		for name, n := range counts {
			if name != "" && n > 0 {
				list = append(list, entry{Kind: session.PickCommand, Key: name, Count: n, Last: now})
			}
		}
		out, err := json.Marshal(list)
		if err != nil {
			return
		}
		tmp := path + ".tmp"
		if os.WriteFile(tmp, out, 0o600) == nil {
			_ = os.Rename(tmp, path)
		}
	})
}
