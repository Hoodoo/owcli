package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"owcli/internal/llm"
)

// Loop errors.
var (
	ErrStepLimit = errors.New("agent exceeded its step limit")
	ErrRefused   = errors.New("model refused the request")
)

// Event reports loop progress to a caller (for logging or a UI).
type Event struct {
	Kind    string // "text", "tool", "tool_error", "usage"
	Text    string
	Tool    string
	Input   json.RawMessage
	Usage   llm.Usage
	Request int // 1-based model call number
}

// Options configures Run.
type Options struct {
	System    string
	Prompt    string
	Tools     []Tool
	MaxSteps  int // model calls; 0 means 60
	MaxTokens int
	OnEvent   func(Event)
	// Continue is asked when the model ends its turn without a stopping
	// tool. A non-empty reply is sent as the next user message; "" ends the
	// run.
	Continue func(Outcome) string
}

// Outcome summarizes a run.
type Outcome struct {
	Text    string // final assistant text
	Stopped bool   // a tool ended the loop
	Steps   int
	Usage   llm.Usage
}

// maxTokensNudge asks the model to continue after its output was cut off.
const maxTokensNudge = "Your previous response hit the output token limit and was cut off. Continue, and keep each tool call's input smaller (write long pages in several edit_file calls if needed)."

// Run drives the model until it ends its turn, a tool requests a stop, or the
// step limit is reached. Tool failures are returned to the model as error
// results so it can correct itself; context cancellation aborts.
func Run(ctx context.Context, p llm.Provider, o Options) (Outcome, error) {
	maxSteps := o.MaxSteps
	if maxSteps == 0 {
		maxSteps = 60
	}
	emit := func(e Event) {
		if o.OnEvent != nil {
			o.OnEvent(e)
		}
	}
	byName := map[string]Tool{}
	var defs []llm.ToolDef
	for _, t := range o.Tools {
		d := t.Def()
		byName[d.Name] = t
		defs = append(defs, d)
	}
	msgs := []llm.Message{{Role: llm.User, Text: o.Prompt}}
	var out Outcome
	for out.Steps < maxSteps {
		out.Steps++
		resp, err := p.Complete(ctx, llm.Request{System: o.System, Messages: msgs, Tools: defs, MaxTokens: o.MaxTokens})
		if err != nil {
			return out, err
		}
		addUsage(&out.Usage, resp.Usage)
		emit(Event{Kind: "usage", Usage: resp.Usage, Request: out.Steps})
		if resp.Message.Text != "" {
			emit(Event{Kind: "text", Text: resp.Message.Text, Request: out.Steps})
		}
		if resp.Stop == llm.StopRefusal {
			return out, fmt.Errorf("%w: %s", ErrRefused, resp.Detail)
		}
		msgs = append(msgs, resp.Message)
		out.Text = resp.Message.Text

		if len(resp.Message.ToolCalls) == 0 {
			if resp.Stop == llm.StopMaxTokens {
				msgs = append(msgs, llm.Message{Role: llm.User, Text: maxTokensNudge})
				continue
			}
			if o.Continue != nil {
				if next := o.Continue(out); next != "" {
					msgs = append(msgs, llm.Message{Role: llm.User, Text: next})
					continue
				}
			}
			return out, nil
		}

		results := make([]llm.ToolResult, 0, len(resp.Message.ToolCalls))
		stop := false
		for _, call := range resp.Message.ToolCalls {
			res, err := callTool(ctx, byName, call)
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			r := llm.ToolResult{CallID: call.ID, Content: res.Text}
			if err != nil {
				r.Content, r.IsError = err.Error(), true
				emit(Event{Kind: "tool_error", Tool: call.Name, Input: call.Input, Text: err.Error(), Request: out.Steps})
			} else {
				emit(Event{Kind: "tool", Tool: call.Name, Input: call.Input, Request: out.Steps})
			}
			if r.Content == "" {
				r.Content = "ok"
			}
			stop = stop || res.Stop
			results = append(results, r)
		}
		msgs = append(msgs, llm.Message{Role: llm.User, ToolResults: results})
		if stop {
			out.Stopped = true
			return out, nil
		}
	}
	return out, ErrStepLimit
}

func callTool(ctx context.Context, tools map[string]Tool, call llm.ToolCall) (res Result, err error) {
	t, ok := tools[call.Name]
	if !ok {
		names := make([]string, 0, len(tools))
		for n := range tools {
			names = append(names, n)
		}
		return Result{}, fmt.Errorf("unknown tool %q (available: %s)", call.Name, strings.Join(names, ", "))
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %s failed: %v", call.Name, r)
		}
	}()
	return t.Call(ctx, call.Input)
}

func addUsage(total *llm.Usage, u llm.Usage) {
	total.InputTokens += u.InputTokens
	total.OutputTokens += u.OutputTokens
	total.CacheReadTokens += u.CacheReadTokens
	total.CacheWriteTokens += u.CacheWriteTokens
}
