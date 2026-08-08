package main

import (
	"fmt"
	"regexp"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type geminiPromptHandler struct{}

func (geminiPromptHandler) InjectSystem(payload []byte, content, marker, position string) []byte {
	field := activeGeminiSystemField(payload)
	if field == "" {
		if content == "" || marker != "" {
			return payload
		}
		return setGeminiSystem(payload, "systemInstruction", content)
	}
	parts := gjson.GetBytes(payload, field+".parts")
	if !parts.IsArray() {
		if marker != "" {
			return payload
		}
		return setGeminiSystem(payload, field, content)
	}
	return injectBlockArray(payload, field+".parts", isGeminiTextPart, newGeminiTextPart, content, marker, position)
}

func (geminiPromptHandler) StripSystem(payload []byte, pattern *regexp.Regexp) []byte {
	field := activeGeminiSystemField(payload)
	parts := gjson.GetBytes(payload, field+".parts")
	if field == "" || !parts.IsArray() {
		return payload
	}
	out := payload
	for index, part := range parts.Array() {
		text := part.Get("text")
		if text.Exists() {
			out = replaceStringAtPath(out, fmt.Sprintf("%s.parts.%d.text", field, index), text.String(), pattern)
		}
	}
	return out
}

func (geminiPromptHandler) InjectLastUser(payload []byte, content, marker, position string) []byte {
	index := lastGeminiUserIndex(gjson.GetBytes(payload, "contents"))
	if index < 0 {
		return payload
	}
	path := fmt.Sprintf("contents.%d.parts", index)
	return injectBlockArray(payload, path, isGeminiTextPart, newGeminiTextPart, content, marker, position)
}

func (geminiPromptHandler) StripLastUser(payload []byte, pattern *regexp.Regexp) []byte {
	index := lastGeminiUserIndex(gjson.GetBytes(payload, "contents"))
	if index < 0 {
		return payload
	}
	path := fmt.Sprintf("contents.%d.parts", index)
	parts := gjson.GetBytes(payload, path)
	out := payload
	for partIndex, part := range parts.Array() {
		text := part.Get("text")
		if text.Exists() {
			out = replaceStringAtPath(out, fmt.Sprintf("%s.%d.text", path, partIndex), text.String(), pattern)
		}
	}
	return out
}

func activeGeminiSystemField(payload []byte) string {
	if gjson.GetBytes(payload, "systemInstruction").Exists() {
		return "systemInstruction"
	}
	if gjson.GetBytes(payload, "system_instruction").Exists() {
		return "system_instruction"
	}
	return ""
}

func setGeminiSystem(payload []byte, path, content string) []byte {
	raw, err := marshalJSON(map[string]any{
		"role":  "system",
		"parts": []map[string]any{{"text": content}},
	})
	if err != nil {
		return payload
	}
	updated, err := sjson.SetRawBytes(payload, path, raw)
	if err != nil {
		return payload
	}
	return updated
}

func lastGeminiUserIndex(contents gjson.Result) int {
	if !contents.IsArray() {
		return -1
	}
	items := contents.Array()
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if item.Get("role").String() != "user" {
			continue
		}
		for _, part := range item.Get("parts").Array() {
			if hasNonEmptyText(part, "text") {
				return index
			}
		}
	}
	return -1
}

func isGeminiTextPart(part gjson.Result) bool {
	return part.Get("text").Exists()
}

func newGeminiTextPart(content string) ([]byte, error) {
	return marshalJSON(map[string]any{"text": content})
}
