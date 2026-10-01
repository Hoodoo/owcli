// Package llm is a minimal, provider-neutral chat interface with tool calling,
// plus raw-HTTP implementations for the Anthropic Messages API and
// OpenAI-compatible Chat Completions endpoints.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"owcli/internal/config"
)

// Role of a message.
type Role string

// Roles.
const (
	User      Role = "user"
	Assistant Role = "assistant"
)

// ToolDef describes a tool the model may call.
type ToolDef struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema of the input object
}

// ToolCall is one tool invocation requested by the model.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// ToolResult answers one ToolCall.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// Message is one conversation turn. A user turn carries Text and/or
// ToolResults; an assistant turn carries Text and/or ToolCalls.
type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
	// raw is the provider's verbatim assistant content. Providers that must
	// receive their own blocks back unchanged (Anthropic thinking blocks)
	// resend it instead of rebuilding the turn, keeping history append-only.
	raw json.RawMessage
}

// StopReason says why the model stopped.
type StopReason string

// Normalized stop reasons.
const (
	StopEnd       StopReason = "end"        // finished its turn
	StopToolUse   StopReason = "tool_use"   // waiting for tool results
	StopMaxTokens StopReason = "max_tokens" // output was truncated
	StopRefusal   StopReason = "refusal"    // declined by the model or a safety classifier
)

// Usage reports token counts.
type Usage struct {
	InputTokens, OutputTokens, CacheReadTokens, CacheWriteTokens int
}

// Request is one model call.
type Request struct {
	System    string
	Messages  []Message
	Tools     []ToolDef
	MaxTokens int
}

// Response is the model's assistant turn.
type Response struct {
	Message Message
	Stop    StopReason
	Detail  string // provider detail, e.g. a refusal category
	Usage   Usage
}

// Provider runs one model call.
type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
	// Name identifies the provider and model, e.g. "anthropic/claude-opus-5-5".
	Name() string
}

// ErrNoAPIKey reports a missing credential.
var ErrNoAPIKey = errors.New("no API key")

// New builds the provider a resolved config names.
func New(c config.Config) (Provider, error) {
	key := os.Getenv(c.APIKeyEnv)
	if key == "" && !(c.Provider == config.ProviderOpenAI && isLocal(c.BaseURL)) {
		return nil, fmt.Errorf("%w: set %s", ErrNoAPIKey, c.APIKeyEnv)
	}
	switch c.Provider {
	case config.ProviderAnthropic:
		return NewAnthropic(c.BaseURL, key, c.Model, c.Effort, !c.NoFallbacks), nil
	case config.ProviderOpenAI:
		return NewOpenAI(c.BaseURL, key, c.Model), nil
	}
	return nil, fmt.Errorf("unknown provider %q", c.Provider)
}

// isLocal reports endpoints that usually need no key (Ollama, vLLM, ...).
func isLocal(baseURL string) bool {
	for _, h := range []string{"://localhost", "://127.0.0.1", "://[::1]"} {
		if strings.Contains(baseURL, h) {
			return true
		}
	}
	return false
}
