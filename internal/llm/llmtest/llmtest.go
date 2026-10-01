// Package llmtest provides scripted providers for tests.
package llmtest

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"owcli/internal/llm"
)

// Scripted replays a fixed sequence of turns and records every request.
type Scripted struct {
	mu       sync.Mutex
	Turns    []func(req llm.Request) (llm.Response, error)
	Requests []llm.Request
}

// Complete implements llm.Provider.
func (s *Scripted) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, req)
	i := len(s.Requests) - 1
	if i >= len(s.Turns) {
		return llm.Response{}, fmt.Errorf("llmtest: unexpected request %d", i+1)
	}
	return s.Turns[i](req)
}

// Name implements llm.Provider.
func (s *Scripted) Name() string { return "test/scripted" }

// Text is a turn that ends with text.
func Text(text string) func(llm.Request) (llm.Response, error) {
	return func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Message{Role: llm.Assistant, Text: text}, Stop: llm.StopEnd}, nil
	}
}

// Call is a turn that calls one tool with input marshaled to JSON.
func Call(id, name string, input any) func(llm.Request) (llm.Response, error) {
	return Calls(llm.ToolCall{ID: id, Name: name, Input: mustJSON(input)})
}

// Calls is a turn that calls several tools.
func Calls(calls ...llm.ToolCall) func(llm.Request) (llm.Response, error) {
	return func(llm.Request) (llm.Response, error) {
		return llm.Response{Message: llm.Message{Role: llm.Assistant, ToolCalls: calls}, Stop: llm.StopToolUse}, nil
	}
}

// Input marshals v for a ToolCall.
func Input(v any) json.RawMessage { return mustJSON(v) }

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// LastResults returns the tool results the most recent request sent back.
func LastResults(req llm.Request) []llm.ToolResult {
	if len(req.Messages) == 0 {
		return nil
	}
	return req.Messages[len(req.Messages)-1].ToolResults
}
