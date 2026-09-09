package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestNormalizeBaseAPIAddsV1Once(t *testing.T) {
	tests := map[string]string{
		"https://gateway.example.com":         "https://gateway.example.com/v1",
		"https://gateway.example.com/":        "https://gateway.example.com/v1",
		"https://gateway.example.com/api":     "https://gateway.example.com/api/v1",
		"https://gateway.example.com/api/v1":  "https://gateway.example.com/api/v1",
		"https://gateway.example.com/api/v1/": "https://gateway.example.com/api/v1",
	}
	for input, want := range tests {
		if got := normalizeBaseAPI(input); got != want {
			t.Fatalf("normalizeBaseAPI(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestConfiguredModelRoutingAndPrefix(t *testing.T) {
	cfg := pluginConfig{
		Enabled: true,
		BaseAPI: "https://gateway.example.com",
		APIKey:  "secret",
		Prefix:  "omp",
		Models:  []modelConfig{{Name: "gpt-5.4", Alias: "fast"}},
	}
	normalizeConfig(&cfg)
	currentConfig.Store(cfg)

	if got, want := messagesURL(cfg.BaseAPI), "https://gateway.example.com/v1/messages"; got != want {
		t.Fatalf("messagesURL = %q, want %q", got, want)
	}
	if got, ok := resolveUpstreamModel(cfg, "omp/fast(high)"); !ok || got != "gpt-5.4" {
		t.Fatalf("resolveUpstreamModel = %q, %v; want gpt-5.4, true", got, ok)
	}

	raw, errRoute := routeModel(mustJSON(t, rpcModelRouteRequest{ModelRouteRequest: pluginapi.ModelRouteRequest{
		SourceFormat:   "claude",
		RequestedModel: "omp/fast",
	}}))
	if errRoute != nil {
		t.Fatalf("routeModel error: %v", errRoute)
	}
	var env envelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal != nil {
		t.Fatalf("decode route envelope: %v", errUnmarshal)
	}
	var response pluginapi.ModelRouteResponse
	if errUnmarshal := json.Unmarshal(env.Result, &response); errUnmarshal != nil {
		t.Fatalf("decode route result: %v", errUnmarshal)
	}
	if !response.Handled || response.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("route response = %#v, want handled self", response)
	}
}

func TestNormalizeConfigDeduplicatesAPIKeys(t *testing.T) {
	cfg := pluginConfig{APIKey: "primary", APIKeys: []string{"secondary", "primary", "secondary"}}
	normalizeConfig(&cfg)
	if got, want := cfg.APIKeys, []string{"primary", "secondary"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("APIKeys = %#v, want %#v", got, want)
	}
}

func TestConfigWithoutAPIKeyStillRegistersModels(t *testing.T) {
	cfg := pluginConfig{
		Enabled: true,
		BaseAPI: "http://127.0.0.1:9000",
		Models:  []modelConfig{{Name: "gpt-local"}},
	}
	normalizeConfig(&cfg)
	if !configRunnable(cfg) {
		t.Fatal("configRunnable() = false, want true for an unauthenticated upstream")
	}
	currentConfig.Store(cfg)
	if got := len(staticModels().Models); got != 1 {
		t.Fatalf("static model count = %d, want 1", got)
	}
}

func TestSupportedSourceFormatIncludesOpenAIResponses(t *testing.T) {
	if !supportedSourceFormat("openai-response") {
		t.Fatal("openai-response should be supported")
	}
}

func TestStaticModelsExposeOpenAICatalogEntries(t *testing.T) {
	cfg := pluginConfig{
		Enabled: true,
		BaseAPI: "https://gateway.example.com/v1",
		APIKey:  "secret",
		Prefix:  "omp",
		Models: []modelConfig{{
			Name:            "gpt-5.4",
			Alias:           "pro",
			DisplayName:     "GPT Pro",
			ContextLength:   200000,
			MaxOutputTokens: 32000,
		}},
	}
	normalizeConfig(&cfg)
	currentConfig.Store(cfg)

	response := staticModels()
	if len(response.Models) != 1 {
		t.Fatalf("model count = %d, want 1", len(response.Models))
	}
	model := response.Models[0]
	if model.ID != "omp/pro" || model.Name != "gpt-5.4" || model.Type != "openai" || model.OwnedBy != "openai" {
		t.Fatalf("model = %#v", model)
	}
}

func TestPrepareAnthropicPayloadUsesUpstreamName(t *testing.T) {
	payload, errPrepare := prepareAnthropicPayload([]byte(`{"model":"omp/pro","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`), "gpt-5.4", true)
	if errPrepare != nil {
		t.Fatalf("prepareAnthropicPayload error: %v", errPrepare)
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		t.Fatalf("decode payload: %v", errUnmarshal)
	}
	if body["model"] != "gpt-5.4" || body["stream"] != true {
		t.Fatalf("payload = %#v", body)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("marshal test value: %v", errMarshal)
	}
	return raw
}
