package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPromptABIAdvertisesConfigurationResource(t *testing.T) {
	resetPromptABIState(t)
	lifecycle, err := json.Marshal(lifecycleRequest{ConfigYAML: []byte("rules: []\n")})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handlePromptABIMethod(t.Context(), pluginabi.MethodPluginRegister, lifecycle)
	if err != nil {
		t.Fatalf("plugin.register error = %v", err)
	}
	var registrationEnvelope abiEnvelope
	if err := json.Unmarshal(raw, &registrationEnvelope); err != nil {
		t.Fatalf("decode registration envelope: %v", err)
	}
	var registration abiRegistration
	if err := json.Unmarshal(registrationEnvelope.Result, &registration); err != nil {
		t.Fatalf("decode registration result: %v", err)
	}
	if !registrationEnvelope.OK || !registration.Capabilities.RequestInterceptor || !registration.Capabilities.ManagementAPI {
		t.Fatalf("registration = %#v", registration)
	}
	if registration.Metadata.Version != "0.2.0" {
		t.Fatalf("metadata version = %q, want 0.2.0", registration.Metadata.Version)
	}

	raw, err = handlePromptABIMethod(t.Context(), pluginabi.MethodManagementRegister, nil)
	if err != nil {
		t.Fatalf("management.register error = %v", err)
	}
	var managementEnvelope abiEnvelope
	if err := json.Unmarshal(raw, &managementEnvelope); err != nil {
		t.Fatalf("decode management envelope: %v", err)
	}
	var management managementRegistrationResponse
	if err := json.Unmarshal(managementEnvelope.Result, &management); err != nil {
		t.Fatalf("decode management result: %v", err)
	}
	if len(management.Resources) != 1 || management.Resources[0].Path != "/config" || management.Resources[0].Menu != "Prompt Rules" {
		t.Fatalf("resources = %#v", management.Resources)
	}
	if len(management.Routes) != 1 || management.Routes[0].Method != http.MethodPost || management.Routes[0].Path != "/plugins/prompt-rules/validate" {
		t.Fatalf("routes = %#v", management.Routes)
	}
}

func TestPromptManagementDashboardUsesAuthenticatedConfigAPI(t *testing.T) {
	request, err := json.Marshal(managementRPCRequest{ManagementRequest: pluginapi.ManagementRequest{
		Method: http.MethodGet,
		Path:   promptDashboardPath,
	}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := handlePromptABIMethod(t.Context(), pluginabi.MethodManagementHandle, request)
	if err != nil {
		t.Fatalf("management.handle error = %v", err)
	}
	var envelope abiEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var response pluginapi.ManagementResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatalf("decode management response: %v", err)
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Headers.Get("Content-Type"), "text/html") {
		t.Fatalf("response status=%d headers=%v", response.StatusCode, response.Headers)
	}
	page := string(response.Body)
	for _, required := range []string{
		"<title>Prompt Rules</title>",
		"/v0/management/plugins/prompt-rules/config",
		"/v0/management/plugins/prompt-rules/validate",
		"Authorization:'Bearer '+key",
		"data-action=\"add-model\"",
	} {
		if !strings.Contains(page, required) {
			t.Fatalf("dashboard missing %q", required)
		}
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "http://", "https://", "innerHTML"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("dashboard contains forbidden text %q", forbidden)
		}
	}
	if csp := response.Headers.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "connect-src 'self'") {
		t.Fatalf("Content-Security-Policy = %q", csp)
	}
}

func TestPromptManagementValidationUsesPluginParser(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantText   string
	}{
		{
			name:       "valid",
			body:       `{"enabled":true,"rules":[{"name":"policy","enabled":true,"models":[{"name":"gpt-*","protocol":"openai"}],"target":"system","action":"inject","position":"append","content":"Be concise."}]}`,
			wantStatus: http.StatusOK,
			wantText:   `"valid":true`,
		},
		{
			name:       "invalid rule",
			body:       `{"enabled":true,"rules":[{"name":"policy","enabled":true,"target":"system","action":"inject","content":""}]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "content is required for inject",
		},
		{
			name:       "unknown request field",
			body:       `{"enabled":true,"rules":[],"unknown":true}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "unknown field",
		},
		{
			name:       "multiple documents",
			body:       `{"enabled":true,"rules":[]} {"enabled":true,"rules":[]}`,
			wantStatus: http.StatusBadRequest,
			wantText:   "multiple JSON values",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := handlePromptManagement(pluginapi.ManagementRequest{
				Method: http.MethodPost,
				Path:   promptValidationPath,
				Body:   []byte(test.body),
			})
			if response.StatusCode != test.wantStatus || !strings.Contains(string(response.Body), test.wantText) {
				t.Fatalf("status=%d body=%s, want status=%d containing %q", response.StatusCode, response.Body, test.wantStatus, test.wantText)
			}
		})
	}
}

func TestPromptManagementRejectsUnsupportedMethod(t *testing.T) {
	response := handlePromptManagement(pluginapi.ManagementRequest{Method: http.MethodDelete, Path: promptDashboardPath})
	if response.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(string(response.Body), "method_not_allowed") {
		t.Fatalf("response = %#v, body=%s", response, response.Body)
	}
}

func resetPromptABIState(t *testing.T) {
	t.Helper()
	promptABIState.Lock()
	promptABIState.plugin = nil
	promptABIState.shuttingDown = false
	promptABIState.Unlock()
	t.Cleanup(func() {
		promptABIState.Lock()
		promptABIState.plugin = nil
		promptABIState.shuttingDown = false
		promptABIState.Unlock()
	})
}
