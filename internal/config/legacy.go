package config

import (
	"os"
	"sort"
	"strings"
)

// LegacyEnvPrefix is the prefix every setting carried before the project was
// renamed. An installed instance still has its unit file, drop-ins and shell
// profiles written with it, and a session launched by the previous binary
// still exports its identity under it.
const LegacyEnvPrefix = "AGENTDECK_"

// EnvPrefix is the prefix every setting carries now.
const EnvPrefix = "LECTERN_"

// AliasLegacyEnv makes every AGENTDECK_* variable readable under its LECTERN_*
// name for the rest of this process, without overriding a LECTERN_* value that
// is already set. It returns the names it aliased so the server can say so
// once at startup; the CLI and the MCP child stay quiet.
func AliasLegacyEnv() []string {
	var aliased []string
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, LegacyEnvPrefix) {
			continue
		}
		current := EnvPrefix + strings.TrimPrefix(name, LegacyEnvPrefix)
		if _, set := os.LookupEnv(current); set {
			continue
		}
		os.Setenv(current, value)
		aliased = append(aliased, name)
	}
	sort.Strings(aliased)
	return aliased
}

func init() {
	legacyAliased = AliasLegacyEnv()
}

// legacyAliased records what init aliased, for Load to report.
var legacyAliased []string

// LegacyEnvAliased lists the AGENTDECK_* names this process is reading through
// their LECTERN_* equivalents.
func LegacyEnvAliased() []string { return append([]string(nil), legacyAliased...) }
