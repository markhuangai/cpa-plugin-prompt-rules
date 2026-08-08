package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIPromptHandler struct{}

func (openAIPromptHandler) InjectSystem(payload []byte, content, marker, position string) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if messages.IsArray() {
		if index := firstOpenAISystemIndex(messages); index >= 0 {
			return mutateOpenAIMessage(payload, index, content, marker, position)
		}
	}
	if content == "" || marker != "" {
		return payload
	}
	raw, err := marshalJSON(map[string]any{"role": "system", "content": content})
	if err != nil {
		return payload
	}
	if !messages.Exists() {
		updated, err := sjson.SetRawBytes(payload, "messages", append(append([]byte{'['}, raw...), ']'))
		if err == nil {
			return updated
		}
		return payload
	}
	return prependArrayElement(payload, "messages", raw)
}

func (openAIPromptHandler) StripSystem(payload []byte, pattern *regexp.Regexp) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	out := payload
	for index, message := range messages.Array() {
		role := message.Get("role").String()
		if role == "system" || role == "developer" {
			out = stripOpenAIMessage(out, index, pattern)
		}
	}
	return out
}

func (openAIPromptHandler) InjectLastUser(payload []byte, content, marker, position string) []byte {
	messages := gjson.GetBytes(payload, "messages")
	index := lastOpenAINaturalUserIndex(messages)
	if index < 0 {
		return payload
	}
	return mutateOpenAIMessage(payload, index, content, marker, position)
}

func (openAIPromptHandler) StripLastUser(payload []byte, pattern *regexp.Regexp) []byte {
	index := lastOpenAINaturalUserIndex(gjson.GetBytes(payload, "messages"))
	if index < 0 {
		return payload
	}
	return stripOpenAIMessage(payload, index, pattern)
}

func firstOpenAISystemIndex(messages gjson.Result) int {
	developer := -1
	for index, message := range messages.Array() {
		switch message.Get("role").String() {
		case "system":
			return index
		case "developer":
			if developer < 0 {
				developer = index
			}
		}
	}
	return developer
}

func lastOpenAINaturalUserIndex(messages gjson.Result) int {
	if !messages.IsArray() {
		return -1
	}
	items := messages.Array()
	for index := len(items) - 1; index >= 0; index-- {
		message := items[index]
		if message.Get("role").String() != "user" || message.Get("tool_call_id").Exists() {
			continue
		}
		content := message.Get("content")
		if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
			return index
		}
		if content.IsArray() {
			for _, block := range content.Array() {
				kind := block.Get("type").String()
				if (kind == "text" || kind == "input_text") && hasNonEmptyText(block, "text") {
					return index
				}
			}
		}
	}
	return -1
}

func mutateOpenAIMessage(payload []byte, index int, content, marker, position string) []byte {
	path := fmt.Sprintf("messages.%d.content", index)
	current := gjson.GetBytes(payload, path)
	if !current.Exists() || current.Type == gjson.Null {
		text, changed := injectText("", content, marker, position)
		if !changed {
			return payload
		}
		updated, err := sjson.SetBytes(payload, path, text)
		if err == nil {
			return updated
		}
		return payload
	}
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
		return injectBlockArray(payload, path, isOpenAITextBlock, newOpenAITextBlock, content, marker, position)
	}
	return payload
}

func stripOpenAIMessage(payload []byte, index int, pattern *regexp.Regexp) []byte {
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
		kind := block.Get("type").String()
		if kind != "text" && kind != "input_text" {
			continue
		}
		text := block.Get("text")
		if text.Exists() {
			out = replaceStringAtPath(out, fmt.Sprintf("%s.%d.text", path, blockIndex), text.String(), pattern)
		}
	}
	return out
}

func isOpenAITextBlock(block gjson.Result) bool {
	kind := block.Get("type").String()
	return kind == "text" || kind == "input_text"
}

func newOpenAITextBlock(content string) ([]byte, error) {
	return marshalJSON(map[string]any{"type": "text", "text": content})
}

func replaceStringAtPath(payload []byte, path, value string, pattern *regexp.Regexp) []byte {
	if pattern == nil {
		return payload
	}
	updatedValue := pattern.ReplaceAllString(value, "")
	if updatedValue == value {
		return payload
	}
	updated, err := sjson.SetBytes(payload, path, updatedValue)
	if err != nil {
		return payload
	}
	return updated
}
