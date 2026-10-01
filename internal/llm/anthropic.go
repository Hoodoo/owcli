package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	anthropicVersion  = "2023-06-01"
	fallbackBeta      = "server-side-fallback-2026-07-01"
	defaultMaxTokens  = 16000
	anthropicProvider = "anthropic"
)

// Anthropic calls the Messages API.
//
// Thinking is left at the model default (always on for current models) and
// never disabled. Assistant content, including thinking blocks, is resent
// verbatim so history stays append-only. Only tool_choice auto is used,
// since current models reject forced tool use. The stable tools+system
// prefix is cached with top-level cache_control.
type Anthropic struct {
	baseURL, key, model, effort string
	fallbacks                   bool
	c                           *client
}

// NewAnthropic builds a Messages API provider. effort may be empty;
// fallbacks enables server-side refusal fallbacks (Claude API only).
func NewAnthropic(baseURL, key, model, effort string, fallbacks bool) *Anthropic {
	return &Anthropic{baseURL: strings.TrimRight(baseURL, "/"), key: key, model: model, effort: effort, fallbacks: fallbacks, c: newClient(parseAnthropicError)}
}

// Name implements Provider.
func (a *Anthropic) Name() string { return anthropicProvider + "/" + a.model }

type aBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type aMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type aTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type aRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       string            `json:"system,omitempty"`
	Messages     []aMessage        `json:"messages"`
	Tools        []aTool           `json:"tools,omitempty"`
	ToolChoice   map[string]string `json:"tool_choice,omitempty"`
	CacheControl map[string]string `json:"cache_control"`
	OutputConfig map[string]string `json:"output_config,omitempty"`
	Fallbacks    string            `json:"fallbacks,omitempty"`
}

type aResponse struct {
	Content     json.RawMessage `json:"content"`
	StopReason  string          `json:"stop_reason"`
	StopDetails *struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Complete implements Provider.
func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	body := aRequest{
		Model:        a.model,
		MaxTokens:    req.MaxTokens,
		System:       req.System,
		CacheControl: map[string]string{"type": "ephemeral"},
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = defaultMaxTokens
	}
	if a.effort != "" {
		body.OutputConfig = map[string]string{"effort": a.effort}
	}
	headers := map[string]string{"x-api-key": a.key, "anthropic-version": anthropicVersion}
	if a.fallbacks {
		body.Fallbacks = "default"
		headers["anthropic-beta"] = fallbackBeta
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, aTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema})
	}
	if len(body.Tools) > 0 {
		body.ToolChoice = map[string]string{"type": "auto"}
	}
	for _, m := range req.Messages {
		content, err := anthropicContent(m)
		if err != nil {
			return Response{}, err
		}
		body.Messages = append(body.Messages, aMessage{Role: string(m.Role), Content: content})
	}

	var out aResponse
	if err := a.c.post(ctx, a.baseURL+"/v1/messages", headers, body, &out); err != nil {
		return Response{}, err
	}
	var blocks []aBlock
	if err := json.Unmarshal(out.Content, &blocks); err != nil {
		return Response{}, fmt.Errorf("decode content: %w", err)
	}
	msg := Message{Role: Assistant, raw: out.Content}
	var texts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			texts = append(texts, b.Text)
		case "tool_use":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	msg.Text = strings.Join(texts, "\n")
	resp := Response{Message: msg, Usage: Usage{
		InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
		CacheReadTokens: out.Usage.CacheReadInputTokens, CacheWriteTokens: out.Usage.CacheCreationInputTokens,
	}}
	switch out.StopReason {
	case "tool_use":
		resp.Stop = StopToolUse
	case "max_tokens":
		resp.Stop = StopMaxTokens
	case "refusal":
		resp.Stop = StopRefusal
		if d := out.StopDetails; d != nil {
			resp.Detail = strings.TrimSpace(d.Category + " " + d.Explanation)
		}
	default:
		resp.Stop = StopEnd
	}
	if resp.Stop == StopEnd && len(msg.ToolCalls) > 0 {
		resp.Stop = StopToolUse
	}
	return resp, nil
}

// anthropicContent renders one message's content blocks.
func anthropicContent(m Message) (json.RawMessage, error) {
	if m.Role == Assistant && m.raw != nil {
		return m.raw, nil
	}
	var blocks []aBlock
	for _, r := range m.ToolResults {
		blocks = append(blocks, aBlock{Type: "tool_result", ToolUseID: r.CallID, Content: r.Content, IsError: r.IsError})
	}
	if m.Text != "" {
		blocks = append(blocks, aBlock{Type: "text", Text: m.Text})
	}
	for _, c := range m.ToolCalls {
		input := c.Input
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		blocks = append(blocks, aBlock{Type: "tool_use", ID: c.ID, Name: c.Name, Input: input})
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("empty %s message", m.Role)
	}
	return json.Marshal(blocks)
}

func parseAnthropicError(status int, body []byte) *APIError {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return &APIError{Status: status, Type: e.Error.Type, Message: e.Error.Message}
	}
	return &APIError{Status: status, Message: truncate(string(body), 500)}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
