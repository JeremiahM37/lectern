package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/proxy"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/registry"
)

func TestProxySidecarNeedsARegistry(t *testing.T) {
	b, _ := newTestBackend(t, t.TempDir())
	if sc := b.ProxySidecar(); sc != nil {
		t.Fatal("no registry means no proxy to manage")
	}
	if err := b.StopProxy(); err != nil {
		t.Fatalf("StopProxy with no proxy = %v, want nil", err)
	}
}

func TestProxySidecarFromRegistry(t *testing.T) {
	t.Setenv("SCRIBE_PROXY_URL", "127.0.0.1:9999")
	b, a := newTestBackend(t, t.TempDir())
	a.Registry = registry.New(config.Config{})
	if sc := b.ProxySidecar(); sc == nil {
		t.Fatal("the proxy provider should give a sidecar")
	}
}

func TestIsProxyModelOnlyForTheProxyProvider(t *testing.T) {
	p := proxy.New(proxy.Options{BaseURL: "http://127.0.0.1:8317/v1"})
	if !isProxyModel(agent.Model{Ref: "proxy/claude-x", Provider: p}) {
		t.Error("a model served by the proxy should count")
	}
	if isProxyModel(agent.Model{Ref: "x/y"}) {
		t.Error("a model with no provider should not count")
	}
}

func TestProxyStartIsClaimedOnce(t *testing.T) {
	if !beginProxyStart() {
		t.Fatal("first start should be claimed")
	}
	if beginProxyStart() {
		t.Fatal("a second start must wait for the first")
	}
	endProxyStart()
	if !beginProxyStart() {
		t.Fatal("after the first ends, a start should be claimed again")
	}
	endProxyStart()
}

func TestEnsureProxyWithoutAnAgentDoesNothing(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	b, _ := newTestBackend(t, t.TempDir())
	// No agent means no current model, so nothing starts and nothing blocks.
	b.EnsureProxy()
	if !beginProxyStart() {
		t.Fatal("EnsureProxy without a model must not hold the start guard")
	}
	endProxyStart()
}
