# OpenAI Models via Anthropic Messages Plugin

This plugin lets CLIProxyAPI expose user-configured OpenAI model names in the global `/v1/models` catalog while sending their traffic to an upstream **Anthropic Messages** endpoint. It is designed for Oh My Pi and other Anthropic clients, and follows the cc-switch route shape:

```text
configured base_api + /v1 + /messages
```

If `base_api` already ends in `/v1`, the plugin does not add it again.

## Features

- Configurable from CLIProxyAPI's plugin page (`ConfigFields` metadata is included).
- Adds configured models to CLIProxyAPI's unified model catalog.
- Optional public prefix such as `omp/gpt-5.4`.
- Maps a public alias back to the upstream OpenAI model name.
- Accepts Anthropic `/v1/messages`; CLIProxyAPI can also translate OpenAI Chat Completions/Responses requests to Anthropic Messages before execution.
- Supports streaming and non-streaming requests.
- Supports one key or round-robin across multiple keys.
- Adds `Anthropic-Version` and uses `x-api-key`, Bearer, or both.

## Build

This repository is the standalone plugin module. Its Go module depends on the
published CLIProxyAPI v7 SDK (`github.com/router-for-me/CLIProxyAPI/v7`), so it
builds without the CLIProxyAPI source tree.

From the repository root:

```powershell
cd go
go build -buildmode=c-shared -o ../bin/openai-anthropic-messages.dll .
```

Linux/macOS users build with the matching `GOOS`/`GOARCH` and `.so`/`.dylib`
output. Pushing a `v*` tag (for example `v0.1.0`) triggers the `release-linux`
workflow, which builds the Linux `.so` archives for amd64 and arm64 and
attaches them to the GitHub release for that tag. The output basename must
remain `openai-anthropic-messages` so it matches the config key.

## Configure

Copy the dynamic library into the configured `plugins.dir` (default `plugins`) and configure:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    openai-anthropic-messages:
      enabled: true
      priority: 100
      base_api: "https://your-gateway.example.com/api"
      api_key: "sk-upstream"
      # api_keys: ["sk-1", "sk-2"]
      auth_header: "x-api-key" # x-api-key, bearer, or both
      anthropic_version: "2023-06-01"
      prefix: "omp"            # optional; empty exposes aliases without a prefix
      models:
        - name: "gpt-5.4"       # upstream model sent in the Messages request
          alias: "gpt-pro"      # optional public name
          display_name: "GPT Pro via Anthropic"
          context_length: 200000
          max_output_tokens: 32000
      headers: {}
```

With this example:

- Public model: `omp/gpt-pro`
- Upstream URL: `https://your-gateway.example.com/api/v1/messages`
- Upstream request model: `gpt-5.4`

You can edit the same values through the CLIProxyAPI management page. The plugin configuration APIs are also available at:

```text
GET /v0/management/plugins/openai-anthropic-messages/config
PUT /v0/management/plugins/openai-anthropic-messages/config
```

The browser status page is:

```text
/v0/resource/plugins/openai-anthropic-messages/status
```

## Oh My Pi

Configure Oh My Pi as an Anthropic client pointing to CLIProxyAPI, not to the upstream gateway:

```text
ANTHROPIC_BASE_URL=http://127.0.0.1:8317
ANTHROPIC_API_KEY=<a CLIProxyAPI client API key>
model=omp/gpt-pro
```

Oh My Pi then calls CLIProxyAPI's `/v1/messages`. CLIProxyAPI routes only the configured model IDs to this plugin, translates to Anthropic Messages when needed, and the plugin forwards to the configured upstream `/v1/messages` endpoint.

> Plugin executor routes are intentionally unavailable when CLIProxyAPI Home mode is enabled; use standalone/local CLIProxyAPI mode for this plugin.
