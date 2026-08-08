package main

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestPromptRulesInjectAcrossProtocols(t *testing.T) {
	rules := []compiledPromptRule{{promptRule: promptRule{Name: "inject", Enabled: true, Target: promptTargetSystem, Action: promptActionInject, Content: "POLICY", Position: promptPositionAppend}}}
	tests := []struct {
		format string
		body   string
		path   string
	}{
		{format: "openai", body: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, path: "messages.0.content"},
		{format: "openai-response", body: `{"model":"m","input":"hello"}`, path: "instructions"},
		{format: "claude", body: `{"model":"m","messages":[{"role":"user","content":"hello"}]}`, path: "system"},
		{format: "gemini", body: `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`, path: "systemInstruction.parts.0.text"},
		{format: "interactions", body: `{"input":"hello"}`, path: "system_instruction"},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			out := applyPromptRules(rules, test.format, "m", []byte(test.body), "/v1/chat/completions")
			if got := gjson.GetBytes(out, test.path).String(); got != "POLICY" {
				t.Fatalf("%s = %q, want POLICY; payload=%s", test.path, got, out)
			}
		})
	}
}

func TestPromptRulesLastNaturalUserAcrossProtocols(t *testing.T) {
	rules := []compiledPromptRule{{promptRule: promptRule{Name: "inject", Enabled: true, Target: promptTargetUser, Action: promptActionInject, Content: "!", Position: promptPositionAppend}}}
	tests := []struct {
		format string
		body   string
		path   string
	}{
		{format: "openai", body: `{"messages":[{"role":"user","content":"hello"},{"role":"tool","content":"result"}]}`, path: "messages.0.content"},
		{format: "openai-response", body: `{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"function_call_output","output":"result"}]}`, path: "input.0.content.1.text"},
		{format: "claude", body: `{"messages":[{"role":"user","content":"hello"},{"role":"user","content":[{"type":"tool_result","content":"result"}]}]}`, path: "messages.0.content"},
		{format: "gemini", body: `{"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"user","parts":[{"functionResponse":{"name":"x"}}]}]}`, path: "contents.0.parts.1.text"},
		{format: "interactions", body: `{"input":[{"type":"user_input","content":"hello"},{"type":"function_result","content":"result"}]}`, path: "input.0.content"},
	}
	for _, test := range tests {
		t.Run(test.format, func(t *testing.T) {
			out := applyPromptRules(rules, test.format, "m", []byte(test.body), "")
			if test.format == "openai-response" || test.format == "gemini" {
				if got := gjson.GetBytes(out, test.path).String(); got != "!" {
					t.Fatalf("injected block = %q, want !; payload=%s", got, out)
				}
				return
			}
			if got := gjson.GetBytes(out, test.path).String(); got != "hello!" {
				t.Fatalf("content = %q, want hello!; payload=%s", got, out)
			}
		})
	}
}

func TestPromptRulesStripRunsBeforeInjectAndInjectionIsIdempotent(t *testing.T) {
	rules := []compiledPromptRule{
		{promptRule: promptRule{Name: "inject", Enabled: true, Target: promptTargetSystem, Action: promptActionInject, Content: "KEEP", Position: promptPositionAppend}},
		{promptRule: promptRule{Name: "strip", Enabled: true, Target: promptTargetSystem, Action: promptActionStrip, Pattern: "DROP"}, pattern: regexp.MustCompile("DROP")},
	}
	body := []byte(`{"messages":[{"role":"system","content":"DROP"},{"role":"user","content":"hello"}]}`)
	first := applyPromptRules(rules, "openai", "m", body, "")
	second := applyPromptRules(rules, "openai", "m", first, "")
	if got := gjson.GetBytes(first, "messages.0.content").String(); got != "KEEP" {
		t.Fatalf("first content = %q, want KEEP", got)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("second application changed payload:\nfirst=%s\nsecond=%s", first, second)
	}
}

func TestPromptRulesMarkerAndModelScope(t *testing.T) {
	rules := []compiledPromptRule{{promptRule: promptRule{
		Name: "marker", Enabled: true, Target: promptTargetUser, Action: promptActionInject,
		Content: "[X]", Marker: "HERE", Position: promptPositionPrepend,
		Models: []promptModelRule{{Name: "route-*", Protocol: "openai"}},
	}}}
	body := []byte(`{"messages":[{"role":"user","content":"before HERE after"}]}`)
	out := applyPromptRules(rules, "openai", "route-a(high)", body, "")
	if got := gjson.GetBytes(out, "messages.0.content").String(); got != "before [X]HERE after" {
		t.Fatalf("content = %q", got)
	}
	if again := applyPromptRules(rules, "openai", "route-a(high)", out, ""); !bytes.Equal(out, again) {
		t.Fatalf("marker injection was not idempotent: %s", again)
	}
	if mismatch := applyPromptRules(rules, "claude", "route-a", body, ""); !bytes.Equal(mismatch, body) {
		t.Fatalf("protocol mismatch changed body: %s", mismatch)
	}
}

func TestPromptRulesSkippedRequests(t *testing.T) {
	rules := []compiledPromptRule{{promptRule: promptRule{Name: "inject", Enabled: true, Target: promptTargetSystem, Action: promptActionInject, Content: "x", Position: promptPositionAppend}}}
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	for _, path := range []string{"/v1/images/generations", "/proxy/v1/images/edits", "/v1/responses/compact"} {
		if out := applyPromptRules(rules, "openai", "m", body, path); !bytes.Equal(out, body) {
			t.Fatalf("path %q changed body: %s", path, out)
		}
	}
	if out := applyPromptRules(rules, "unknown", "m", body, ""); !bytes.Equal(out, body) {
		t.Fatalf("unknown protocol changed body: %s", out)
	}
	malformed := []byte(`{"messages":[`)
	if out := applyPromptRules(rules, "openai", "m", malformed, ""); !bytes.Equal(out, malformed) {
		t.Fatalf("malformed body changed: %s", out)
	}
}

func TestInterceptorSkipsNestedCallbacksAndMatchesRequestedAlias(t *testing.T) {
	cfg, err := decodePromptConfig([]byte("rules:\n  - name: alias\n    enabled: true\n    models: [{name: route, protocol: openai}]\n    target: system\n    action: inject\n    content: policy\n"))
	if err != nil {
		t.Fatal(err)
	}
	plugin := &promptRulesPlugin{config: cfg}
	req := pluginapi.RequestInterceptRequest{
		SourceFormat: "openai", Model: "physical", RequestedModel: "route",
		Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}
	response, err := plugin.InterceptRequestBeforeAuth(context.Background(), req)
	if err != nil || !bytes.Contains(response.Body, []byte("policy")) {
		t.Fatalf("outer response = %s, error=%v", response.Body, err)
	}
	req.Metadata = map[string]any{"source": nestedModelCallbackSource}
	response, err = plugin.InterceptRequestBeforeAuth(context.Background(), req)
	if err != nil || len(response.Body) != 0 {
		t.Fatalf("nested response = %s, error=%v", response.Body, err)
	}
}

func TestPromptABIRegistrationAndInterception(t *testing.T) {
	configYAML := []byte("rules:\n  - name: abi\n    enabled: true\n    target: system\n    action: inject\n    content: abi-policy\n")
	lifecycle, _ := json.Marshal(lifecycleRequest{ConfigYAML: configYAML})
	raw, err := handlePromptABIMethod(context.Background(), pluginabi.MethodPluginRegister, lifecycle)
	if err != nil {
		t.Fatalf("register error = %v", err)
	}
	var envelope abiEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("registration envelope = %s, error=%v", raw, err)
	}
	var registration abiRegistration
	if err := json.Unmarshal(envelope.Result, &registration); err != nil {
		t.Fatal(err)
	}
	if registration.SchemaVersion != registrationSchemaVersion || registration.Metadata.Name != pluginName || !registration.Capabilities.RequestInterceptor {
		t.Fatalf("registration = %#v", registration)
	}
	rpcRaw, _ := json.Marshal(requestInterceptRPC{RequestInterceptRequest: pluginapi.RequestInterceptRequest{
		SourceFormat: "openai", RequestedModel: "m",
		Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
	}})
	raw, err = handlePromptABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, rpcRaw)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || !envelope.OK {
		t.Fatalf("intercept envelope = %s, error=%v", raw, err)
	}
	var response pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(response.Body, []byte("abi-policy")) {
		t.Fatalf("response body = %s", response.Body)
	}
}
