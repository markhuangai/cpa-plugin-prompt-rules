package main

import (
	"bytes"
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const nestedModelCallbackSource = "plugin_host_model_callback"

func (p *promptRulesPlugin) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if p == nil || !p.config.Enabled || metadataString(req.Metadata, "source") == nestedModelCallbackSource {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	model := strings.TrimSpace(req.RequestedModel)
	if model == "" {
		model = strings.TrimSpace(req.Model)
	}
	updated := applyPromptRules(p.config.Rules, req.SourceFormat, model, req.Body, metadataString(req.Metadata, "request_path"))
	if bytes.Equal(updated, req.Body) {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	return pluginapi.RequestInterceptResponse{Body: updated}, nil
}

func (*promptRulesPlugin) InterceptRequestAfterAuth(_ context.Context, _ pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return pluginapi.RequestInterceptResponse{}, nil
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}
