package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type promptHandler interface {
	InjectSystem([]byte, string, string, string) []byte
	StripSystem([]byte, *regexp.Regexp) []byte
	InjectLastUser([]byte, string, string, string) []byte
	StripLastUser([]byte, *regexp.Regexp) []byte
}

func applyPromptRules(rules []compiledPromptRule, sourceFormat, model string, body []byte, requestPath string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) || skipPromptRulesPath(requestPath) {
		return body
	}
	handler := handlerForPromptFormat(sourceFormat)
	if handler == nil {
		return body
	}
	models := promptModelCandidates(model)
	out := body
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled || rule.Action != promptActionStrip || !matchesPromptRule(rule.promptRule, sourceFormat, models) {
			continue
		}
		if rule.Target == promptTargetSystem {
			out = handler.StripSystem(out, rule.pattern)
		} else {
			out = handler.StripLastUser(out, rule.pattern)
		}
	}
	for i := range rules {
		rule := &rules[i]
		if !rule.Enabled || rule.Action != promptActionInject || !matchesPromptRule(rule.promptRule, sourceFormat, models) {
			continue
		}
		if rule.Target == promptTargetSystem {
			out = handler.InjectSystem(out, rule.Content, rule.Marker, rule.Position)
		} else {
			out = handler.InjectLastUser(out, rule.Content, rule.Marker, rule.Position)
		}
	}
	return out
}

func handlerForPromptFormat(sourceFormat string) promptHandler {
	switch strings.ToLower(strings.TrimSpace(sourceFormat)) {
	case "openai":
		return openAIPromptHandler{}
	case "openai-response":
		return openAIResponsePromptHandler{}
	case "claude":
		return claudePromptHandler{}
	case "gemini":
		return geminiPromptHandler{}
	case "interactions":
		return interactionsPromptHandler{}
	default:
		return nil
	}
}

func matchesPromptRule(rule promptRule, sourceFormat string, models []string) bool {
	if len(rule.Models) == 0 {
		return true
	}
	for _, model := range models {
		for _, candidate := range rule.Models {
			if candidate.Name == "" {
				continue
			}
			if candidate.Protocol != "" && sourceFormat != "" && !strings.EqualFold(candidate.Protocol, sourceFormat) {
				continue
			}
			if wildcardMatch(candidate.Name, model) {
				return true
			}
		}
	}
	return false
}

func promptModelCandidates(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	out := []string{model}
	if base, _, hasSuffix := splitThinkingSuffix(model); hasSuffix && !strings.EqualFold(base, model) {
		out = append(out, base)
	}
	return out
}

func splitThinkingSuffix(model string) (string, string, bool) {
	model = strings.TrimSpace(model)
	open := strings.LastIndex(model, "(")
	if open <= 0 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	base := strings.TrimSpace(model[:open])
	suffix := strings.TrimSpace(model[open+1 : len(model)-1])
	if base == "" || suffix == "" {
		return model, "", false
	}
	return base, suffix, true
}

func wildcardMatch(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	value = strings.TrimSpace(value)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	patternIndex, valueIndex, starIndex, matchIndex := 0, 0, -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			matchIndex = valueIndex
			patternIndex++
			continue
		}
		if starIndex < 0 {
			return false
		}
		patternIndex = starIndex + 1
		matchIndex++
		valueIndex = matchIndex
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

func skipPromptRulesPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasSuffix(path, "/v1/images/generations") ||
		strings.HasSuffix(path, "/v1/images/edits") ||
		strings.HasSuffix(path, "/images/generations") ||
		strings.HasSuffix(path, "/images/edits") ||
		strings.HasSuffix(path, "/v1/responses/compact") ||
		strings.HasSuffix(path, "/responses/compact")
}

func injectText(text, content, marker, position string) (string, bool) {
	if content == "" {
		return text, false
	}
	if marker == "" {
		if strings.Contains(text, content) {
			return text, false
		}
		if position == promptPositionPrepend {
			return content + text, true
		}
		return text + content, true
	}
	first := strings.Index(text, marker)
	if first < 0 || hasAdjacentContent(text, content, marker, position) {
		return text, false
	}
	if position == promptPositionPrepend {
		return text[:first] + content + text[first:], true
	}
	end := first + len(marker)
	return text[:end] + content + text[end:], true
}

func hasAdjacentContent(text, content, marker, position string) bool {
	if marker == "" || content == "" {
		return false
	}
	for start := 0; start < len(text); {
		offset := strings.Index(text[start:], marker)
		if offset < 0 {
			return false
		}
		index := start + offset
		if position == promptPositionPrepend {
			if index >= len(content) && text[index-len(content):index] == content {
				return true
			}
		} else {
			after := index + len(marker)
			if after+len(content) <= len(text) && text[after:after+len(content)] == content {
				return true
			}
		}
		start = index + 1
	}
	return false
}

func injectBlockArray(payload []byte, path string, isText func(gjson.Result) bool, newBlock func(string) ([]byte, error), content, marker, position string) []byte {
	array := gjson.GetBytes(payload, path)
	if !array.IsArray() {
		return payload
	}
	blocks := array.Array()
	if marker == "" {
		for _, block := range blocks {
			if isText(block) && strings.Contains(blockText(block), content) {
				return payload
			}
		}
		raw, err := newBlock(content)
		if err != nil {
			return payload
		}
		if position == promptPositionAppend {
			updated, err := sjson.SetRawBytes(payload, path+".-1", raw)
			if err == nil {
				return updated
			}
			return payload
		}
		return prependArrayElement(payload, path, raw)
	}
	first := -1
	for i, block := range blocks {
		if !isText(block) {
			continue
		}
		text := blockText(block)
		if !strings.Contains(text, marker) {
			continue
		}
		if first < 0 {
			first = i
		}
		if hasAdjacentContent(text, content, marker, position) {
			return payload
		}
	}
	if first < 0 {
		return payload
	}
	text, changed := injectText(blockText(blocks[first]), content, marker, position)
	if !changed {
		return payload
	}
	target := fmt.Sprintf("%s.%d.text", path, first)
	if blocks[first].Type == gjson.String {
		target = fmt.Sprintf("%s.%d", path, first)
	}
	updated, err := sjson.SetBytes(payload, target, text)
	if err != nil {
		return payload
	}
	return updated
}

func prependArrayElement(payload []byte, path string, raw []byte) []byte {
	array := gjson.GetBytes(payload, path)
	if !array.IsArray() {
		return payload
	}
	var buffer bytes.Buffer
	buffer.WriteByte('[')
	buffer.Write(raw)
	for _, item := range array.Array() {
		buffer.WriteByte(',')
		buffer.WriteString(item.Raw)
	}
	buffer.WriteByte(']')
	updated, err := sjson.SetRawBytes(payload, path, buffer.Bytes())
	if err != nil {
		return payload
	}
	return updated
}

func blockText(block gjson.Result) string {
	if block.Type == gjson.String {
		return block.String()
	}
	return block.Get("text").String()
}

func hasNonEmptyText(value gjson.Result, field string) bool {
	text := value.Get(field)
	return text.Exists() && text.Type == gjson.String && strings.TrimSpace(text.String()) != ""
}

func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}
