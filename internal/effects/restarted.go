package effects

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// systemdTimeLayout is the form systemctl prints ActiveEnterTimestamp in, for
// example "Tue 2026-10-13 08:01:02 UTC".
const systemdTimeLayout = "Mon 2006-01-02 15:04:05 MST"

// unitProps are the properties the restart probe reads.
var unitProps = []string{"ActiveState", "LoadState", "ActiveEnterTimestamp"}

// probeRestarted asks systemd when the unit last became active. A process that
// started before the turn did not run the change the agent says it deployed.
func probeRestarted(ctx context.Context, ex executor.Executor, dir, unit string, since time.Time) Verdict {
	if unit == "" {
		return unknown("no unit named in the claim")
	}
	props, scope, err := showUnit(ctx, ex, dir, unit)
	if err != nil {
		return unknown("%v", err)
	}
	state := props["ActiveState"]
	switch state {
	case "failed", "inactive", "deactivating":
		return contradicted("%s is %s (%s)", unit, state, scope)
	case "active", "reloading":
	default:
		return unknown("%s is %q, not settled", unit, state)
	}
	entered, err := parseSystemdTime(props["ActiveEnterTimestamp"])
	if err != nil {
		return unknown("%s is active, but its start time is unreadable: %q", unit, props["ActiveEnterTimestamp"])
	}
	if entered.Before(since) {
		return contradicted("%s still runs the process started at %s, before this turn (%s); the restart was not observed",
			unit, props["ActiveEnterTimestamp"], since.Format(time.RFC3339))
	}
	return verified("%s is active since %s, after this turn started", unit, props["ActiveEnterTimestamp"])
}

// showUnit reads the unit properties from the system manager, then from the
// user manager. It reports which one knew the unit.
func showUnit(ctx context.Context, ex executor.Executor, dir, unit string) (map[string]string, string, error) {
	args := ""
	for _, p := range unitProps {
		args += " -p " + p
	}
	for _, scope := range []string{"", " --user"} {
		name := "system"
		if scope != "" {
			name = "user"
		}
		res, err := run(ctx, ex, dir, "systemctl"+scope+" show "+executor.ShellQuote(unit)+args)
		if err != nil {
			return nil, name, err
		}
		props := parseShow(res.Stdout)
		if props["LoadState"] == "loaded" {
			return props, name + " manager", nil
		}
	}
	return nil, "", fmt.Errorf("unit %s is not loaded by the system or user manager", unit)
}

// parseShow parses systemctl show output, one key=value pair per line. Values
// may themselves contain "=", so only the first one splits.
func parseShow(out string) map[string]string {
	props := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

// parseSystemdTime reads ActiveEnterTimestamp. systemd prints host-local time,
// so a zone abbreviation the local zone does not know is read as UTC.
func parseSystemdTime(s string) (time.Time, error) {
	return time.ParseInLocation(systemdTimeLayout, s, time.Local)
}
