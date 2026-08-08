package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type interactionsPromptHandler struct{}

type interactionsTarget struct {
	path     string
	array    bool
	isText   func(gjson.Result) bool
	newBlock func(string) ([]byte, error)
}

func (interactionsPromptHandler) InjectSystem(payload []byte, content, marker, position string) []byte {
	target, ok := interactionsSystemTarget(payload)
	if !ok {
		if content == "" || marker != "" {
			return payload
		}
		updated, err := sjson.SetBytes(payload, "system_instruction", content)
		if err == nil {
			return updated
		}
		return payload
	}
	return injectInteractionsTarget(payload, target, content, marker, position)
}

func (interactionsPromptHandler) StripSystem(payload []byte, pattern *regexp.Regexp) []byte {
	target, ok := interactionsSystemTarget(payload)
	if !ok {
		return payload
	}
	return stripInteractionsTarget(payload, target, pattern)
}

func (interactionsPromptHandler) InjectLastUser(payload []byte, content, marker, position string) []byte {
	target, ok := interactionsLastUserTarget(payload)
	if !ok {
		return payload
	}
	return injectInteractionsTarget(payload, target, content, marker, position)
}

func (interactionsPromptHandler) StripLastUser(payload []byte, pattern *regexp.Regexp) []byte {
	target, ok := interactionsLastUserTarget(payload)
	if !ok {
		return payload
	}
	return stripInteractionsTarget(payload, target, pattern)
}

func interactionsSystemTarget(payload []byte) (interactionsTarget, bool) {
	system := gjson.GetBytes(payload, "system_instruction")
	if !system.Exists() {
		return interactionsTarget{}, false
	}
	if system.Type == gjson.String {
		return interactionsTarget{path: "system_instruction"}, true
	}
	if text := system.Get("text"); text.Type == gjson.String {
		return interactionsTarget{path: "system_instruction.text"}, true
	}
	parts := system.Get("parts")
	if parts.IsArray() && interactionsArrayHasText(parts, isInteractionsNativeText) {
		return interactionsTarget{path: "system_instruction.parts", array: true, isText: isInteractionsNativeText, newBlock: newInteractionsNativeText}, true
	}
	return interactionsTarget{}, false
}

func interactionsLastUserTarget(payload []byte) (interactionsTarget, bool) {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() {
		return interactionsTarget{}, false
	}
	if input.Type == gjson.String {
		return interactionsTarget{path: "input"}, strings.TrimSpace(input.String()) != ""
	}
	var targets []interactionsTarget
	collectInteractionTargets(input, "input", "user", &targets)
	if len(targets) == 0 {
		return interactionsTarget{}, false
	}
	return targets[len(targets)-1], true
}

func collectInteractionTargets(value gjson.Result, path, defaultRole string, targets *[]interactionsTarget) {
	if value.Type == gjson.String {
		if defaultRole == "user" && strings.TrimSpace(value.String()) != "" {
			*targets = append(*targets, interactionsTarget{path: path})
		}
		return
	}
	if value.IsArray() {
		for index, item := range value.Array() {
			collectInteractionItemTargets(item, fmt.Sprintf("%s.%d", path, index), defaultRole, targets)
		}
		return
	}
	if steps := value.Get("steps"); steps.IsArray() {
		role := interactionRole(value.Get("role").String(), defaultRole)
		for index, step := range steps.Array() {
			collectInteractionItemTargets(step, fmt.Sprintf("%s.steps.%d", path, index), role, targets)
		}
		return
	}
	collectInteractionItemTargets(value, path, defaultRole, targets)
}

func collectInteractionItemTargets(item gjson.Result, path, defaultRole string, targets *[]interactionsTarget) {
	if item.Type == gjson.String {
		collectInteractionTargets(item, path, defaultRole, targets)
		return
	}
	if steps := item.Get("steps"); steps.IsArray() {
		role := interactionRole(item.Get("role").String(), defaultRole)
		for index, step := range steps.Array() {
			collectInteractionItemTargets(step, fmt.Sprintf("%s.steps.%d", path, index), role, targets)
		}
		return
	}
	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "model_output", "thought", "function_call", "function_result":
		return
	case "user_input":
		defaultRole = "user"
	default:
		defaultRole = interactionRole(item.Get("role").String(), defaultRole)
	}
	if defaultRole != "user" {
		return
	}
	if parts := item.Get("parts"); parts.IsArray() {
		if interactionsArrayHasText(parts, isInteractionsNativeText) {
			*targets = append(*targets, interactionsTarget{path: path + ".parts", array: true, isText: isInteractionsNativeText, newBlock: newInteractionsNativeText})
		}
		return
	}
	content := item.Get("content")
	if content.Type == gjson.String && strings.TrimSpace(content.String()) != "" {
		*targets = append(*targets, interactionsTarget{path: path + ".content"})
		return
	}
	if content.IsArray() && interactionsArrayHasText(content, isInteractionsContentText) {
		*targets = append(*targets, interactionsTarget{path: path + ".content", array: true, isText: isInteractionsContentText, newBlock: newInteractionsContentText})
		return
	}
	if content.IsObject() {
		if text := content.Get("text"); text.Type == gjson.String && strings.TrimSpace(text.String()) != "" {
			*targets = append(*targets, interactionsTarget{path: path + ".content.text"})
		}
		return
	}
	if text := item.Get("text"); text.Type == gjson.String && strings.TrimSpace(text.String()) != "" {
		*targets = append(*targets, interactionsTarget{path: path + ".text"})
	}
}

func interactionRole(role, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "model", "assistant":
		return "model"
	case "user":
		return "user"
	default:
		return fallback
	}
}

func interactionsArrayHasText(items gjson.Result, isText func(gjson.Result) bool) bool {
	for _, item := range items.Array() {
		if isText(item) && strings.TrimSpace(blockText(item)) != "" {
			return true
		}
	}
	return false
}

func isInteractionsNativeText(block gjson.Result) bool {
	return block.Get("text").Exists()
}

func newInteractionsNativeText(content string) ([]byte, error) {
	return marshalJSON(map[string]any{"text": content})
}

func isInteractionsContentText(block gjson.Result) bool {
	return block.Type == gjson.String || block.Get("text").Exists()
}

func newInteractionsContentText(content string) ([]byte, error) {
	return marshalJSON(map[string]any{"type": "text", "text": content})
}

func injectInteractionsTarget(payload []byte, target interactionsTarget, content, marker, position string) []byte {
	if target.array {
		return injectBlockArray(payload, target.path, target.isText, target.newBlock, content, marker, position)
	}
	current := gjson.GetBytes(payload, target.path)
	if current.Type != gjson.String {
		return payload
	}
	text, changed := injectText(current.String(), content, marker, position)
	if !changed {
		return payload
	}
	updated, err := sjson.SetBytes(payload, target.path, text)
	if err != nil {
		return payload
	}
	return updated
}

func stripInteractionsTarget(payload []byte, target interactionsTarget, pattern *regexp.Regexp) []byte {
	if !target.array {
		current := gjson.GetBytes(payload, target.path)
		if current.Type != gjson.String {
			return payload
		}
		return replaceStringAtPath(payload, target.path, current.String(), pattern)
	}
	items := gjson.GetBytes(payload, target.path)
	out := payload
	for index, item := range items.Array() {
		if item.Type == gjson.String {
			out = replaceStringAtPath(out, fmt.Sprintf("%s.%d", target.path, index), item.String(), pattern)
			continue
		}
		if !target.isText(item) {
			continue
		}
		out = replaceStringAtPath(out, fmt.Sprintf("%s.%d.text", target.path, index), item.Get("text").String(), pattern)
	}
	return out
}
