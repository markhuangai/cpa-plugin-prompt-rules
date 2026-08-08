//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPromptRulesWithCLIProxyAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the native host smoke test is currently exercised on Unix hosts")
	}
	cpaSource := os.Getenv("CPA_SOURCE")
	if cpaSource == "" {
		cpaSource = filepath.Join("..", "CLIProxyAPI")
	}
	cpaSource, err := filepath.Abs(cpaSource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cpaSource, "go.mod")); err != nil {
		t.Fatalf("CPA source not found at %s; set CPA_SOURCE: %v", cpaSource, err)
	}

	provider := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.HasSuffix(request.URL.Path, "/chat/completions") {
			http.NotFound(response, request)
			return
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		system := ""
		for _, message := range body.Messages {
			if message.Role == "system" {
				system = message.Content
			}
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"id":      "prompt-rules-smoke",
			"object":  "chat.completion",
			"created": 1,
			"model":   body.Model,
			"choices": []any{map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": system,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]int{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	defer provider.Close()

	workDir := t.TempDir()
	pluginsDir := filepath.Join(workDir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pluginPath := filepath.Join(pluginsDir, "prompt-rules"+sharedLibraryExtension())
	runSmokeCommand(t, ".", "go", "build", "-buildmode=c-shared", "-o", pluginPath, ".")
	cpaBinary := filepath.Join(workDir, "cli-proxy-api")
	runSmokeCommand(t, cpaSource, "go", "build", "-o", cpaBinary, "./cmd/server")

	port := reserveLocalPort(t)
	configPath := filepath.Join(workDir, "config.yaml")
	config := fmt.Sprintf(`host: "127.0.0.1"
port: %d
auth-dir: %q
api-keys: ["local-test-key"]
request-retry: 0
remote-management:
  allow-remote: false
  secret-key: "local-management-key"
  disable-control-panel: true
plugins:
  enabled: true
  dir: %q
  configs:
    prompt-rules:
      enabled: true
      priority: 100
      rules:
        - name: smoke-system
          enabled: true
          models:
            - name: working-model
              protocol: openai
          target: system
          action: inject
          position: append
          content: "|PLUGIN"
openai-compatibility:
  - name: mock
    base-url: %q
    api-key-entries:
      - api-key: mock-provider-key
    models:
      - name: working-model
`, port, filepath.Join(workDir, "auth"), pluginsDir, provider.URL+"/v1")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	logs := startSmokeCPA(t, cpaBinary, configPath, baseURL)
	verifyPromptManagementUI(t, baseURL, logs)
	response := postSmokeChat(t, baseURL, "working-model", []map[string]string{
		{"role": "system", "content": "base"},
		{"role": "user", "content": "hello"},
	})
	choices, ok := response["choices"].([]any)
	if !ok || len(choices) != 1 {
		t.Fatalf("unexpected response choices: %#v", response)
	}
	choice, _ := choices[0].(map[string]any)
	message, _ := choice["message"].(map[string]any)
	if content, _ := message["content"].(string); content != "base|PLUGIN" {
		t.Fatalf("assistant content = %q, want %q; response = %#v\n%s", content, "base|PLUGIN", response, logs.String())
	}
}

func verifyPromptManagementUI(t *testing.T, baseURL string, logs *smokeSyncBuffer) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	request, err := http.NewRequest(http.MethodGet, baseURL+"/v0/management/plugins", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-management-key")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("list plugins: %v\n%s", err, logs.String())
	}
	defer response.Body.Close()
	var plugins struct {
		Plugins []struct {
			ID    string `json:"id"`
			Menus []struct {
				Path string `json:"path"`
				Menu string `json:"menu"`
			} `json:"menus"`
		} `json:"plugins"`
	}
	if err := json.NewDecoder(response.Body).Decode(&plugins); err != nil {
		t.Fatalf("decode plugin list: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("plugin list status = %s\n%s", response.Status, logs.String())
	}
	resourcePath := ""
	for _, plugin := range plugins.Plugins {
		if plugin.ID != pluginID {
			continue
		}
		for _, menu := range plugin.Menus {
			if menu.Menu == pluginName {
				resourcePath = menu.Path
			}
		}
	}
	if resourcePath != promptDashboardPath {
		t.Fatalf("dashboard path = %q, want %q; plugins=%#v\n%s", resourcePath, promptDashboardPath, plugins, logs.String())
	}

	resourceResponse, err := client.Get(baseURL + resourcePath)
	if err != nil {
		t.Fatalf("get dashboard: %v", err)
	}
	defer resourceResponse.Body.Close()
	resourceBody, err := io.ReadAll(resourceResponse.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resourceResponse.StatusCode != http.StatusOK || !strings.Contains(string(resourceBody), "<title>Prompt Rules</title>") {
		t.Fatalf("dashboard status=%s body=%q", resourceResponse.Status, resourceBody)
	}

	validation := []byte(`{"enabled":true,"rules":[{"name":"smoke","enabled":true,"target":"system","action":"inject","content":"ok"}]}`)
	unauthenticatedRequest, err := http.NewRequest(http.MethodPost, baseURL+promptValidationPath, bytes.NewReader(validation))
	if err != nil {
		t.Fatal(err)
	}
	unauthenticatedRequest.Header.Set("Content-Type", "application/json")
	unauthenticatedResponse, err := client.Do(unauthenticatedRequest)
	if err != nil {
		t.Fatalf("validate dashboard config without authentication: %v", err)
	}
	unauthenticatedResponse.Body.Close()
	if unauthenticatedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated validation status=%s, want 401", unauthenticatedResponse.Status)
	}

	validationRequest, err := http.NewRequest(http.MethodPost, baseURL+promptValidationPath, bytes.NewReader(validation))
	if err != nil {
		t.Fatal(err)
	}
	validationRequest.Header.Set("Authorization", "Bearer local-management-key")
	validationRequest.Header.Set("Content-Type", "application/json")
	validationResponse, err := client.Do(validationRequest)
	if err != nil {
		t.Fatalf("validate dashboard config: %v", err)
	}
	defer validationResponse.Body.Close()
	var validationBody map[string]any
	if err := json.NewDecoder(validationResponse.Body).Decode(&validationBody); err != nil {
		t.Fatal(err)
	}
	if validationResponse.StatusCode != http.StatusOK || validationBody["valid"] != true {
		t.Fatalf("validation status=%s body=%#v", validationResponse.Status, validationBody)
	}
}

type smokeSyncBuffer struct {
	sync.Mutex
	bytes.Buffer
}

func (buffer *smokeSyncBuffer) Write(payload []byte) (int, error) {
	buffer.Lock()
	defer buffer.Unlock()
	return buffer.Buffer.Write(payload)
}

func (buffer *smokeSyncBuffer) String() string {
	buffer.Lock()
	defer buffer.Unlock()
	return buffer.Buffer.String()
}

func runSmokeCommand(t *testing.T, dir, name string, arguments ...string) {
	t.Helper()
	command := exec.Command(name, arguments...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(arguments, " "), err, output)
	}
}

func reserveLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func startSmokeCPA(t *testing.T, binary, configPath, baseURL string) *smokeSyncBuffer {
	t.Helper()
	logs := &smokeSyncBuffer{}
	command := exec.Command(binary, "--config", configPath)
	command.Stdout = logs
	command.Stderr = logs
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("CLIProxyAPI exited before becoming ready: %v\n%s", err, logs.String())
		default:
		}
		request, err := http.NewRequest(http.MethodGet, baseURL+"/v1/models", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer local-test-key")
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return logs
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("CLIProxyAPI did not become ready at %s\n%s", baseURL, logs.String())
	return logs
}

func postSmokeChat(t *testing.T, baseURL, model string, messages []map[string]string) map[string]any {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"model": model, "messages": messages, "stream": false})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer local-test-key")
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode status %s response: %v", response.Status, err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("chat status = %s; body = %#v", response.Status, body)
	}
	return body
}

func sharedLibraryExtension() string {
	switch runtime.GOOS {
	case "darwin":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}
