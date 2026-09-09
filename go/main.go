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
			Version:          "0.2.0",
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
				{Name: "models", Type: pluginapi.ConfigFieldTypeArray, Description: "Models: [{name, alias, display_name, context_length, max_output_tokens}]. name is sent upstream; alias is exposed publicly."},
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

func resolveUpstreamModel(cfg pluginConfig, requested string) (string, bool) {
	requested = modelWithoutThinkingSuffix(requested)
	for _, model := range cfg.Models {
		if requested == publicModelID(cfg, model) {
			return model.Name, true
		}
	}
	return "", false
}

func execute(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	upstreamModel, ok := resolveUpstreamModel(cfg, request.Model)
	if !ok || !configRunnable(cfg) {
		return errorEnvelope("model_not_configured", "model is not configured for this plugin", http.StatusBadRequest), nil
	}
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
	return okEnvelope(pluginapi.ExecutorResponse{Payload: upstream.Body, Headers: upstream.Headers})
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
	upstreamModel, ok := resolveUpstreamModel(cfg, request.Model)
	if !ok || !configRunnable(cfg) {
		return errorEnvelope("model_not_configured", "model is not configured for this plugin", http.StatusBadRequest), nil
	}
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
	if upstream.StatusCode < 200 || upstream.StatusCode >= 300 {
		return errorEnvelope("upstream_error", fmt.Sprintf("upstream returned HTTP %d", upstream.StatusCode), upstream.StatusCode), nil
	}
	if strings.TrimSpace(upstream.StreamID) == "" {
		return errorEnvelope("stream_unavailable", "upstream returned no stream id", http.StatusBadGateway), nil
	}
	go forwardHTTPStream(upstream.StreamID, request.StreamID, request.HostCallbackID)
	return okEnvelope(map[string]any{"headers": upstream.Headers})
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
		models = append(models, publicModelID(cfg, model)+" → "+model.Name)
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
