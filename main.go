package main

import "github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"

const (
	pluginID   = "prompt-rules"
	pluginName = "Prompt Rules"
)

var pluginVersion = "0.2.0"

type promptRulesPlugin struct {
	config promptConfig
}

var _ pluginapi.RequestInterceptor = (*promptRulesPlugin)(nil)

func buildPlugin(configYAML []byte) (*promptRulesPlugin, pluginapi.Metadata, error) {
	cfg, err := decodePromptConfig(configYAML)
	if err != nil {
		return nil, pluginapi.Metadata{}, err
	}
	plugin := &promptRulesPlugin{config: cfg}
	metadata := pluginapi.Metadata{
		Name:             pluginName,
		Version:          pluginVersion,
		Author:           "markhuangai",
		GitHubRepository: "https://github.com/markhuangai/cpa-plugin-prompt-rules",
		ConfigFields: []pluginapi.ConfigField{
			{Name: "rules", Type: pluginapi.ConfigFieldTypeArray, Description: "Ordered prompt injection and stripping rules. Strip rules run before inject rules."},
		},
	}
	return plugin, metadata, nil
}

func main() {}
