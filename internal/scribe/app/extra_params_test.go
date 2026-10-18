package app

import (
	"encoding/json"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func extraBlock(kv map[string]string) *config.ExtraParamMap {
	m := config.ExtraParamMap{}
	for k, v := range kv {
		m[k] = json.RawMessage(v)
	}
	return &m
}

func TestApplyExtraParamsCopiesBlock(t *testing.T) {
	var r llm.Request
	applyExtraParams(&r, config.Reasoning{ExtraParams: extraBlock(map[string]string{"top_k": "5"})})
	if string(r.ExtraParams["top_k"]) != "5" {
		t.Fatalf("extra params not copied: %v", r.ExtraParams)
	}
}

func TestApplyExtraParamsNilLeavesRequestAlone(t *testing.T) {
	var r llm.Request
	applyExtraParams(&r, config.Reasoning{})
	applyExtraParams(&r, config.Reasoning{ExtraParams: &config.ExtraParamMap{}})
	if r.ExtraParams != nil {
		t.Fatalf("empty block changed the request: %v", r.ExtraParams)
	}
}

func TestExtraParamsReachOpenAICompatibleWire(t *testing.T) {
	rs := config.Reasoning{ExtraParams: extraBlock(map[string]string{
		"enable_thinking": "true",
		"model":           `"hijacked"`,
	})}
	var r llm.Request
	r.Model = "qwen-plus"
	r.Messages = []llm.Message{llm.UserText("hi")}
	applyExtraParams(&r, rs)
	body := openAIWireBody(t, "myproxy", r)
	if body["enable_thinking"] != true {
		t.Fatalf("enable_thinking not on the wire: %v", body)
	}
	if body["model"] != "qwen-plus" {
		t.Fatalf("model taken from extra_params: %v", body["model"])
	}
}

func TestExtraParamsIgnoredByAnthropicAdapter(t *testing.T) {
	// The Anthropic adapter does not read ExtraParams, so an extra_params
	// block set for it sends nothing extra.
	rs := config.Reasoning{ExtraParams: extraBlock(map[string]string{"top_k": "5"})}
	req := tuned("claude-opus-4-7", config.Performance{}, rs)
	applyExtraParams(&req, rs)
	body := wireBody(t, req)
	if _, ok := body["top_k"]; ok {
		t.Fatalf("anthropic body picked up an extra param: %v", body)
	}
}
