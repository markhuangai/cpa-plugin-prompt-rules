package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int PromptRulesPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void PromptRulesPluginFree(void*, size_t);
extern void PromptRulesPluginShutdown(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type abiEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *abiError       `json:"error,omitempty"`
}

type abiError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type requestInterceptRPC struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	RequestInterceptor bool `json:"request_interceptor"`
}

var promptABIState = struct {
	sync.Mutex
	plugin       *promptRulesPlugin
	shuttingDown bool
	inFlight     sync.WaitGroup
}{}

const maxCGoRequestLen = C.size_t(1<<31 - 1)

// The plugin uses no schema-v2 request-lifecycle fields, so advertising v1 keeps older CPA v7 hosts compatible.
const registrationSchemaVersion uint32 = 1

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	promptABIState.Lock()
	promptABIState.shuttingDown = false
	promptABIState.Unlock()
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.PromptRulesPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.PromptRulesPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.PromptRulesPluginShutdown)
	return 0
}

//export PromptRulesPluginCall
func PromptRulesPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, errorEnvelope("invalid_method", "method is required", 0))
		return 0
	}
	if requestLen > maxCGoRequestLen {
		writeABIResponse(response, errorEnvelope("request_too_large", "request payload is too large", 0))
		return 0
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handlePromptABIMethod(context.Background(), C.GoString(method), requestBytes)
	if err != nil {
		raw = errorEnvelope("plugin_error", err.Error(), 0)
	}
	writeABIResponse(response, raw)
	return 0
}

//export PromptRulesPluginFree
func PromptRulesPluginFree(pointer unsafe.Pointer, _ C.size_t) {
	if pointer != nil {
		C.free(pointer)
	}
}

//export PromptRulesPluginShutdown
func PromptRulesPluginShutdown() {
	promptABIState.Lock()
	promptABIState.shuttingDown = true
	promptABIState.plugin = nil
	promptABIState.Unlock()
	promptABIState.inFlight.Wait()
}

func handlePromptABIMethod(ctx context.Context, method string, request []byte) ([]byte, error) {
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		return registerPromptPlugin(request)
	}
	plugin, done, err := beginPromptCall()
	if err != nil {
		return nil, err
	}
	defer done()
	var rpcRequest requestInterceptRPC
	if err := json.Unmarshal(request, &rpcRequest); err != nil {
		return nil, fmt.Errorf("decode %s request: %w", method, err)
	}
	switch method {
	case pluginabi.MethodRequestInterceptBefore:
		response, err := plugin.InterceptRequestBeforeAuth(ctx, rpcRequest.RequestInterceptRequest)
		return okEnvelopeWithError(response, err)
	case pluginabi.MethodRequestInterceptAfter:
		response, err := plugin.InterceptRequestAfterAuth(ctx, rpcRequest.RequestInterceptRequest)
		return okEnvelopeWithError(response, err)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, 0), nil
	}
}

func registerPromptPlugin(raw []byte) ([]byte, error) {
	var request lifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, fmt.Errorf("decode lifecycle request: %w", err)
		}
	}
	plugin, metadata, err := buildPlugin(request.ConfigYAML)
	if err != nil {
		return nil, err
	}
	promptABIState.Lock()
	promptABIState.plugin = plugin
	promptABIState.shuttingDown = false
	promptABIState.Unlock()
	return okEnvelope(abiRegistration{
		SchemaVersion: registrationSchemaVersion,
		Metadata:      metadata,
		Capabilities:  abiCapabilities{RequestInterceptor: true},
	})
}

func beginPromptCall() (*promptRulesPlugin, func(), error) {
	promptABIState.Lock()
	defer promptABIState.Unlock()
	if promptABIState.shuttingDown {
		return nil, nil, fmt.Errorf("%s is shutting down", pluginID)
	}
	if promptABIState.plugin == nil {
		return nil, nil, fmt.Errorf("%s is not registered", pluginID)
	}
	promptABIState.inFlight.Add(1)
	return promptABIState.plugin, promptABIState.inFlight.Done, nil
}

func okEnvelopeWithError(value any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return okEnvelope(value)
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(abiEnvelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(abiEnvelope{OK: false, Error: &abiError{Code: code, Message: message, HTTPStatus: status}})
	return raw
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	pointer := C.CBytes(raw)
	if pointer == nil {
		return
	}
	response.ptr = pointer
	response.len = C.size_t(len(raw))
}
