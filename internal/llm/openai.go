package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// OpenAI calls an OpenAI-compatible Chat Completions endpoint (OpenAI,
// OpenRouter, Ollama, vLLM, ...). The base URL includes the version prefix,
// e.g. https://api.openai.com/v1 or http://localhost:11434/v1.
type OpenAI struct {
	baseURL, key, model string
	c                   *client
}

// NewOpenAI builds a Chat Completions provider. key may be empty for local
// servers.
func NewOpenAI(baseURL, key, model string) *OpenAI {
	return &OpenAI{baseURL: strings.TrimRight(baseURL, "/"), key: key, model: model, c: newClient(parseOpenAIError)}
}

// Name implements Provider.
func (o *OpenAI) Name() string { return "openai/" + o.model }

type oToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oMessage struct {
	Role       string      `json:"role"`
	Content    *string     `json:"content"`
	ToolCalls  []oToolCall `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type oTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type oRequest struct {
	Model     string     `json:"model"`
	Messages  []oMessage `json:"messages"`
	Tools     []oTool    `json:"tools,omitempty"`
	MaxTokens int        `json:"max_tokens,omitempty"`
}

type oResponse struct {
	Choices []struct {
		Message      oMessage `json:"message"`
		FinishReason string   `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func strPtr(s string) *string { return &s }

// Complete implements Provider.
func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	body := oRequest{Model: o.model, MaxTokens: req.MaxTokens}
	if req.System != "" {
		body.Messages = append(body.Messages, oMessage{Role: "system", Content: strPtr(req.System)})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case User:
			for _, r := range m.ToolResults {
				content := r.Content
				if r.IsError {
					content = "ERROR: " + content
				}
				body.Messages = append(body.Messages, oMessage{Role: "tool", ToolCallID: r.CallID, Content: strPtr(content)})
			}
			if m.Text != "" {
				body.Messages = append(body.Messages, oMessage{Role: "user", Content: strPtr(m.Text)})
			}
		case Assistant:
			om := oMessage{Role: "assistant"}
			if m.Text != "" {
				om.Content = strPtr(m.Text)
			}
			for _, c := range m.ToolCalls {
				tc := oToolCall{ID: c.ID, Type: "function"}
				tc.Function.Name = c.Name
				tc.Function.Arguments = string(c.Input)
				if tc.Function.Arguments == "" {
					tc.Function.Arguments = "{}"
				}
				om.ToolCalls = append(om.ToolCalls, tc)
			}
			body.Messages = append(body.Messages, om)
		}
	}
	for _, t := range req.Tools {
		ot := oTool{Type: "function"}
		ot.Function.Name, ot.Function.Description, ot.Function.Parameters = t.Name, t.Description, t.Schema
		body.Tools = append(body.Tools, ot)
	}
	headers := map[string]string{}
	if o.key != "" {
		headers["authorization"] = "Bearer " + o.key
	}
	var out oResponse
	if err := o.c.post(ctx, o.baseURL+"/chat/completions", headers, body, &out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("response has no choices")
	}
	ch := out.Choices[0]
	msg := Message{Role: Assistant}
	if ch.Message.Content != nil {
		msg.Text = *ch.Message.Content
	}
	for _, tc := range ch.Message.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if !json.Valid(input) {
			// Keep the call so the agent can report the malformed arguments.
			input, _ = json.Marshal(map[string]string{"_invalid_json": tc.Function.Arguments})
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Input: input})
	}
	resp := Response{Message: msg, Usage: Usage{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}}
	switch ch.FinishReason {
	case "length":
		resp.Stop = StopMaxTokens
	case "content_filter":
		resp.Stop = StopRefusal
		resp.Detail = "content_filter"
	case "tool_calls":
		resp.Stop = StopToolUse
	default:
		resp.Stop = StopEnd
	}
	if resp.Stop == StopEnd && len(msg.ToolCalls) > 0 {
		resp.Stop = StopToolUse
	}
	return resp, nil
}

func parseOpenAIError(status int, body []byte) *APIError {
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
