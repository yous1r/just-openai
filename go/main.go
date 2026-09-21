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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const (
	pluginIdentifier = "openai-anthropic-messages"
	defaultVersion   = "2023-06-01"
)

var (
	currentConfig atomic.Value
	keyCursor     atomic.Uint64
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginConfig struct {
	Enabled          bool              `yaml:"enabled"`
	BaseAPI          string            `yaml:"base_api"`
	APIKey           string            `yaml:"api_key"`
	APIKeys          []string          `yaml:"api_keys"`
	AuthHeader       string            `yaml:"auth_header"`
	AnthropicVersion string            `yaml:"anthropic_version"`
	Prefix           string            `yaml:"prefix"`
	Models           []modelConfig     `yaml:"models"`
	Headers          map[string]string `yaml:"headers"`
}

type modelConfig struct {
	Name            string `yaml:"name" json:"name"`
	Alias           string `yaml:"alias" json:"alias,omitempty"`
	DisplayName     string `yaml:"display_name" json:"display_name,omitempty"`
	ContextLength   int64  `yaml:"context_length" json:"context_length,omitempty"`
	MaxOutputTokens int64  `yaml:"max_output_tokens" json:"max_output_tokens,omitempty"`
	// Stream pins the upstream Messages request mode for this model.
	// Unset follows the client request; true always streams upstream and
	// aggregates for non-streaming clients; false never streams upstream and
	// synthesizes a Claude SSE stream for streaming clients.
	Stream *bool `yaml:"stream" json:"stream,omitempty"`
}

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ModelProvider         bool                         `json:"model_provider"`
	ModelRouter           bool                         `json:"model_router"`
	Executor              bool                         `json:"executor"`
	ExecutorModelScope    pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats  []string                     `json:"executor_input_formats"`
	ExecutorOutputFormats []string                     `json:"executor_output_formats"`
	ManagementAPI         bool                         `json:"management_api"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcManagementRequest struct {
	pluginapi.ManagementRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type hostHTTPRequest struct {
	HostCallbackID string                     `json:"host_callback_id,omitempty"`
	Method         string                     `json:"method"`
	URL            string                     `json:"url"`
	Headers        http.Header                `json:"headers,omitempty"`
	Body           []byte                     `json:"body,omitempty"`
	WireProfile    *pluginapi.HTTPWireProfile `json:"wire_profile,omitempty"`
}

type hostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id,omitempty"`
}

type hostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type pluginStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
}

type pluginStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required", http.StatusBadRequest))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error(), http.StatusInternalServerError))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelStatic:
		return okEnvelope(staticModels())
	case pluginabi.MethodModelForAuth:
		return okEnvelope(staticModels())
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`), Headers: http.Header{"Content-Type": []string{"application/json"}}})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(pluginapi.ManagementRegistrationResponse{Resources: []pluginapi.ResourceRoute{{
			Path:        "/status",
			Menu:        "OpenAI via Anthropic",
			Description: "Shows the normalized Anthropic Messages route and configured public models.",
		}}})
	case pluginabi.MethodManagementHandle:
		return managementStatus(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotFound), nil
	}
}

func configure(raw []byte) error {
	var request lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
		}
	}
	cfg := defaultPluginConfig()
	if len(request.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(request.ConfigYAML, &cfg); errUnmarshal != nil {
			return fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	normalizeConfig(&cfg)
	currentConfig.Store(cfg)
	return nil
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:          false,
		AuthHeader:       "x-api-key",
		AnthropicVersion: defaultVersion,
	}
}

func normalizeConfig(cfg *pluginConfig) {
	if cfg == nil {
		return
	}
	cfg.BaseAPI = normalizeBaseAPI(cfg.BaseAPI)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.AuthHeader = strings.ToLower(strings.TrimSpace(cfg.AuthHeader))
	if cfg.AuthHeader == "" {
		cfg.AuthHeader = "x-api-key"
	}
	cfg.AnthropicVersion = strings.TrimSpace(cfg.AnthropicVersion)
	if cfg.AnthropicVersion == "" {
		cfg.AnthropicVersion = defaultVersion
	}
	cfg.Prefix = strings.Trim(strings.TrimSpace(cfg.Prefix), "/")

	keys := make([]string, 0, len(cfg.APIKeys)+1)
	seenKeys := make(map[string]struct{})
	for _, key := range append([]string{cfg.APIKey}, cfg.APIKeys...) {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seenKeys[key]; exists {
			continue
		}
		seenKeys[key] = struct{}{}
		keys = append(keys, key)
	}
	cfg.APIKeys = keys

	models := make([]modelConfig, 0, len(cfg.Models))
	seenModels := make(map[string]struct{})
	for _, model := range cfg.Models {
		model.Name = strings.TrimSpace(model.Name)
		model.Alias = strings.TrimSpace(model.Alias)
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		if model.Name == "" {
			continue
		}
		publicID := publicModelID(*cfg, model)
		if publicID == "" {
			continue
		}
		if _, exists := seenModels[publicID]; exists {
			continue
		}
		seenModels[publicID] = struct{}{}
		models = append(models, model)
	}
	cfg.Models = models
}

func loadedConfig() pluginConfig {
	if raw := currentConfig.Load(); raw != nil {
		if cfg, ok := raw.(pluginConfig); ok {
			return cfg
		}
	}
	return defaultPluginConfig()
}

func normalizeBaseAPI(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Scheme == "" || parsed.Host == "" {
		return raw
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.EqualFold(path, "/v1") && !strings.HasSuffix(strings.ToLower(path), "/v1") {
		path += "/v1"
	}
	parsed.Path = path
	return strings.TrimRight(parsed.String(), "/")
}

func messagesURL(baseAPI string) string {
	return strings.TrimRight(normalizeBaseAPI(baseAPI), "/") + "/messages"
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginIdentifier,
			Version:          "0.4.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enables routing for the configured models."},
				{Name: "base_api", Type: pluginapi.ConfigFieldTypeString, Description: "Upstream API root. /v1 is appended automatically, matching cc-switch routing."},
				{Name: "api_key", Type: pluginapi.ConfigFieldTypeString, Description: "Primary upstream API key."},
				{Name: "api_keys", Type: pluginapi.ConfigFieldTypeArray, Description: "Optional additional API keys used round-robin."},
				{Name: "auth_header", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"x-api-key", "bearer", "both"}, Description: "Authentication header style expected by the upstream."},
				{Name: "anthropic_version", Type: pluginapi.ConfigFieldTypeString, Description: "Anthropic-Version header; defaults to 2023-06-01."},
				{Name: "prefix", Type: pluginapi.ConfigFieldTypeString, Description: "Optional public model prefix, for example omp creates omp/model-name."},
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Models: [{name, alias, display_name, context_length, max_output_tokens, stream}]. name is sent upstream; alias is exposed publicly. stream pins the upstream Messages mode per model: unset follows the client request, true always streams upstream (non-streaming clients get the aggregated message), false never streams upstream (streaming clients get synthesized Claude SSE)."},
				{Name: "headers", Type: pluginapi.ConfigFieldTypeObject, Description: "Optional additional upstream request headers."},
			},
		},
		Capabilities: registrationCapabilities{
			ModelProvider:         true,
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    pluginapi.ExecutorModelScopeStatic,
			ExecutorInputFormats:  []string{"claude"},
			ExecutorOutputFormats: []string{"claude"},
			ManagementAPI:         true,
		},
	}
}

func staticModels() pluginapi.ModelResponse {
	cfg := loadedConfig()
	response := pluginapi.ModelResponse{Provider: pluginIdentifier}
	if !cfg.Enabled || !configRunnable(cfg) {
		return response
	}
	response.Models = make([]pluginapi.ModelInfo, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		id := publicModelID(cfg, model)
		displayName := model.DisplayName
		if displayName == "" {
			displayName = id
		}
		response.Models = append(response.Models, pluginapi.ModelInfo{
			ID:                         id,
			Object:                     "model",
			OwnedBy:                    "openai",
			Type:                       "openai",
			DisplayName:                displayName,
			Name:                       model.Name,
			SupportedGenerationMethods: []string{"chat"},
			ContextLength:              model.ContextLength,
			MaxCompletionTokens:        model.MaxOutputTokens,
			SupportedInputModalities:   []string{"text", "image"},
			SupportedOutputModalities:  []string{"text"},
			UserDefined:                true,
		})
	}
	return response
}

func routeModel(raw []byte) ([]byte, error) {
	var request rpcModelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	if !cfg.Enabled || !configRunnable(cfg) || !supportedSourceFormat(request.SourceFormat) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if _, ok := resolveUpstreamModel(cfg, request.RequestedModel); !ok {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	return okEnvelope(pluginapi.ModelRouteResponse{
		Handled:    true,
		TargetKind: pluginapi.ModelRouteTargetSelf,
		Reason:     "configured_openai_model_via_anthropic_messages",
	})
}

func supportedSourceFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "claude", "anthropic", "openai", "chat-completions", "chat_completions", "responses", "openai-response":
		return true
	default:
		return false
	}
}

func configRunnable(cfg pluginConfig) bool {
	return cfg.BaseAPI != "" && len(cfg.Models) > 0
}

func allAPIKeys(cfg pluginConfig) []string {
	keys := make([]string, 0, len(cfg.APIKeys)+1)
	if key := strings.TrimSpace(cfg.APIKey); key != "" {
		keys = append(keys, key)
	}
	for _, key := range cfg.APIKeys {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func publicModelID(cfg pluginConfig, model modelConfig) string {
	name := strings.TrimSpace(model.Alias)
	if name == "" {
		name = strings.TrimSpace(model.Name)
	}
	if name == "" {
		return ""
	}
	if cfg.Prefix == "" {
		return name
	}
	return strings.Trim(cfg.Prefix, "/") + "/" + strings.TrimLeft(name, "/")
}

func modelWithoutThinkingSuffix(model string) string {
	model = strings.TrimSpace(model)
	open := strings.LastIndex(model, "(")
	if open >= 0 && strings.HasSuffix(model, ")") {
		return strings.TrimSpace(model[:open])
	}
	return model
}

func resolveModelConfig(cfg pluginConfig, requested string) (modelConfig, bool) {
	requested = modelWithoutThinkingSuffix(requested)
	for _, model := range cfg.Models {
		if requested == publicModelID(cfg, model) {
			return model, true
		}
	}
	return modelConfig{}, false
}

func resolveUpstreamModel(cfg pluginConfig, requested string) (string, bool) {
	model, ok := resolveModelConfig(cfg, requested)
	if !ok {
		return "", false
	}
	return model.Name, true
}

// upstreamStreamMode reports whether the upstream Messages request must be sent
// with stream=true. An unset pin follows the client request.
func upstreamStreamMode(model modelConfig, clientStream bool) bool {
	if model.Stream == nil {
		return clientStream
	}
	return *model.Stream
}

func streamModeLabel(model modelConfig) string {
	if model.Stream == nil {
		return "client"
	}
	if *model.Stream {
		return "always"
	}
	return "never"
}

func execute(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	model, ok := resolveModelConfig(cfg, request.Model)
	if !ok || !configRunnable(cfg) {
		return errorEnvelope("model_not_configured", "model is not configured for this plugin", http.StatusBadRequest), nil
	}
	if upstreamStreamMode(model, false) {
		return executeAggregatedResponse(cfg, model.Name, request)
	}
	payload, errPayload := prepareAnthropicPayload(request.Payload, model.Name, false)
	if errPayload != nil {
		return errorEnvelope("invalid_request", errPayload.Error(), http.StatusBadRequest), nil
	}
	response, errCall := callHostHTTP(pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         http.MethodPost,
		URL:            messagesURL(cfg.BaseAPI),
		Headers:        upstreamHeaders(cfg),
		Body:           payload,
	})
	if errCall != nil {
		return errorEnvelope("upstream_error", errCall.Error(), http.StatusBadGateway), nil
	}
	var upstream pluginapi.HTTPResponse
	if errUnmarshal := json.Unmarshal(response, &upstream); errUnmarshal != nil {
		return nil, fmt.Errorf("decode upstream response: %w", errUnmarshal)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return upstreamErrorEnvelope(upstream.StatusCode, upstream.Body), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: upstream.Body, Headers: upstream.Headers})
}

// executeAggregatedResponse serves a non-streaming client from an upstream that is
// pinned to streaming: it drains the Messages SSE stream and folds it back into a
// single Claude message body so the client sees the shape it asked for.
func executeAggregatedResponse(cfg pluginConfig, upstreamModel string, request rpcExecutorRequest) ([]byte, error) {
	payload, errPayload := prepareAnthropicPayload(request.Payload, upstreamModel, true)
	if errPayload != nil {
		return errorEnvelope("invalid_request", errPayload.Error(), http.StatusBadRequest), nil
	}
	response, errCall := callHostHTTP(pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         http.MethodPost,
		URL:            messagesURL(cfg.BaseAPI),
		Headers:        upstreamHeaders(cfg),
		Body:           payload,
	})
	if errCall != nil {
		return errorEnvelope("upstream_error", errCall.Error(), http.StatusBadGateway), nil
	}
	var upstream hostHTTPStreamResponse
	if errUnmarshal := json.Unmarshal(response, &upstream); errUnmarshal != nil {
		return nil, fmt.Errorf("decode upstream stream response: %w", errUnmarshal)
	}
	if strings.TrimSpace(upstream.StreamID) == "" {
		return errorEnvelope("stream_unavailable", "upstream returned no stream id", http.StatusBadGateway), nil
	}
	stream, errDrain := drainHostHTTPStream(upstream.StreamID, request.HostCallbackID)
	if errDrain != nil {
		return errorEnvelope("upstream_error", errDrain.Error(), http.StatusBadGateway), nil
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return upstreamErrorEnvelope(upstream.StatusCode, stream), nil
	}
	body, errConvert := claudeStreamToResponse(stream)
	if errConvert != nil {
		return errorEnvelope("upstream_error", errConvert.Error(), http.StatusBadGateway), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: body, Headers: upstream.Headers})
}

func executeStream(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if strings.TrimSpace(request.StreamID) == "" {
		return errorEnvelope("stream_unavailable", "plugin stream id is required", http.StatusInternalServerError), nil
	}
	cfg := loadedConfig()
	model, ok := resolveModelConfig(cfg, request.Model)
	if !ok || !configRunnable(cfg) {
		return errorEnvelope("model_not_configured", "model is not configured for this plugin", http.StatusBadRequest), nil
	}
	if !upstreamStreamMode(model, true) {
		return executeSynthesizedStream(cfg, model.Name, request)
	}
	payload, errPayload := prepareAnthropicPayload(request.Payload, model.Name, true)
	if errPayload != nil {
		return errorEnvelope("invalid_request", errPayload.Error(), http.StatusBadRequest), nil
	}
	response, errCall := callHostHTTP(pluginabi.MethodHostHTTPDoStream, hostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         http.MethodPost,
		URL:            messagesURL(cfg.BaseAPI),
		Headers:        upstreamHeaders(cfg),
		Body:           payload,
	})
	if errCall != nil {
		return errorEnvelope("upstream_error", errCall.Error(), http.StatusBadGateway), nil
	}
	var upstream hostHTTPStreamResponse
	if errUnmarshal := json.Unmarshal(response, &upstream); errUnmarshal != nil {
		return nil, fmt.Errorf("decode upstream stream response: %w", errUnmarshal)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return errorEnvelope("upstream_error", fmt.Sprintf("upstream returned HTTP %d", upstream.StatusCode), upstream.StatusCode), nil
	}
	if strings.TrimSpace(upstream.StreamID) == "" {
		return errorEnvelope("stream_unavailable", "upstream returned no stream id", http.StatusBadGateway), nil
	}
	go forwardHTTPStream(upstream.StreamID, request.StreamID, request.HostCallbackID)
	return okEnvelope(map[string]any{"headers": upstream.Headers})
}

// executeSynthesizedStream serves a streaming client from an upstream that is
// pinned to non-streaming: it replays the single Claude message body as the
// Claude SSE event sequence the client expects.
func executeSynthesizedStream(cfg pluginConfig, upstreamModel string, request rpcExecutorRequest) ([]byte, error) {
	payload, errPayload := prepareAnthropicPayload(request.Payload, upstreamModel, false)
	if errPayload != nil {
		return errorEnvelope("invalid_request", errPayload.Error(), http.StatusBadRequest), nil
	}
	response, errCall := callHostHTTP(pluginabi.MethodHostHTTPDo, hostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         http.MethodPost,
		URL:            messagesURL(cfg.BaseAPI),
		Headers:        upstreamHeaders(cfg),
		Body:           payload,
	})
	if errCall != nil {
		return errorEnvelope("upstream_error", errCall.Error(), http.StatusBadGateway), nil
	}
	var upstream pluginapi.HTTPResponse
	if errUnmarshal := json.Unmarshal(response, &upstream); errUnmarshal != nil {
		return nil, fmt.Errorf("decode upstream response: %w", errUnmarshal)
	}
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return upstreamErrorEnvelope(upstream.StatusCode, upstream.Body), nil
	}
	frames, errConvert := claudeResponseToSSEFrames(upstream.Body, upstreamModel)
	if errConvert != nil {
		return errorEnvelope("upstream_error", errConvert.Error(), http.StatusBadGateway), nil
	}
	go emitSynthesizedStream(frames, request.StreamID, request.HostCallbackID)
	return okEnvelope(map[string]any{"headers": upstream.Headers})
}

// drainHostHTTPStream reads a host HTTP stream to completion.
func drainHostHTTPStream(streamID, callbackID string) ([]byte, error) {
	var stream bytes.Buffer
	for {
		raw, errRead := callHostHTTP(pluginabi.MethodHostHTTPStreamRead, struct {
			HostCallbackID string `json:"host_callback_id,omitempty"`
			StreamID       string `json:"stream_id"`
		}{HostCallbackID: callbackID, StreamID: streamID})
		if errRead != nil {
			closeHostHTTPStream(streamID, callbackID)
			return nil, errRead
		}
		var chunk hostHTTPStreamReadResponse
		if errUnmarshal := json.Unmarshal(raw, &chunk); errUnmarshal != nil {
			closeHostHTTPStream(streamID, callbackID)
			return nil, errUnmarshal
		}
		if len(chunk.Payload) > 0 {
			stream.Write(chunk.Payload)
		}
		if chunk.Error != "" {
			closeHostHTTPStream(streamID, callbackID)
			return nil, fmt.Errorf("%s", chunk.Error)
		}
		if chunk.Done {
			return stream.Bytes(), nil
		}
	}
}

// emitSynthesizedStream pushes pre-built SSE frames to the client stream, one
// host emit per frame, and closes it.
func emitSynthesizedStream(frames [][]byte, pluginStreamID, callbackID string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			closePluginStream(pluginStreamID, fmt.Sprintf("stream synthesis panic: %v", recovered), callbackID)
		}
	}()
	for _, frame := range frames {
		_, errEmit := callHostHTTP(pluginabi.MethodHostStreamEmit, struct {
			HostCallbackID string `json:"host_callback_id,omitempty"`
			pluginStreamEmitRequest
		}{HostCallbackID: callbackID, pluginStreamEmitRequest: pluginStreamEmitRequest{StreamID: pluginStreamID, Payload: frame}})
		if errEmit != nil {
			closePluginStream(pluginStreamID, errEmit.Error(), callbackID)
			return
		}
	}
	closePluginStream(pluginStreamID, "", callbackID)
}

// claudeSSEFrame renders one complete SSE event (event name + data + blank line).
func claudeSSEFrame(event string, payload map[string]any) ([]byte, error) {
	raw, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode %s event: %w", event, errMarshal)
	}
	frame := append([]byte("event: "), event...)
	frame = append(frame, '\n')
	frame = append(frame, "data: "...)
	frame = append(frame, raw...)
	return append(frame, '\n', '\n'), nil
}

func claudeContentBlockStartFrame(index int, contentBlock map[string]any) ([]byte, error) {
	return claudeSSEFrame("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         index,
		"content_block": contentBlock,
	})
}

func claudeContentBlockDeltaFrame(index int, delta map[string]any) ([]byte, error) {
	return claudeSSEFrame("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": delta,
	})
}

// claudeResponseToSSEFrames renders a non-streaming Anthropic Messages body as the
// Claude SSE event sequence a streaming client expects.
func claudeResponseToSSEFrames(body []byte, fallbackModel string) ([][]byte, error) {
	root, errDecode := decodeJSONObject(body)
	if errDecode != nil {
		return nil, fmt.Errorf("upstream response must be a JSON object: %w", errDecode)
	}
	content, ok := root["content"].([]any)
	if !ok {
		return nil, fmt.Errorf("upstream response has no content array")
	}
	message := make(map[string]any, len(root))
	for key, value := range root {
		switch key {
		case "content", "stop_reason", "stop_sequence":
			continue
		}
		message[key] = value
	}
	message["content"] = []any{}
	message["stop_reason"] = nil
	message["stop_sequence"] = nil
	if blockType, _ := message["type"].(string); strings.TrimSpace(blockType) == "" {
		message["type"] = "message"
	}
	if role, _ := message["role"].(string); strings.TrimSpace(role) == "" {
		message["role"] = "assistant"
	}
	if id, _ := message["id"].(string); strings.TrimSpace(id) == "" {
		message["id"] = fmt.Sprintf("msg_%d", time.Now().UnixNano())
	}
	if model, _ := message["model"].(string); strings.TrimSpace(model) == "" {
		message["model"] = strings.TrimSpace(fallbackModel)
	}
	usage := claudeUsageObject(root["usage"])
	message["usage"] = usage

	frames := make([][]byte, 0, 2*len(content)+4)
	startFrame, errStart := claudeSSEFrame("message_start", map[string]any{"type": "message_start", "message": message})
	if errStart != nil {
		return nil, errStart
	}
	frames = append(frames, startFrame)
	for index, rawBlock := range content {
		block, ok := rawBlock.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("upstream response content block %d is not an object", index)
		}
		blockFrames, errBlock := claudeContentBlockFrames(index, block)
		if errBlock != nil {
			return nil, errBlock
		}
		frames = append(frames, blockFrames...)
	}
	deltaFrame, errDelta := claudeSSEFrame("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": root["stop_reason"], "stop_sequence": root["stop_sequence"]},
		"usage": usage,
	})
	if errDelta != nil {
		return nil, errDelta
	}
	frames = append(frames, deltaFrame)
	stopFrame, errStop := claudeSSEFrame("message_stop", map[string]any{"type": "message_stop"})
	if errStop != nil {
		return nil, errStop
	}
	return append(frames, stopFrame), nil
}

// claudeContentBlockFrames expands one content block into start/delta/stop frames.
// Text and thinking blocks stream incrementally; tool blocks stream their input as
// partial JSON; every other block type is sent whole inside content_block_start.
func claudeContentBlockFrames(index int, block map[string]any) ([][]byte, error) {
	blockType, _ := block["type"].(string)
	frames := make([][]byte, 0, 4)
	switch blockType {
	case "text", "thinking":
		contentBlock := map[string]any{"type": blockType, blockType: ""}
		citations, _ := block["citations"].([]any)
		if len(citations) > 0 {
			contentBlock["citations"] = []any{}
		}
		startFrame, errStart := claudeContentBlockStartFrame(index, contentBlock)
		if errStart != nil {
			return nil, errStart
		}
		frames = append(frames, startFrame)
		if text, _ := block[blockType].(string); text != "" {
			deltaFrame, errDelta := claudeContentBlockDeltaFrame(index, map[string]any{
				"type":    blockType + "_delta",
				blockType: text,
			})
			if errDelta != nil {
				return nil, errDelta
			}
			frames = append(frames, deltaFrame)
		}
		if len(citations) > 0 {
			for _, citation := range citations {
				deltaFrame, errDelta := claudeContentBlockDeltaFrame(index, map[string]any{
					"type":     "citations_delta",
					"citation": citation,
				})
				if errDelta != nil {
					return nil, errDelta
				}
				frames = append(frames, deltaFrame)
			}
		}
		if signature, _ := block["signature"].(string); signature != "" {
			deltaFrame, errDelta := claudeContentBlockDeltaFrame(index, map[string]any{
				"type":      "signature_delta",
				"signature": signature,
			})
			if errDelta != nil {
				return nil, errDelta
			}
			frames = append(frames, deltaFrame)
		}
	case "tool_use", "server_tool_use":
		startFrame, errStart := claudeContentBlockStartFrame(index, map[string]any{
			"type":  blockType,
			"id":    stringValue(block["id"]),
			"name":  stringValue(block["name"]),
			"input": map[string]any{},
		})
		if errStart != nil {
			return nil, errStart
		}
		frames = append(frames, startFrame)
		if rawInput, errMarshal := json.Marshal(claudeToolInput(block["input"])); errMarshal != nil {
			return nil, fmt.Errorf("encode tool input: %w", errMarshal)
		} else if !bytes.Equal(rawInput, []byte("{}")) && !bytes.Equal(rawInput, []byte("null")) {
			deltaFrame, errDelta := claudeContentBlockDeltaFrame(index, map[string]any{
				"type":         "input_json_delta",
				"partial_json": string(rawInput),
			})
			if errDelta != nil {
				return nil, errDelta
			}
			frames = append(frames, deltaFrame)
		}
	default:
		startFrame, errStart := claudeContentBlockStartFrame(index, block)
		if errStart != nil {
			return nil, errStart
		}
		frames = append(frames, startFrame)
	}
	stopFrame, errStop := claudeSSEFrame("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": index,
	})
	if errStop != nil {
		return nil, errStop
	}
	return append(frames, stopFrame), nil
}

// claudeStreamToResponse folds a Claude SSE stream back into one non-streaming
// Anthropic Messages body. It fails when the stream is truncated (no message_stop)
// or carries an error event, so a non-streaming client never receives a partial message.
func claudeStreamToResponse(stream []byte) ([]byte, error) {
	var (
		message    map[string]any
		usage      map[string]any
		blocks     = make(map[int]map[string]any)
		blockOrder []int
		partials   = make(map[int]*strings.Builder)
		stopReason any
		stopSeq    any
		hasStop    bool
	)
	for _, line := range bytes.Split(stream, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		event, errDecode := decodeJSONObject(payload)
		if errDecode != nil {
			return nil, fmt.Errorf("upstream stream line is not a JSON object: %w", errDecode)
		}
		switch eventType, _ := event["type"].(string); eventType {
		case "message_start":
			started, ok := event["message"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("upstream stream message_start has no message object")
			}
			message = started
			if startedUsage, ok := started["usage"].(map[string]any); ok {
				usage = cloneJSONObject(startedUsage)
			}
		case "content_block_start":
			index, ok := jsonIndex(event["index"])
			if !ok {
				return nil, fmt.Errorf("upstream stream content_block_start has no index")
			}
			block, ok := event["content_block"].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("upstream stream content_block_start has no content_block object")
			}
			if _, exists := blocks[index]; !exists {
				blockOrder = append(blockOrder, index)
			}
			blocks[index] = block
		case "content_block_delta":
			index, ok := jsonIndex(event["index"])
			delta, _ := event["delta"].(map[string]any)
			if !ok || delta == nil {
				continue
			}
			block, exists := blocks[index]
			if !exists {
				block = claudeBlockForDelta(delta)
				blocks[index] = block
				blockOrder = append(blockOrder, index)
			}
			switch deltaType, _ := delta["type"].(string); deltaType {
			case "text_delta":
				appendBlockString(block, "text", delta["text"])
			case "thinking_delta":
				appendBlockString(block, "thinking", delta["thinking"])
			case "signature_delta":
				appendBlockString(block, "signature", delta["signature"])
			case "input_json_delta":
				if partials[index] == nil {
					partials[index] = &strings.Builder{}
				}
				partials[index].WriteString(stringValue(delta["partial_json"]))
			case "citations_delta":
				if citation, exists := delta["citation"]; exists {
					citations, _ := block["citations"].([]any)
					block["citations"] = append(citations, citation)
				}
			}
		case "message_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				if value, exists := delta["stop_reason"]; exists {
					stopReason = value
				}
				if value, exists := delta["stop_sequence"]; exists {
					stopSeq = value
				}
			}
			if update, ok := event["usage"].(map[string]any); ok {
				usage = mergeClaudeUsage(usage, update)
			}
		case "message_stop":
			hasStop = true
		case "error":
			errorObject, _ := event["error"].(map[string]any)
			detail := stringValue(errorObject["message"])
			if detail == "" {
				detail = stringValue(errorObject["type"])
			}
			if detail == "" {
				detail = "unknown upstream error"
			}
			return nil, fmt.Errorf("upstream stream error: %s", detail)
		}
	}
	if message == nil {
		return nil, fmt.Errorf("upstream stream is missing message_start")
	}
	if !hasStop {
		return nil, fmt.Errorf("upstream stream ended before message_stop")
	}
	sort.Ints(blockOrder)
	content := make([]any, 0, len(blockOrder))
	for _, index := range blockOrder {
		block := blocks[index]
		if builder := partials[index]; builder != nil && builder.Len() > 0 {
			input, errDecode := decodeJSONValue([]byte(builder.String()))
			if errDecode != nil {
				return nil, fmt.Errorf("upstream tool input is not valid JSON: %w", errDecode)
			}
			block["input"] = input
		}
		content = append(content, block)
	}
	message["content"] = content
	message["stop_reason"] = stopReason
	message["stop_sequence"] = stopSeq
	message["usage"] = claudeUsageObject(usage)
	return json.Marshal(message)
}

// claudeToolInput normalizes a tool_use input so an absent or null value still
// serializes as the empty object Claude clients expect.
func claudeToolInput(value any) any {
	switch typed := value.(type) {
	case nil:
		return map[string]any{}
	case map[string]any:
		return typed
	default:
		return value
	}
}

// claudeBlockForDelta rebuilds a content block when a delta arrives without its start.
func claudeBlockForDelta(delta map[string]any) map[string]any {
	switch deltaType, _ := delta["type"].(string); deltaType {
	case "text_delta":
		return map[string]any{"type": "text"}
	case "thinking_delta":
		return map[string]any{"type": "thinking"}
	case "input_json_delta":
		return map[string]any{"type": "tool_use"}
	default:
		return map[string]any{"type": "text"}
	}
}

func appendBlockString(block map[string]any, key string, value any) {
	block[key] = stringValue(block[key]) + stringValue(value)
}

// claudeUsageObject returns a Claude usage object carrying the standard counters.
func claudeUsageObject(source any) map[string]any {
	usage, _ := source.(map[string]any)
	usage = cloneJSONObject(usage)
	if _, exists := usage["input_tokens"]; !exists {
		usage["input_tokens"] = json.Number("0")
	}
	if _, exists := usage["output_tokens"]; !exists {
		usage["output_tokens"] = json.Number("0")
	}
	return usage
}

// mergeClaudeUsage overlays newer counters while keeping earlier non-zero values,
// mirroring the host's stream usage merge.
func mergeClaudeUsage(base, update map[string]any) map[string]any {
	merged := cloneJSONObject(base)
	for key, value := range update {
		existing, exists := merged[key]
		if !exists || isZeroJSONNumber(existing) {
			merged[key] = value
		}
	}
	return merged
}

func isZeroJSONNumber(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, errParse := number.Float64()
	return errParse == nil && parsed == 0
}

func cloneJSONObject(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func decodeJSONObject(raw []byte) (map[string]any, error) {
	value, errDecode := decodeJSONValue(raw)
	if errDecode != nil {
		return nil, errDecode
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return object, nil
}

func decodeJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, errDecode
	}
	return value, nil
}

func jsonIndex(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	index, errParse := number.Int64()
	if errParse != nil {
		return 0, false
	}
	return int(index), true
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func prepareAnthropicPayload(payload []byte, upstreamModel string, stream bool) ([]byte, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("request body is empty")
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return nil, fmt.Errorf("request body must be JSON: %w", errUnmarshal)
	}
	body["model"] = upstreamModel
	body["stream"] = stream
	return json.Marshal(body)
}

func upstreamHeaders(cfg pluginConfig) http.Header {
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	headers.Set("Anthropic-Version", cfg.AnthropicVersion)
	for name, value := range cfg.Headers {
		name = strings.TrimSpace(name)
		if name != "" && !forbiddenConfiguredHeader(name) {
			headers.Set(name, value)
		}
	}
	keys := allAPIKeys(cfg)
	if len(keys) == 0 {
		return headers
	}
	key := keys[(keyCursor.Add(1)-1)%uint64(len(keys))]
	switch cfg.AuthHeader {
	case "bearer":
		headers.Set("Authorization", "Bearer "+key)
	case "both":
		headers.Set("X-Api-Key", key)
		headers.Set("Authorization", "Bearer "+key)
	default:
		headers.Set("X-Api-Key", key)
	}
	return headers
}

func forbiddenConfiguredHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization", "x-api-key", "content-length", "host":
		return true
	default:
		return false
	}
}

func forwardHTTPStream(upstreamStreamID, pluginStreamID, callbackID string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			closePluginStream(pluginStreamID, fmt.Sprintf("stream forwarding panic: %v", recovered), callbackID)
		}
	}()
	for {
		raw, errRead := callHostHTTP(pluginabi.MethodHostHTTPStreamRead, struct {
			HostCallbackID string `json:"host_callback_id,omitempty"`
			StreamID       string `json:"stream_id"`
		}{HostCallbackID: callbackID, StreamID: upstreamStreamID})
		if errRead != nil {
			closePluginStream(pluginStreamID, errRead.Error(), callbackID)
			closeHostHTTPStream(upstreamStreamID, callbackID)
			return
		}
		var chunk hostHTTPStreamReadResponse
		if errUnmarshal := json.Unmarshal(raw, &chunk); errUnmarshal != nil {
			closePluginStream(pluginStreamID, errUnmarshal.Error(), callbackID)
			closeHostHTTPStream(upstreamStreamID, callbackID)
			return
		}
		if len(chunk.Payload) > 0 {
			_, errEmit := callHostHTTP(pluginabi.MethodHostStreamEmit, struct {
				HostCallbackID string `json:"host_callback_id,omitempty"`
				pluginStreamEmitRequest
			}{HostCallbackID: callbackID, pluginStreamEmitRequest: pluginStreamEmitRequest{StreamID: pluginStreamID, Payload: chunk.Payload}})
			if errEmit != nil {
				closePluginStream(pluginStreamID, errEmit.Error(), callbackID)
				closeHostHTTPStream(upstreamStreamID, callbackID)
				return
			}
		}
		if chunk.Error != "" {
			closePluginStream(pluginStreamID, chunk.Error, callbackID)
			closeHostHTTPStream(upstreamStreamID, callbackID)
			return
		}
		if chunk.Done {
			closePluginStream(pluginStreamID, "", callbackID)
			return
		}
	}
}

func closeHostHTTPStream(streamID, callbackID string) {
	_, _ = callHostHTTP(pluginabi.MethodHostHTTPStreamClose, struct {
		HostCallbackID string `json:"host_callback_id,omitempty"`
		StreamID       string `json:"stream_id"`
	}{HostCallbackID: callbackID, StreamID: streamID})
}

func closePluginStream(streamID, errorMessage, callbackID string) {
	_, _ = callHostHTTP(pluginabi.MethodHostStreamClose, struct {
		HostCallbackID string `json:"host_callback_id,omitempty"`
		pluginStreamCloseRequest
	}{HostCallbackID: callbackID, pluginStreamCloseRequest: pluginStreamCloseRequest{StreamID: streamID, Error: strings.TrimSpace(errorMessage)}})
}

func callHostHTTP(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload")
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func managementStatus(raw []byte) ([]byte, error) {
	var request rpcManagementRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	models := make([]string, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		models = append(models, publicModelID(cfg, model)+" → "+model.Name+" [stream: "+streamModeLabel(model)+"]")
	}
	sort.Strings(models)
	var body bytes.Buffer
	body.WriteString(`<!doctype html><html><head><meta charset="utf-8"><title>OpenAI via Anthropic Messages</title><style>body{font-family:system-ui,sans-serif;max-width:900px;margin:2rem auto;padding:0 1rem;color:#17202a}code,pre{background:#f4f6f7;border-radius:6px;padding:.15rem .35rem}li{margin:.4rem 0}.ok{color:#067647}.off{color:#b42318}</style></head><body><main><h1>OpenAI via Anthropic Messages</h1>`)
	stateClass := "off"
	stateText := "not ready"
	if cfg.Enabled && configRunnable(cfg) {
		stateClass = "ok"
		stateText = "ready"
	}
	body.WriteString(`<p>Status: <strong class="` + stateClass + `">` + stateText + `</strong></p>`)
	body.WriteString(`<p>Upstream route: <code>` + html.EscapeString(messagesURL(cfg.BaseAPI)) + `</code></p>`)
	body.WriteString(`<p>Configure this plugin from the CLIProxyAPI plugin settings page. Oh My Pi should use this CLIProxyAPI server as its Anthropic base URL and call <code>/v1/messages</code>.</p><h2>Models</h2><ul>`)
	for _, model := range models {
		body.WriteString(`<li><code>` + html.EscapeString(model) + `</code></li>`)
	}
	if len(models) == 0 {
		body.WriteString(`<li>No models configured.</li>`)
	}
	body.WriteString(`</ul></main></body></html>`)
	return okEnvelope(pluginapi.ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       body.Bytes(),
	})
}

func upstreamErrorEnvelope(status int, body []byte) []byte {
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = fmt.Sprintf("upstream returned HTTP %d", status)
	}
	if len(message) > 4096 {
		message = message[:4096]
	}
	return errorEnvelope("upstream_error", message, status)
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string, status int) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{
		Code:       code,
		Message:    message,
		Retryable:  status == http.StatusTooManyRequests || status >= 500,
		HTTPStatus: status,
	}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
