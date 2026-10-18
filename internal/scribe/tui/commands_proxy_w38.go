package tui

// Wave 38: /proxy status, /proxy login <provider> and /proxy logout <provider>
// for the CLIProxyAPI sidecar behind the proxy provider (spec 02 /proxy rows,
// spec 03 section 11). The sidecar lives in llm/proxy; this file only decides
// what the command shows. install, start, stop, restart and upgrade are not
// here yet.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/proxy"
)

// ProxyManager is the optional backend interface /proxy uses. ProxySidecar
// returns the sidecar for the proxy provider's address, or nil when the
// session has no proxy provider.
type ProxyManager interface {
	ProxySidecar() *proxy.Sidecar
}

// proxyWait bounds the probes /proxy makes: the status check and the check
// after a login.
const proxyWait = 5 * time.Second

func init() {
	registerCommands(
		&command{Name: "proxy", Args: "[status|login <provider>|logout <provider>]",
			Desc: "CLIProxyAPI proxy: show its status, or sign a provider in or out", Group: "Models",
			Run: func(m *Model, args string) tea.Cmd { return proxyCommand(m, strings.TrimSpace(args)) }},
	)
}

// proxyCommand dispatches /proxy. With no argument it shows the status.
func proxyCommand(m *Model, args string) tea.Cmd {
	pm, ok := m.cur().be.(ProxyManager)
	if !ok {
		return cmdErrText("this session has no proxy support")
	}
	sc := pm.ProxySidecar()
	if sc == nil {
		return cmdErrText("there is no proxy provider to manage")
	}
	f := strings.Fields(args)
	sub, arg := "status", ""
	if len(f) > 0 {
		sub = strings.ToLower(f[0])
	}
	if len(f) > 1 {
		arg = f[1]
	}
	if len(f) > 2 {
		return proxyUsage()
	}
	switch sub {
	case "status":
		if arg != "" {
			return proxyUsage()
		}
		return proxyStatusCommand(m, sc)
	case "login":
		return proxyLoginCommand(m, sc, arg)
	case "logout":
		return proxyLogoutCommand(sc, arg)
	}
	return proxyUsage()
}

func proxyUsage() tea.Cmd {
	return cmdErrText("Usage: /proxy status, /proxy login <provider>, or /proxy logout <provider>. Providers: " +
		strings.Join(proxy.LoginProviders(), ", "))
}

// proxyStatusCommand opens the status popup. The probe is bounded and goes to
// a loopback address, so it runs before the popup opens.
func proxyStatusCommand(m *Model, sc *proxy.Sidecar) tea.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), proxyWait)
	defer cancel()
	m.openInfo("Proxy status", "gauge", proxyStatusLines(sc.Status(ctx)))
	return nil
}

// proxyStatusLines is the text of the status popup.
func proxyStatusLines(st proxy.SidecarStatus) []string {
	lines := []string{"Address: " + st.Base}
	switch st.State {
	case proxy.Running:
		lines = append(lines, "State: running")
	case proxy.NeedsAuth:
		lines = append(lines, "State: needs login. The proxy rejected every key. Run /proxy login <provider>.")
	default:
		lines = append(lines, "State: not running. Scribe starts it when a proxy model is in use.")
	}
	if st.State != proxy.Unreachable {
		if st.Owned {
			lines = append(lines, "Started by: this Scribe")
		} else {
			lines = append(lines, "Started by: something other than this Scribe")
		}
	}
	if st.Binary == "" {
		lines = append(lines, "Binary: not installed. "+proxyInstallText+".")
	} else {
		lines = append(lines, "Binary: "+st.Binary)
	}
	if len(st.Accounts) == 0 {
		lines = append(lines, "Accounts: none. /proxy login <provider> adds one.")
	} else {
		lines = append(lines, fmt.Sprintf("Accounts: %d (%s)", len(st.Accounts), strings.Join(st.Accounts, ", ")))
	}
	lines = append(lines, "Logins: "+strings.Join(proxy.LoginProviders(), ", "))
	return lines
}

// proxyInstallText says how to get the binary. It repeats the wording the
// llm/proxy package uses in its own not-installed error.
const proxyInstallText = "Install CLIProxyAPI from its GitHub releases (github.com/router-for-me/CLIProxyAPI) and put cliproxyapi on PATH"

// proxyProviderUsage is the message for a subcommand given no provider.
func proxyProviderUsage(sub string) tea.Cmd {
	return cmdErrText("Usage: /proxy " + sub + " <provider>. Providers: " + strings.Join(proxy.LoginProviders(), ", "))
}

// proxyLoginCommand hands the terminal to the proxy's own login for id. Once
// the login exits, the proxy is checked again and the result is flashed.
func proxyLoginCommand(m *Model, sc *proxy.Sidecar, id string) tea.Cmd {
	if id == "" {
		return proxyProviderUsage("login")
	}
	ctx, cancel := context.WithTimeout(context.Background(), proxyWait)
	defer cancel()
	plan, err := sc.PlanLogin(ctx, id)
	if err != nil {
		return cmdErr(err)
	}
	return tea.Sequence(m.runPassthrough(plan.Command), proxyLoginDone(sc, plan))
}

// proxyLoginDone is the command that runs after the login terminal is back.
func proxyLoginDone(sc *proxy.Sidecar, plan proxy.LoginPlan) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), proxyWait)
		defer cancel()
		return proxyLoginFlash(sc.FinishLogin(ctx, plan))
	}
}

// proxyLoginFlash says whether the login saved a credential, and how the
// proxy looks now. A login the user backed out of is not an error.
func proxyLoginFlash(res proxy.LoginResult) flashMsg {
	saved := append(append([]string{}, res.Added...), res.Updated...)
	if len(saved) == 0 {
		return flashMsg{kind: "info", text: fmt.Sprintf("No %s login was saved. Proxy: %s. Run /proxy login %s to try again.",
			res.Name, proxyStateText(res.State), res.Provider)}
	}
	return flashMsg{kind: "ok", text: fmt.Sprintf("%s login saved (%s). Proxy: %s.",
		res.Name, strings.Join(saved, ", "), proxyStateText(res.State))}
}

// proxyStateText is the state as a phrase for a flash line.
func proxyStateText(s proxy.State) string {
	switch s {
	case proxy.Running:
		return "running"
	case proxy.NeedsAuth:
		return "needs login (it rejected the key)"
	}
	return "not running"
}

// proxyLogoutCommand removes the saved accounts of one provider. There is no
// confirmation, which matches /proxy logout in spec 02.
func proxyLogoutCommand(sc *proxy.Sidecar, id string) tea.Cmd {
	if id == "" {
		return proxyProviderUsage("logout")
	}
	removed, err := sc.Logout(id)
	if err != nil {
		return cmdErr(err)
	}
	if len(removed) == 0 {
		return flashCmd("info", "No saved account for "+id+".")
	}
	return flashCmd("ok", fmt.Sprintf("Removed %d account file(s) for %s: %s. A running proxy may keep the login until it restarts.",
		len(removed), id, strings.Join(removed, ", ")))
}
