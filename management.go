package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	promptDashboardPath       = "/v0/resource/plugins/" + pluginID + "/config"
	promptValidationPath      = "/v0/management/plugins/" + pluginID + "/validate"
	maxManagementRequestBytes = 1 << 20
)

type managementRPCRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementRegistrationResponse struct {
	Routes    []managementRoute `json:"routes,omitempty"`
	Resources []resourceRoute   `json:"resources,omitempty"`
}

type managementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type promptValidationRequest struct {
	Enabled *bool        `json:"enabled"`
	Rules   []promptRule `json:"rules"`
}

func promptManagementRegistration() managementRegistrationResponse {
	return managementRegistrationResponse{
		Routes: []managementRoute{{
			Method:      http.MethodPost,
			Path:        "/plugins/" + pluginID + "/validate",
			Description: "Validate Prompt Rules configuration before saving it.",
		}},
		Resources: []resourceRoute{{
			Path:        "/config",
			Menu:        "Prompt Rules",
			Description: "Configure ordered prompt injection and stripping rules.",
		}},
	}
}

func handlePromptManagement(request pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	path := strings.TrimRight(strings.TrimSpace(request.Path), "/")
	switch path {
	case promptDashboardPath:
		if !strings.EqualFold(request.Method, http.MethodGet) {
			return promptJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "dashboard only supports GET",
			})
		}
		return pluginapi.ManagementResponse{
			StatusCode: http.StatusOK,
			Headers: http.Header{
				"Content-Type":            {"text/html; charset=utf-8"},
				"Cache-Control":           {"no-store"},
				"Content-Security-Policy": {"default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'"},
				"Referrer-Policy":         {"no-referrer"},
				"X-Content-Type-Options":  {"nosniff"},
			},
			Body: append([]byte(nil), configDashboardHTML...),
		}
	case promptValidationPath:
		if !strings.EqualFold(request.Method, http.MethodPost) {
			return promptJSONResponse(http.StatusMethodNotAllowed, map[string]any{
				"error":   "method_not_allowed",
				"message": "validation only supports POST",
			})
		}
		return validatePromptManagementConfig(request.Body)
	default:
		return promptJSONResponse(http.StatusNotFound, map[string]any{
			"error":   "not_found",
			"message": "management resource not found",
		})
	}
}

func validatePromptManagementConfig(body []byte) pluginapi.ManagementResponse {
	if len(body) > maxManagementRequestBytes {
		return promptJSONResponse(http.StatusRequestEntityTooLarge, map[string]any{
			"valid":   false,
			"error":   "request_too_large",
			"message": "configuration exceeds the 1 MiB validation limit",
		})
	}
	var request promptValidationRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return promptJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_request",
			"message": err.Error(),
		})
	}
	if err := ensurePromptJSONEOF(decoder); err != nil {
		return promptJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_request",
			"message": err.Error(),
		})
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	wire := struct {
		Enabled bool         `yaml:"enabled"`
		Rules   []promptRule `yaml:"rules"`
	}{Enabled: enabled, Rules: request.Rules}
	raw, err := yaml.Marshal(wire)
	if err != nil {
		return promptJSONResponse(http.StatusInternalServerError, map[string]any{
			"valid":   false,
			"error":   "validation_failed",
			"message": err.Error(),
		})
	}
	if _, err := decodePromptConfig(raw); err != nil {
		return promptJSONResponse(http.StatusBadRequest, map[string]any{
			"valid":   false,
			"error":   "invalid_config",
			"message": err.Error(),
		})
	}
	return promptJSONResponse(http.StatusOK, map[string]any{
		"valid":      true,
		"rule_count": len(request.Rules),
	})
}

func ensurePromptJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not supported")
		}
		return err
	}
	return nil
}

func promptJSONResponse(status int, value any) pluginapi.ManagementResponse {
	body, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"response_encode_failed"}`)
	}
	return pluginapi.ManagementResponse{
		StatusCode: status,
		Headers: http.Header{
			"Content-Type":           {"application/json; charset=utf-8"},
			"Cache-Control":          {"no-store"},
			"X-Content-Type-Options": {"nosniff"},
		},
		Body: body,
	}
}
