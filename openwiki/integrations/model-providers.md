---
type: Integration
title: Model Providers and Configuration
description: The provider-neutral llm.Provider interface, the raw-HTTP Anthropic Messages and OpenAI-compatible Chat Completions clients (retries, refusal fallbacks, effort, prompt caching), and how owcli resolves model configuration from defaults, config file, environment, and flags.
tags: [llm, anthropic, openai, ollama, configuration, retries]
verified:
  - by: openwiki/0.6.1
    at: 2026-10-01T17:37:16.322Z
sources:
  - id: openwiki-source-a8910515ddd14810ad43f5c1
    resource: repo://internal/config/config.go
  - id: openwiki-source-596f24d929bbc555ab74e86b
    resource: repo://internal/llm/anthropic.go
  - id: openwiki-source-ca43cced2e5cbe6ae402385f
    resource: repo://internal/llm/http.go
  - id: openwiki-source-552b6d83e1d1b259e32d81a4
    resource: repo://internal/llm/llm.go
  - id: openwiki-source-3345efbdfe5cc3508c2d2456
    resource: repo://internal/llm/openai.go
generated: { by: "claude-code", at: "2026-10-01T17:37:16.322Z" }
---

# Model Providers and Configuration

Generation is the only part of owcli that calls a model. The `internal/llm`
package defines a small provider-neutral interface plus two raw-HTTP
implementations; `internal/config` decides which one to use and how. Search,
read, status, and check never touch either package.

## The Provider interface

`Provider.Complete(ctx, Request) (Response, error)` performs one chat call:

- the request carries a system prompt, the conversation, tool definitions
  (name, description, JSON Schema), and `MaxTokens`;
- the response carries the assistant `Message` (text plus tool calls), a
  normalized stop reason (`end`, `tool_use`, `max_tokens`, `refusal`), an
  optional detail such as a refusal category, and token usage;
- `Name()` identifies provider and model (e.g. `anthropic/claude-opus-5-5`)
  and is recorded as `model` in `.last-update.json`.

A `Message` also carries the provider's verbatim assistant content in an
unexported field. A provider that must receive its own blocks back unchanged
resends that instead of rebuilding the turn, which keeps history append-only.
`llm.New(config)` builds the provider. It requires the API key environment
variable to be set, except for OpenAI-compatible endpoints on `localhost`,
which typically need no key.

## Shared HTTP client

Both clients use one retrying JSON POST helper:

- status 408, 409, 429, and any 5xx are retried, as are connection errors;
- up to 3 retries with exponential backoff (1s, 2s, 4s), or the
  `retry-after` header when present;
- other statuses fail immediately with a typed `*APIError` carrying the
  status, the provider's error type, and its message;
- the HTTP timeout is 10 minutes per attempt, and cancellation aborts at once.

## Anthropic Messages API

`Anthropic.Complete` posts to `/v1/messages` with `x-api-key` and
`anthropic-version: 2023-06-01`. The request body:

- leaves `thinking` unset, so the model runs with its default (always on for
  current models);
- sets `output_config.effort` from configuration (default `high`; the model's
  own default would be `medium`);
- uses `tool_choice: auto` whenever tools are present, because current models
  reject forced tool use;
- sets a top-level `cache_control: {type: ephemeral}` so the stable tools and
  system prefix is cached across the many calls of a run;
- by default adds server-side refusal fallbacks (`fallbacks: "default"` with
  the `server-side-fallback-2026-07-01` beta header), so a classifier decline
  is retried on Anthropic's recommended model inside the same call. Set
  `no_fallbacks = true` for proxies or platforms that reject the beta;
- uses `max_tokens` 16000 unless the caller overrides it.

Assistant content, including thinking blocks and their signatures, is resent
verbatim on the next turn. Tool results go back as `tool_result` blocks with
`is_error` for failed tools. A `refusal` stop surfaces as `StopRefusal`
together with the `stop_details` category, which the agent loop turns into an
error.

## OpenAI-compatible Chat Completions

`OpenAI.Complete` posts to `<base>/chat/completions` (the base URL includes
`/v1`) with an optional bearer token. That reaches OpenAI, OpenRouter, Ollama,
vLLM, and similar servers:

- the system prompt becomes a `system` message, and each tool result becomes
  a `tool` message (failures prefixed `ERROR:`);
- assistant turns always send string content, because Ollama rejects `null`;
- tool definitions use the `function` shape;
- finish reasons map `length` to `max_tokens`, `content_filter` to `refusal`,
  and `tool_calls` to `tool_use`;
- tool-call arguments that are not valid JSON are kept as
  `{"_invalid_json": ...}`, so the agent can tell the model what went wrong.

## Configuration

`config.Load` merges, from lowest to highest precedence: built-in defaults,
`$XDG_CONFIG_HOME/owcli/config.toml`, `OWCLI_*` environment variables, and
command-line flags. Provider-specific defaults then fill whatever is still
empty, and `Validate` checks the result.

| Key | Flag | Env | Default |
| --- | --- | --- | --- |
| `provider` | `--provider` | `OWCLI_PROVIDER` | `anthropic` (`openai` = any compatible endpoint) |
| `model` | `--model` | `OWCLI_MODEL` | `claude-opus-5-5` for Anthropic; none for OpenAI-compatible |
| `base_url` | `--base-url` | `OWCLI_BASE_URL` | `https://api.anthropic.com` / `https://api.openai.com/v1` |
| `api_key_env` | `--api-key-env` | `OWCLI_API_KEY_ENV` | `ANTHROPIC_API_KEY` / `OPENAI_API_KEY` |
| `effort` | `--effort` | `OWCLI_EFFORT` | `high` for Anthropic |
| `no_fallbacks` | — | — | `false` |

The config file rejects unknown keys, so a typo like `modle` is an error
rather than silently ignored. For a local model:

```sh
owcli init --provider openai --base-url http://localhost:11434/v1 --model qwen3:14b
```

How the agent loop uses these providers is described in
[Generation Run Lifecycle](../workflows/generation-run.md).
