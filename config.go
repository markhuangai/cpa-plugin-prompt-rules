package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	promptTargetSystem = "system"
	promptTargetUser   = "user"

	promptActionInject = "inject"
	promptActionStrip  = "strip"

	promptPositionAppend  = "append"
	promptPositionPrepend = "prepend"

	promptPatternMaxLen = 1024
)

var allowedPromptProtocols = map[string]struct{}{
	"openai":          {},
	"openai-response": {},
	"claude":          {},
	"gemini":          {},
	"interactions":    {},
}

type promptModelRule struct {
	Name     string `yaml:"name" json:"name"`
	Protocol string `yaml:"protocol" json:"protocol"`
}

type promptRule struct {
	Name     string            `yaml:"name" json:"name"`
	Enabled  bool              `yaml:"enabled" json:"enabled"`
	Models   []promptModelRule `yaml:"models,omitempty" json:"models,omitempty"`
	Target   string            `yaml:"target" json:"target"`
	Action   string            `yaml:"action" json:"action"`
	Content  string            `yaml:"content,omitempty" json:"content,omitempty"`
	Marker   string            `yaml:"marker,omitempty" json:"marker,omitempty"`
	Position string            `yaml:"position,omitempty" json:"position,omitempty"`
	Pattern  string            `yaml:"pattern,omitempty" json:"pattern,omitempty"`
}

type compiledPromptRule struct {
	promptRule
	pattern *regexp.Regexp
}

type promptConfig struct {
	Enabled bool
	Rules   []compiledPromptRule
}

type promptConfigYAML struct {
	Enabled     *bool         `yaml:"enabled,omitempty"`
	Priority    int           `yaml:"priority,omitempty"`
	Store       yaml.Node     `yaml:"store,omitempty"`
	Rules       *[]promptRule `yaml:"rules,omitempty"`
	LegacyRules *[]promptRule `yaml:"prompt-rules,omitempty"`
}

func decodePromptConfig(raw []byte) (promptConfig, error) {
	wire := promptConfigYAML{}
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&wire); err != nil {
			return promptConfig{}, fmt.Errorf("decode %s config: %w", pluginID, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				return promptConfig{}, fmt.Errorf("decode %s config: multiple YAML documents are not supported", pluginID)
			}
			return promptConfig{}, fmt.Errorf("decode %s config: %w", pluginID, err)
		}
	}
	if wire.Rules != nil && wire.LegacyRules != nil {
		return promptConfig{}, errors.New("prompt-rules config must not contain both rules and prompt-rules")
	}
	enabled := true
	if wire.Enabled != nil {
		enabled = *wire.Enabled
	}
	var rules []promptRule
	if wire.Rules != nil {
		rules = *wire.Rules
	} else if wire.LegacyRules != nil {
		rules = *wire.LegacyRules
	}
	compiled := make([]compiledPromptRule, 0, len(rules))
	seen := make(map[string]int, len(rules))
	for i, rawRule := range rules {
		rule := normalizePromptRule(rawRule)
		if err := validatePromptRule(rule); err != nil {
			return promptConfig{}, fmt.Errorf("rules[%d] %q: %w", i, rule.Name, err)
		}
		if previous, duplicate := seen[rule.Name]; duplicate {
			return promptConfig{}, fmt.Errorf("rules[%d] %q: duplicate name (also at index %d)", i, rule.Name, previous)
		}
		seen[rule.Name] = i
		compiledRule := compiledPromptRule{promptRule: rule}
		if rule.Action == promptActionStrip {
			compiledRule.pattern = regexp.MustCompile(rule.Pattern)
		}
		compiled = append(compiled, compiledRule)
	}
	if !enabled {
		compiled = nil
	}
	return promptConfig{Enabled: enabled, Rules: compiled}, nil
}

func normalizePromptRule(rule promptRule) promptRule {
	rule.Name = strings.TrimSpace(rule.Name)
	rule.Target = strings.ToLower(strings.TrimSpace(rule.Target))
	rule.Action = strings.ToLower(strings.TrimSpace(rule.Action))
	rule.Marker = strings.TrimSpace(rule.Marker)
	if rule.Action == promptActionInject {
		rule.Position = strings.ToLower(strings.TrimSpace(rule.Position))
		if rule.Position == "" {
			rule.Position = promptPositionAppend
		}
	} else {
		rule.Position = ""
	}
	for i := range rule.Models {
		rule.Models[i].Name = strings.TrimSpace(rule.Models[i].Name)
		protocol := strings.ToLower(strings.TrimSpace(rule.Models[i].Protocol))
		if protocol == "gemini-cli" {
			protocol = "interactions"
		}
		rule.Models[i].Protocol = protocol
	}
	return rule
}

func validatePromptRule(rule promptRule) error {
	if rule.Name == "" {
		return errors.New("name is required")
	}
	for i, model := range rule.Models {
		if model.Protocol == "" {
			continue
		}
		if _, ok := allowedPromptProtocols[model.Protocol]; !ok {
			return fmt.Errorf("models[%d].protocol %q is not recognized", i, model.Protocol)
		}
	}
	if rule.Target != promptTargetSystem && rule.Target != promptTargetUser {
		return fmt.Errorf("target must be %q or %q", promptTargetSystem, promptTargetUser)
	}
	switch rule.Action {
	case promptActionInject:
		if rule.Content == "" {
			return errors.New("content is required for inject")
		}
		if rule.Position != promptPositionAppend && rule.Position != promptPositionPrepend {
			return fmt.Errorf("position must be %q or %q", promptPositionAppend, promptPositionPrepend)
		}
		if rule.Pattern != "" {
			return errors.New("pattern must be empty for inject")
		}
	case promptActionStrip:
		if rule.Pattern == "" {
			return errors.New("pattern is required for strip")
		}
		if len(rule.Pattern) > promptPatternMaxLen {
			return fmt.Errorf("pattern length %d exceeds max %d", len(rule.Pattern), promptPatternMaxLen)
		}
		if _, err := regexp.Compile(rule.Pattern); err != nil {
			return fmt.Errorf("invalid regex: %w", err)
		}
		if rule.Content != "" || rule.Marker != "" {
			return errors.New("content and marker must be empty for strip")
		}
	default:
		return fmt.Errorf("action must be %q or %q", promptActionInject, promptActionStrip)
	}
	return nil
}
