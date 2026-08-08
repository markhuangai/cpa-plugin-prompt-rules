package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudePromptHandler struct{}

func (claudePromptHandler) InjectSystem(payload []byte, content, marker, position string) []byte {
	system := gjson.GetBytes(payload, "system")
	if !system.Exists() {
		if content == "" || marker != "" {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "system", content)
		if err == nil {
			return updated
		}
		return payload
	}
	if system.Type == gjson.String {
		text, changed := injectText(system.String(), content, marker, position)
		if !changed {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "system", text)
		if err == nil {
			return updated
		}
		return payload
	}
	if system.IsArray() {
		return injectBlockArray(payload, "system", isClaudeTextBlock, newClaudeTextBlock, content, marker, position)
	}
	return payload
}

func (claudePromptHandler) StripSystem(payload []byte, pattern *regexp.Regexp) []byte {
	system := gjson.GetBytes(payload, "system")
	if system.Type == gjson.String {
		return replaceStringAtPath(payload, "system", system.String(), pattern)
	}
	if !system.IsArray() {
		return payload
	}
	out := payload
	for index, block := range system.Array() {
		if !isClaudeTextBlock(block) {
			continue
		}
		text := block.Get("text")
		if text.Exists() {
			out = replaceStringAtPath(out, fmt.Sprintf("system.%d.text", index), text.String(), pattern)
		}
	}
	return out
}

func (claudePromptHandler) InjectLastUser(payload []byte, content, marker, position string) []byte {
	index := lastClaudeUserIndex(gjson.GetBytes(payload, "messages"))
	if index < 0 {
		return payload
	}
	return mutateClaudeMessage(payload, index, content, marker, position)
}

func (claudePromptHandler) StripLastUser(payload []byte, pattern *regexp.Regexp) []byte {
	index := lastClaudeUserIndex(gjson.GetBytes(payload, "messages"))
	if index < 0 {
		return payload
	}
	return stripClaudeMessage(payload, index, pattern)
}

func lastClaudeUserIndex(messages gjson.Result) int {
	if !messages.IsArray() {
		return -1
	}
	items := messages.Array()
	for index := len(items) - 1; index >= 0; index-- {
		message := items[index]
		if message.Get("role").String() != "user" {
			continue
		}
		content := message.Get("content")
		if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
			return index
		}
		if content.IsArray() {
			for _, block := range content.Array() {
				if isClaudeTextBlock(block) && hasNonEmptyText(block, "text") {
					return index
				}
			}
		}
	}
	return -1
}

func mutateClaudeMessage(payload []byte, index int, content, marker, position string) []byte {
	path := fmt.Sprintf("messages.%d.content", index)
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
		return injectBlockArray(payload, path, isClaudeTextBlock, newClaudeTextBlock, content, marker, position)
	}
	return payload
}

func stripClaudeMessage(payload []byte, index int, pattern *regexp.Regexp) []byte {
	path := fmt.Sprintf("messages.%d.content", index)
	current := gjson.GetBytes(payload, path)
	if current.Type == gjson.String {
		return replaceStringAtPath(payload, path, current.String(), pattern)
	}
	if !current.IsArray() {
		return payload
	}
	out := payload
	for blockIndex, block := range current.Array() {
		if !isClaudeTextBlock(block) {
			continue
		}
		text := block.Get("text")
		if text.Exists() {
			out = replaceStringAtPath(out, fmt.Sprintf("%s.%d.text", path, blockIndex), text.String(), pattern)
		}
	}
	return out
}

func isClaudeTextBlock(block gjson.Result) bool {
	return block.Get("type").String() == "text"
}

func newClaudeTextBlock(content string) ([]byte, error) {
	return marshalJSON(map[string]any{"type": "text", "text": content})
}
