package main

import (
	"strings"
	"testing"
)

func TestDecodePromptConfigCanonicalAndLegacy(t *testing.T) {
	for _, test := range []struct {
		name string
		key  string
	}{
		{name: "canonical", key: "rules"},
		{name: "legacy", key: "prompt-rules"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := "enabled: true\npriority: 100\nstore:\n  version: 0.1.0\n" + test.key + ":\n" +
				"  - name: inject\n    enabled: true\n    models:\n      - name: auto(*)\n        protocol: gemini-cli\n    target: SYSTEM\n    action: INJECT\n    content: policy\n"
			cfg, err := decodePromptConfig([]byte(raw))
			if err != nil {
				t.Fatalf("decodePromptConfig() error = %v", err)
			}
			if len(cfg.Rules) != 1 {
				t.Fatalf("rules = %d, want 1", len(cfg.Rules))
			}
			rule := cfg.Rules[0]
			if rule.Target != promptTargetSystem || rule.Action != promptActionInject || rule.Position != promptPositionAppend {
				t.Fatalf("normalized rule = %#v", rule.promptRule)
			}
			if got := rule.Models[0].Protocol; got != "interactions" {
				t.Fatalf("protocol = %q, want interactions", got)
			}
		})
	}
}

func TestDecodePromptConfigRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		contains string
	}{
		{name: "both keys", raw: "rules: []\nprompt-rules: []\n", contains: "both rules and prompt-rules"},
		{name: "unknown field", raw: "rulse: []\n", contains: "field rulse not found"},
		{name: "duplicate", raw: "rules:\n  - name: same\n    enabled: true\n    target: system\n    action: inject\n    content: x\n  - name: same\n    enabled: true\n    target: user\n    action: inject\n    content: y\n", contains: "duplicate name"},
		{name: "unknown protocol", raw: "rules:\n  - name: bad\n    enabled: true\n    models: [{name: '*', protocol: bogus}]\n    target: system\n    action: inject\n    content: x\n", contains: "not recognized"},
		{name: "invalid regex", raw: "rules:\n  - name: bad\n    enabled: true\n    target: user\n    action: strip\n    pattern: '['\n", contains: "invalid regex"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodePromptConfig([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("error = %v, want containing %q", err, test.contains)
			}
		})
	}
}

func validPromptRuleYAML(name string) string {
	return "rules:\n  - name: " + name + "\n    enabled: true\n    target: system\n    action: inject\n    content: x\n"
}

func TestDecodePromptConfigDisabledClearsRules(t *testing.T) {
	cfg, err := decodePromptConfig([]byte("enabled: false\n" + validPromptRuleYAML("disabled")))
	if err != nil {
		t.Fatalf("decodePromptConfig() error = %v", err)
	}
	if cfg.Enabled || len(cfg.Rules) != 0 {
		t.Fatalf("disabled config = %#v", cfg)
	}
}
