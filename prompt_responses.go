package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIResponsePromptHandler struct{}

func (openAIResponsePromptHandler) InjectSystem(payload []byte, content, marker, position string) []byte {
	current := gjson.GetBytes(payload, "instructions")
	if current.Exists() && current.Type == gjson.String {
		text, changed := injectText(current.String(), content, marker, position)
		if !changed {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "instructions", text)
		if err == nil {
			return updated
		}
		return payload
	}
	if content == "" || marker != "" {
		return payload
	}
	updated, err := sjson.SetBytes(payload, "instructions", content)
	if err != nil {
		return payload
	}
	return updated
}

func (openAIResponsePromptHandler) StripSystem(payload []byte, pattern *regexp.Regexp) []byte {
	current := gjson.GetBytes(payload, "instructions")
	if current.Type != gjson.String {
		return payload
	}
	return replaceStringAtPath(payload, "instructions", current.String(), pattern)
}

func (openAIResponsePromptHandler) InjectLastUser(payload []byte, content, marker, position string) []byte {
	input := gjson.GetBytes(payload, "input")
	if input.Type == gjson.String {
		text, changed := injectText(input.String(), content, marker, position)
		if !changed {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "input", text)
		if err == nil {
			return updated
		}
		return payload
	}
	index := lastResponsesUserIndex(input)
	if index < 0 {
		return payload
	}
	return mutateResponsesItem(payload, index, content, marker, position)
}

func (openAIResponsePromptHandler) StripLastUser(payload []byte, pattern *regexp.Regexp) []byte {
	input := gjson.GetBytes(payload, "input")
	if input.Type == gjson.String {
		return replaceStringAtPath(payload, "input", input.String(), pattern)
	}
	index := lastResponsesUserIndex(input)
	if index < 0 {
		return payload
	}
	return stripResponsesItem(payload, index, pattern)
}

func lastResponsesUserIndex(input gjson.Result) int {
	if !input.IsArray() {
		return -1
	}
	items := input.Array()
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if item.Get("role").String() != "user" {
			continue
		}
		kind := item.Get("type").String()
		if kind != "" && kind != "message" && kind != "input_text" {
			continue
		}
		content := item.Get("content")
		if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
			return index
		}
		if content.IsArray() {
			for _, block := range content.Array() {
				kind := block.Get("type").String()
				if (kind == "input_text" || kind == "text") && hasNonEmptyText(block, "text") {
					return index
				}
			}
		}
	}
	return -1
}

func mutateResponsesItem(payload []byte, index int, content, marker, position string) []byte {
	path := fmt.Sprintf("input.%d.content", index)
	current := gjson.GetBytes(payload, path)
	if current.Type == gjson.String {
		text, changed := injectText(current.String(), content, marker, position)
		if !changed {
			return payload
		}
		updated, err := sjson.SetBytes(payload, path, text)
		if err == nil {
			return updated
		}
		return payload
	}
	if current.IsArray() {
		return injectBlockArray(payload, path, isResponsesTextBlock, newResponsesTextBlock, content, marker, position)
	}
	return payload
}

func stripResponsesItem(payload []byte, index int, pattern *regexp.Regexp) []byte {
	path := fmt.Sprintf("input.%d.content", index)
	current := gjson.GetBytes(payload, path)
	if current.Type == gjson.String {
		return replaceStringAtPath(payload, path, current.String(), pattern)
	}
	if !current.IsArray() {
		return payload
	}
	out := payload
	for blockIndex, block := range current.Array() {
		kind := block.Get("type").String()
		if kind != "input_text" && kind != "text" {
			continue
		}
		text := block.Get("text")
		if text.Exists() {
			out = replaceStringAtPath(out, fmt.Sprintf("%s.%d.text", path, blockIndex), text.String(), pattern)
		}
	}
	return out
}

func isResponsesTextBlock(block gjson.Result) bool {
	kind := block.Get("type").String()
	return kind == "input_text" || kind == "text"
}

func newResponsesTextBlock(content string) ([]byte, error) {
	return marshalJSON(map[string]any{"type": "input_text", "text": content})
}
