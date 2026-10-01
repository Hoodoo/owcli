package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"owcli/internal/config"
)

type recorded struct {
	headers http.Header
	body    map[string]any
}

// server replies with the given responses in order and records requests.
func server(t *testing.T, replies ...func(w http.ResponseWriter)) (*httptest.Server, *[]recorded) {
	t.Helper()
	var reqs []recorded
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		reqs = append(reqs, recorded{headers: r.Header.Clone(), body: body})
		if i >= len(replies) {
			t.Errorf("unexpected request %d", i)
			w.WriteHeader(500)
			return
		}
		replies[i](w)
		i++
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func reply(status int, body string, headers ...string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		for i := 0; i+1 < len(headers); i += 2 {
			w.Header().Set(headers[i], headers[i+1])
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func fast(c *client) { c.backoff = func(int) time.Duration { return time.Millisecond } }

var readTool = ToolDef{Name: "read_file", Description: "Read a file.", Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}

const anthropicToolUse = `{"content":[
 {"type":"thinking","thinking":"","signature":"sig-123"},
 {"type":"text","text":"Reading."},
 {"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"go.mod"}}
],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3,"cache_creation_input_tokens":7}}`

func TestAnthropicToolLoop(t *testing.T) {
	srv, reqs := server(t,
		reply(200, anthropicToolUse),
		reply(200, `{"content":[{"type":"text","text":"Done."}],"stop_reason":"end_turn","usage":{}}`),
	)
	a := NewAnthropic(srv.URL+"/", "sk-test", "claude-opus-5-5", "high", true)
	fast(a.c)
	req := Request{System: "sys", Tools: []ToolDef{readTool}, Messages: []Message{{Role: User, Text: "Look at go.mod"}}}
	resp, err := a.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Stop != StopToolUse || resp.Message.Text != "Reading." || len(resp.Message.ToolCalls) != 1 ||
		resp.Message.ToolCalls[0].Name != "read_file" || string(resp.Message.ToolCalls[0].Input) != `{"path":"go.mod"}` {
		t.Fatalf("response %+v", resp)
	}
	if resp.Usage != (Usage{InputTokens: 10, OutputTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 7}) {
		t.Errorf("usage %+v", resp.Usage)
	}

	first := (*reqs)[0]
	if first.headers.Get("x-api-key") != "sk-test" || first.headers.Get("anthropic-version") != "2023-06-01" || first.headers.Get("anthropic-beta") != "server-side-fallback-2026-07-01" {
		t.Errorf("headers %v", first.headers)
	}
	b := first.body
	if b["model"] != "claude-opus-5-5" || b["max_tokens"] != float64(16000) || b["system"] != "sys" || b["fallbacks"] != "default" {
		t.Errorf("body %v", b)
	}
	if _, ok := b["thinking"]; ok {
		t.Error("thinking must be left to the model default")
	}
	if b["output_config"].(map[string]any)["effort"] != "high" || b["cache_control"].(map[string]any)["type"] != "ephemeral" || b["tool_choice"].(map[string]any)["type"] != "auto" {
		t.Errorf("body %v", b)
	}
	tool := b["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "read_file" || tool["input_schema"] == nil {
		t.Errorf("tool %v", tool)
	}

	// Second turn: the assistant content (with its thinking block) is resent
	// verbatim, followed by the tool result.
	req.Messages = append(req.Messages, resp.Message, Message{Role: User, ToolResults: []ToolResult{{CallID: "toolu_1", Content: "module x", IsError: false}}})
	resp, err = a.Complete(context.Background(), req)
	if err != nil || resp.Stop != StopEnd || resp.Message.Text != "Done." {
		t.Fatalf("second %+v %v", resp, err)
	}
	msgs := (*reqs)[1].body["messages"].([]any)
	assistant := msgs[1].(map[string]any)["content"].([]any)
	if assistant[0].(map[string]any)["signature"] != "sig-123" || len(assistant) != 3 {
		t.Errorf("assistant content not verbatim: %v", assistant)
	}
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "toolu_1" || result["content"] != "module x" {
		t.Errorf("tool result %v", result)
	}
}

func TestAnthropicNoFallbacksAndRefusal(t *testing.T) {
	srv, reqs := server(t, reply(200, `{"content":[],"stop_reason":"refusal","stop_details":{"type":"refusal","category":"cyber","explanation":"declined"},"usage":{}}`))
	a := NewAnthropic(srv.URL, "k", "m", "", false)
	resp, err := a.Complete(context.Background(), Request{Messages: []Message{{Role: User, Text: "hi"}}})
	if err != nil || resp.Stop != StopRefusal || resp.Detail != "cyber declined" {
		t.Fatalf("%+v %v", resp, err)
	}
	b := (*reqs)[0].body
	if _, ok := b["fallbacks"]; ok || (*reqs)[0].headers.Get("anthropic-beta") != "" {
		t.Error("fallbacks must be omitted when disabled")
	}
	if _, ok := b["output_config"]; ok {
		t.Error("empty effort must be omitted")
	}
	if _, ok := b["tool_choice"]; ok {
		t.Error("tool_choice only with tools")
	}
}

func TestRetries(t *testing.T) {
	srv, reqs := server(t,
		reply(429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, "retry-after", "0"),
		reply(529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`),
		reply(200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{}}`),
	)
	a := NewAnthropic(srv.URL, "k", "m", "", false)
	fast(a.c)
	resp, err := a.Complete(context.Background(), Request{Messages: []Message{{Role: User, Text: "hi"}}})
	if err != nil || resp.Message.Text != "ok" || len(*reqs) != 3 {
		t.Fatalf("%+v %v (%d requests)", resp, err, len(*reqs))
	}
}

func TestNonRetryableError(t *testing.T) {
	srv, reqs := server(t, reply(400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	a := NewAnthropic(srv.URL, "k", "m", "", false)
	fast(a.c)
	_, err := a.Complete(context.Background(), Request{Messages: []Message{{Role: User, Text: "hi"}}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 || apiErr.Type != "invalid_request_error" || len(*reqs) != 1 {
		t.Fatalf("err %v (%d requests)", err, len(*reqs))
	}
}

func TestRetriesExhausted(t *testing.T) {
	r := reply(500, "oops")
	srv, reqs := server(t, r, r, r, r)
	o := NewOpenAI(srv.URL, "", "m")
	fast(o.c)
	_, err := o.Complete(context.Background(), Request{Messages: []Message{{Role: User, Text: "hi"}}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Message != "oops" || len(*reqs) != 4 {
		t.Fatalf("err %v (%d requests)", err, len(*reqs))
	}
}

func TestOpenAIToolLoop(t *testing.T) {
	srv, reqs := server(t,
		reply(200, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"go.mod\"}"}},{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{broken"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":4,"completion_tokens":2}}`),
		reply(200, `{"choices":[{"message":{"role":"assistant","content":"Done."},"finish_reason":"stop"}],"usage":{}}`),
	)
	o := NewOpenAI(srv.URL+"/", "key", "gpt-x")
	req := Request{System: "sys", Tools: []ToolDef{readTool}, MaxTokens: 100, Messages: []Message{{Role: User, Text: "Look"}}}
	resp, err := o.Complete(context.Background(), req)
	if err != nil || resp.Stop != StopToolUse || len(resp.Message.ToolCalls) != 2 || resp.Usage.InputTokens != 4 {
		t.Fatalf("%+v %v", resp, err)
	}
	if string(resp.Message.ToolCalls[1].Input) != `{"_invalid_json":"{broken"}` {
		t.Errorf("invalid arguments should be preserved: %s", resp.Message.ToolCalls[1].Input)
	}
	if (*reqs)[0].headers.Get("authorization") != "Bearer key" {
		t.Error("auth header")
	}
	b := (*reqs)[0].body
	msgs := b["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || b["max_tokens"] != float64(100) || b["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != "read_file" {
		t.Errorf("body %v", b)
	}

	req.Messages = append(req.Messages, resp.Message, Message{Role: User, ToolResults: []ToolResult{
		{CallID: "call_1", Content: "module x"}, {CallID: "call_2", Content: "bad args", IsError: true},
	}})
	resp, err = o.Complete(context.Background(), req)
	if err != nil || resp.Stop != StopEnd || resp.Message.Text != "Done." {
		t.Fatalf("%+v %v", resp, err)
	}
	msgs = (*reqs)[1].body["messages"].([]any)
	assistant := msgs[2].(map[string]any)
	calls := assistant["tool_calls"].([]any)
	if len(calls) != 2 || assistant["content"] != "" {
		t.Errorf("assistant %v", assistant)
	}
	tool := msgs[4].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call_2" || !strings.HasPrefix(tool["content"].(string), "ERROR: ") {
		t.Errorf("tool message %v", tool)
	}
}

func TestNew(t *testing.T) {
	t.Setenv("OWCLI_TEST_KEY", "")
	c := config.Config{Provider: config.ProviderAnthropic, Model: "m", APIKeyEnv: "OWCLI_TEST_KEY", BaseURL: "https://api.anthropic.com"}
	if _, err := New(c); !errors.Is(err, ErrNoAPIKey) {
		t.Fatalf("want ErrNoAPIKey, got %v", err)
	}
	t.Setenv("OWCLI_TEST_KEY", "k")
	p, err := New(c)
	if err != nil || p.Name() != "anthropic/m" {
		t.Fatalf("%v %v", p, err)
	}
	t.Setenv("OWCLI_TEST_KEY", "")
	local := config.Config{Provider: config.ProviderOpenAI, Model: "llama", APIKeyEnv: "OWCLI_TEST_KEY", BaseURL: "http://localhost:11434/v1"}
	if p, err := New(local); err != nil || p.Name() != "openai/llama" {
		t.Fatalf("local endpoints need no key: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	if retryAfter("2") != 2*time.Second || retryAfter("") != 0 || retryAfter("junk") != 0 {
		t.Error("retry-after parsing")
	}
}
