package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
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

func TestUpstreamStreamModeFollowsPinThenClient(t *testing.T) {
	pinned := true
	unpinned := false
	tests := []struct {
		name         string
		model        modelConfig
		clientStream bool
		want         bool
	}{
		{name: "unset follows streaming client", model: modelConfig{}, clientStream: true, want: true},
		{name: "unset follows non-streaming client", model: modelConfig{}, clientStream: false, want: false},
		{name: "pinned true beats non-streaming client", model: modelConfig{Stream: &pinned}, clientStream: false, want: true},
		{name: "pinned true with streaming client", model: modelConfig{Stream: &pinned}, clientStream: true, want: true},
		{name: "pinned false beats streaming client", model: modelConfig{Stream: &unpinned}, clientStream: true, want: false},
		{name: "pinned false with non-streaming client", model: modelConfig{Stream: &unpinned}, clientStream: false, want: false},
	}
	for _, test := range tests {
		if got := upstreamStreamMode(test.model, test.clientStream); got != test.want {
			t.Fatalf("%s: upstreamStreamMode = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestModelStreamPinResolvesThroughConfig(t *testing.T) {
	pinned := true
	unpinned := false
	cfg := pluginConfig{
		Prefix: "omp",
		Models: []modelConfig{
			{Name: "gpt-5.4", Alias: "pro", Stream: &pinned},
			{Name: "gpt-5.4-mini", Alias: "mini", Stream: &unpinned},
			{Name: "gpt-5.4-nano", Alias: "nano"},
		},
	}
	normalizeConfig(&cfg)
	tests := []struct {
		requested string
		client    bool
		want      bool
		label     string
	}{
		{requested: "omp/pro", client: false, want: true, label: "always"},
		{requested: "omp/pro(high)", client: true, want: true, label: "always"},
		{requested: "omp/mini", client: true, want: false, label: "never"},
		{requested: "omp/nano", client: true, want: true, label: "client"},
		{requested: "omp/nano", client: false, want: false, label: "client"},
	}
	for _, test := range tests {
		model, ok := resolveModelConfig(cfg, test.requested)
		if !ok {
			t.Fatalf("resolveModelConfig(%q) not found", test.requested)
		}
		if got := upstreamStreamMode(model, test.client); got != test.want {
			t.Fatalf("upstreamStreamMode(%q, %v) = %v, want %v", test.requested, test.client, got, test.want)
		}
		if got := streamModeLabel(model); got != test.label {
			t.Fatalf("streamModeLabel(%q) = %q, want %q", test.requested, got, test.label)
		}
	}
	if _, ok := resolveModelConfig(cfg, "omp/unknown"); ok {
		t.Fatalf("resolveModelConfig accepted an unconfigured model")
	}
}

// parseClaudeStream decodes synthesized SSE frames back into event/type pairs.
func parseClaudeStream(t *testing.T, frames [][]byte) []map[string]any {
	t.Helper()
	events := make([]map[string]any, 0, len(frames))
	for index, frame := range frames {
		text := string(frame)
		if !strings.HasPrefix(text, "event: ") || !strings.HasSuffix(text, "\n\n") {
			t.Fatalf("frame %d is not a well formed SSE event: %q", index, text)
		}
		lines := strings.SplitN(strings.TrimSuffix(text, "\n\n"), "\n", 2)
		if len(lines) != 2 || !strings.HasPrefix(lines[1], "data: ") {
			t.Fatalf("frame %d has no data line: %q", index, text)
		}
		var event map[string]any
		if errUnmarshal := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &event); errUnmarshal != nil {
			t.Fatalf("frame %d payload is not JSON: %v", index, errUnmarshal)
		}
		if event["type"] != strings.TrimPrefix(lines[0], "event: ") {
			t.Fatalf("frame %d event name %q disagrees with payload type %v", index, lines[0], event["type"])
		}
		events = append(events, event)
	}
	return events
}

func TestClaudeResponseToSSEFramesEmitsClaudeEventSequence(t *testing.T) {
	body := []byte(`{"id":"msg_1","type":"message","role":"assistant","model":"gpt-5.4",` +
		`"content":[{"type":"thinking","thinking":"weigh options","signature":"sig-1"},` +
		`{"type":"text","text":"hello"},` +
		`{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"q":"x"}}],` +
		`"stop_reason":"tool_use","stop_sequence":null,` +
		`"usage":{"input_tokens":11,"output_tokens":7}}`)
	frames, errConvert := claudeResponseToSSEFrames(body, "gpt-5.4")
	if errConvert != nil {
		t.Fatalf("claudeResponseToSSEFrames error: %v", errConvert)
	}
	events := parseClaudeStream(t, frames)
	wantTypes := []string{
		"message_start",
		"content_block_start", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop",
		"message_delta",
		"message_stop",
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d", len(events), len(wantTypes))
	}
	for index, wantType := range wantTypes {
		if events[index]["type"] != wantType {
			t.Fatalf("event %d type = %v, want %q", index, events[index]["type"], wantType)
		}
	}

	started, _ := events[0]["message"].(map[string]any)
	if started["id"] != "msg_1" || started["model"] != "gpt-5.4" {
		t.Fatalf("message_start message = %s", mustJSON(t, started))
	}
	if content, _ := started["content"].([]any); len(content) != 0 {
		t.Fatalf("message_start content = %s, want empty", mustJSON(t, started["content"]))
	}
	if started["stop_reason"] != nil || started["stop_sequence"] != nil {
		t.Fatalf("message_start must not report a stop reason: %s", mustJSON(t, started))
	}
	startedUsage, _ := started["usage"].(map[string]any)
	if got, want := startedUsage["input_tokens"], float64(11); got != want {
		t.Fatalf("message_start usage.input_tokens = %v, want %v", got, want)
	}

	thinkingStart, _ := events[1]["content_block"].(map[string]any)
	if thinkingStart["type"] != "thinking" || thinkingStart["thinking"] != "" {
		t.Fatalf("thinking content_block_start = %s", mustJSON(t, thinkingStart))
	}
	thinkingDelta, _ := events[2]["delta"].(map[string]any)
	if thinkingDelta["type"] != "thinking_delta" || thinkingDelta["thinking"] != "weigh options" {
		t.Fatalf("thinking delta = %s", mustJSON(t, thinkingDelta))
	}
	signatureDelta, _ := events[3]["delta"].(map[string]any)
	if signatureDelta["type"] != "signature_delta" || signatureDelta["signature"] != "sig-1" {
		t.Fatalf("signature delta = %s", mustJSON(t, signatureDelta))
	}

	textStart, _ := events[5]["content_block"].(map[string]any)
	if textStart["type"] != "text" || textStart["text"] != "" {
		t.Fatalf("text content_block_start = %s", mustJSON(t, textStart))
	}
	textDelta, _ := events[6]["delta"].(map[string]any)
	if textDelta["type"] != "text_delta" || textDelta["text"] != "hello" {
		t.Fatalf("text delta = %s", mustJSON(t, textDelta))
	}

	toolStart, _ := events[8]["content_block"].(map[string]any)
	if toolStart["type"] != "tool_use" || toolStart["id"] != "toolu_1" || toolStart["name"] != "lookup" {
		t.Fatalf("tool_use content_block_start = %s", mustJSON(t, toolStart))
	}
	if input, _ := toolStart["input"].(map[string]any); len(input) != 0 {
		t.Fatalf("tool_use content_block_start input = %s, want empty object", mustJSON(t, toolStart["input"]))
	}
	toolDelta, _ := events[9]["delta"].(map[string]any)
	partial, _ := toolDelta["partial_json"].(string)
	var toolInput map[string]any
	if errUnmarshal := json.Unmarshal([]byte(partial), &toolInput); errUnmarshal != nil {
		t.Fatalf("input_json_delta partial_json %q is not JSON: %v", partial, errUnmarshal)
	}
	if got, want := toolInput["q"], "x"; got != want {
		t.Fatalf("tool input = %v, want %v", got, want)
	}

	messageDelta, _ := events[11]["delta"].(map[string]any)
	if messageDelta["stop_reason"] != "tool_use" {
		t.Fatalf("message_delta stop_reason = %v, want tool_use", messageDelta["stop_reason"])
	}
	if _, exists := messageDelta["stop_sequence"]; !exists {
		t.Fatalf("message_delta delta has no stop_sequence key: %s", mustJSON(t, messageDelta))
	}
	deltaUsage, _ := events[11]["usage"].(map[string]any)
	if got, want := deltaUsage["output_tokens"], float64(7); got != want {
		t.Fatalf("message_delta usage.output_tokens = %v, want %v", got, want)
	}
}

func TestClaudeStreamToResponseRejectsIncompleteStreams(t *testing.T) {
	truncated := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_3\",\"model\":\"gpt-5.4\",\"content\":[]}}\n\n")
	if _, errAggregate := claudeStreamToResponse(truncated); errAggregate == nil {
		t.Fatalf("claudeStreamToResponse accepted a stream that never reached message_stop")
	}
	withError := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_4\",\"model\":\"gpt-5.4\",\"content\":[]}}\n\n" +
		"event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"overloaded\"}}\n\n")
	if _, errAggregate := claudeStreamToResponse(withError); errAggregate == nil {
		t.Fatalf("claudeStreamToResponse accepted an error event")
	}
	if _, errAggregate := claudeStreamToResponse([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")); errAggregate == nil {
		t.Fatalf("claudeStreamToResponse accepted a stream with no message_start")
	}
}

func TestClaudeStreamToResponseAggregatesUpstreamStream(t *testing.T) {
	stream := []byte(
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_5\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"gpt-5.4\",\"content\":[],\"usage\":{\"input_tokens\":17,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hel\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_3\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"q\\\":\\\"z\\\"}\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":null},\"usage\":{\"input_tokens\":0,\"output_tokens\":23}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	aggregated, errAggregate := claudeStreamToResponse(stream)
	if errAggregate != nil {
		t.Fatalf("claudeStreamToResponse error: %v", errAggregate)
	}
	var body map[string]any
	if errUnmarshal := json.Unmarshal(aggregated, &body); errUnmarshal != nil {
		t.Fatalf("decode aggregated body: %v", errUnmarshal)
	}
	if body["id"] != "msg_5" || body["type"] != "message" || body["role"] != "assistant" || body["model"] != "gpt-5.4" {
		t.Fatalf("aggregated envelope = %s", mustJSON(t, body))
	}
	if body["stop_reason"] != "tool_use" {
		t.Fatalf("stop_reason = %v, want tool_use", body["stop_reason"])
	}
	content, _ := body["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %s, want 2 blocks", mustJSON(t, content))
	}
	textBlock, _ := content[0].(map[string]any)
	if textBlock["text"] != "hello" {
		t.Fatalf("text block = %s", mustJSON(t, textBlock))
	}
	toolBlock, _ := content[1].(map[string]any)
	if toolBlock["type"] != "tool_use" || toolBlock["name"] != "lookup" {
		t.Fatalf("tool block = %s", mustJSON(t, toolBlock))
	}
	toolInput, _ := toolBlock["input"].(map[string]any)
	if got, want := toolInput["q"], "z"; got != want {
		t.Fatalf("tool input = %v, want %v", got, want)
	}
	usage, _ := body["usage"].(map[string]any)
	if got, want := usage["input_tokens"], float64(17); got != want {
		t.Fatalf("usage.input_tokens = %v, want %v", got, want)
	}
	if got, want := usage["output_tokens"], float64(23); got != want {
		t.Fatalf("usage.output_tokens = %v, want %v", got, want)
	}
}

func TestClaudeResponseToSSEFramesNormalizesEmptyToolInput(t *testing.T) {
	body := []byte(`{"id":"msg_6","type":"message","role":"assistant","model":"gpt-5.4",` +
		`"content":[{"type":"tool_use","id":"toolu_6","name":"ping","input":null}],` +
		`"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
	frames, errConvert := claudeResponseToSSEFrames(body, "gpt-5.4")
	if errConvert != nil {
		t.Fatalf("claudeResponseToSSEFrames error: %v", errConvert)
	}
	events := parseClaudeStream(t, frames)
	if len(events) != 5 {
		t.Fatalf("event count = %d, want 5 (no input_json_delta for an empty input)", len(events))
	}
	toolStart, _ := events[1]["content_block"].(map[string]any)
	if input, ok := toolStart["input"].(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("tool_use content_block_start input = %s, want empty object", mustJSON(t, toolStart["input"]))
	}
	aggregated, errAggregate := claudeStreamToResponse(bytes.Join(frames, nil))
	if errAggregate != nil {
		t.Fatalf("claudeStreamToResponse error: %v", errAggregate)
	}
	var roundTripped map[string]any
	if errUnmarshal := json.Unmarshal(aggregated, &roundTripped); errUnmarshal != nil {
		t.Fatalf("decode aggregated body: %v", errUnmarshal)
	}
	content, _ := roundTripped["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %s, want 1 block", mustJSON(t, content))
	}
	block, _ := content[0].(map[string]any)
	if input, ok := block["input"].(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("round-tripped input = %s, want empty object", mustJSON(t, block["input"]))
	}
}

func TestClaudeStreamRoundTripPreservesMessage(t *testing.T) {
	body := []byte(`{"id":"msg_2","type":"message","role":"assistant","model":"gpt-5.4",` +
		`"content":[{"type":"text","text":"alpha"},` +
		`{"type":"tool_use","id":"toolu_2","name":"lookup","input":{"q":"y","limit":2}},` +
		`{"type":"text","text":"omega"}],` +
		`"stop_reason":"end_turn","stop_sequence":null,` +
		`"usage":{"input_tokens":3,"output_tokens":9,"cache_read_input_tokens":5}}`)
	frames, errConvert := claudeResponseToSSEFrames(body, "gpt-5.4")
	if errConvert != nil {
		t.Fatalf("claudeResponseToSSEFrames error: %v", errConvert)
	}
	aggregated, errAggregate := claudeStreamToResponse(bytes.Join(frames, nil))
	if errAggregate != nil {
		t.Fatalf("claudeStreamToResponse error: %v", errAggregate)
	}
	var got, want map[string]any
	if errUnmarshal := json.Unmarshal(aggregated, &got); errUnmarshal != nil {
		t.Fatalf("decode aggregated body: %v", errUnmarshal)
	}
	if errUnmarshal := json.Unmarshal(body, &want); errUnmarshal != nil {
		t.Fatalf("decode source body: %v", errUnmarshal)
	}
	if !reflect.DeepEqual(got["content"], want["content"]) {
		t.Fatalf("content = %s, want %s", mustJSON(t, got["content"]), mustJSON(t, want["content"]))
	}
	for _, key := range []string{"id", "type", "role", "model", "stop_reason", "stop_sequence"} {
		if !reflect.DeepEqual(got[key], want[key]) {
			t.Fatalf("%s = %v, want %v", key, got[key], want[key])
		}
	}
	gotUsage, _ := got["usage"].(map[string]any)
	wantUsage, _ := want["usage"].(map[string]any)
	for _, key := range []string{"input_tokens", "output_tokens", "cache_read_input_tokens"} {
		if !reflect.DeepEqual(gotUsage[key], wantUsage[key]) {
			t.Fatalf("usage.%s = %v, want %v", key, gotUsage[key], wantUsage[key])
		}
	}
}
