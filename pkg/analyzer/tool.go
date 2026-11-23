package analyzer

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// DefaultMaxToolSteps is the ToolLoop.MaxSteps used when it is zero.
const DefaultMaxToolSteps = 8

var (
	// ErrInvalidTool is returned for a malformed Tool or ToolLoop.
	ErrInvalidTool = errors.New("analyzer: invalid tool")
	// ErrInvalidToolArguments is returned when a tool call's arguments do not
	// match the tool's schema. A tool handler may also return it to reject
	// arguments the schema cannot express.
	ErrInvalidToolArguments = errors.New("analyzer: invalid tool arguments")
	// ErrUnknownTool is returned when the model calls a tool not in the loop.
	ErrUnknownTool = errors.New("analyzer: unknown tool")
	// ErrMaxToolSteps is returned when the model still calls tools after
	// ToolLoop.MaxSteps replies.
	ErrMaxToolSteps = errors.New("analyzer: tool loop exceeded max steps")
)

// Tool is a typed function the model may call. Build one with NewTool.
type Tool struct {
	spec provider.ToolSpec
	run  func(ctx context.Context, args string) (string, error)
}

// NewTool returns a Tool that decodes the model's arguments into A, calls fn,
// and sends R back to the model as JSON. The parameter schema is SchemaOf(A),
// so A must be a struct that derives an object schema. Arguments that fail the schema or have unknown
// fields are rejected with ErrInvalidToolArguments before fn runs.
func NewTool[A, R any](name, description string, fn func(context.Context, A) (R, error)) (Tool, error) {
	if name == "" {
		return Tool{}, fmt.Errorf("%w: empty name", ErrInvalidTool)
	}
	if fn == nil {
		return Tool{}, fmt.Errorf("%w: tool %q has nil function", ErrInvalidTool, name)
	}
	schema, err := SchemaOf(new(A))
	if err != nil {
		return Tool{}, fmt.Errorf("%w: tool %q: %w", ErrInvalidTool, name, err)
	}
	// Providers take only an object of named arguments.
	if reason, objectRoot := rootObjectContract(schema); !objectRoot {
		return Tool{}, fmt.Errorf("%w: tool %q: arguments must be an object, got %s", ErrInvalidTool, name, reason)
	}
	run := func(ctx context.Context, args string) (string, error) {
		a, err := decodeToolArgs[A](schema, args)
		if err != nil {
			return "", fmt.Errorf("decoding arguments: %w", err)
		}
		r, err := fn(ctx, a)
		if err != nil {
			return "", fmt.Errorf("calling handler: %w", err)
		}
		out, err := json.Marshal(r)
		if err != nil {
			return "", fmt.Errorf("encoding result: %w", err)
		}
		return string(out), nil
	}
	return Tool{spec: provider.ToolSpec{Name: name, Description: description, Parameters: schema}, run: run}, nil
}

// decodeToolArgs validates args against schema and decodes them. Empty args
// count as an empty object. The schema forbids unknown fields.
func decodeToolArgs[A any](schema map[string]any, args string) (A, error) {
	var a A
	raw := []byte(cmp.Or(args, "{}"))
	if err := Validate(schema, raw); err != nil {
		return a, fmt.Errorf("%w: %w", ErrInvalidToolArguments, err)
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, fmt.Errorf("%w: %w", ErrInvalidToolArguments, err)
	}
	return a, nil
}

// ToolLoop configures AnalyzeWithTools.
type ToolLoop struct {
	// Tools are offered to the model. Names must be unique.
	Tools []Tool
	// MaxSteps caps the model replies; zero means DefaultMaxToolSteps.
	MaxSteps int
	// ReportInvalidCalls sends an unknown tool name or invalid arguments back
	// to the model as the tool result, so it can correct itself. Otherwise
	// they end the loop with ErrUnknownTool or ErrInvalidToolArguments. A call
	// without a name and other tool errors always end the loop.
	ReportInvalidCalls bool
}

// toolSet maps tool names to tools.
type toolSet map[string]Tool

// index checks l and returns its tools by name.
func (l *ToolLoop) index() (toolSet, error) {
	if len(l.Tools) == 0 {
		return nil, fmt.Errorf("%w: no tools", ErrInvalidTool)
	}
	if l.MaxSteps < 0 {
		return nil, fmt.Errorf("%w: negative MaxSteps %d", ErrInvalidTool, l.MaxSteps)
	}
	tools := make(toolSet, len(l.Tools))
	for _, tool := range l.Tools {
		if tool.run == nil {
			return nil, fmt.Errorf("%w: tool not built with NewTool", ErrInvalidTool)
		}
		if _, dup := tools[tool.spec.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate name %q", ErrInvalidTool, tool.spec.Name)
		}
		tools[tool.spec.Name] = tool
	}
	return tools, nil
}

// specs returns the tool specs in l.Tools order.
func (l *ToolLoop) specs() []provider.ToolSpec {
	specs := make([]provider.ToolSpec, len(l.Tools))
	for i, tool := range l.Tools {
		specs[i] = tool.spec
	}
	return specs
}

// run runs calls in order and returns one result part per call. With report,
// an unknown tool or invalid arguments become the result instead of an error.
func (ts toolSet) run(ctx context.Context, calls []provider.ToolCall, report bool) ([]provider.Part, error) {
	parts := make([]provider.Part, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("running tool %q: %w", call.Name, err)
		}
		content, invalid, err := ts.call(ctx, call)
		if err != nil {
			if !report || !invalid {
				return nil, fmt.Errorf("running tool %q: %w", call.Name, err)
			}
			content = err.Error()
		}
		parts = append(parts, provider.Part{ToolResult: &provider.ToolResult{CallID: call.ID, Name: call.Name, Content: content}})
	}
	return parts, nil
}

// call runs one tool call. Errors carry no tool name; run adds it. invalid
// reports whether an error is the model's mistake: a name not in ts, or
// invalid arguments. An ErrUnknownTool from a handler is the tool's error.
// An unnamed call is never invalid: history rejects it, so it cannot be
// reported back.
func (ts toolSet) call(ctx context.Context, call provider.ToolCall) (content string, invalid bool, err error) {
	tool, ok := ts[call.Name]
	if !ok {
		return "", call.Name != "", fmt.Errorf("%w: %q", ErrUnknownTool, call.Name)
	}
	content, err = tool.run(ctx, call.Arguments)
	return content, errors.Is(err, ErrInvalidToolArguments), err
}

// toolStep is one model reply in a tool loop: either calls to run, with any
// commentary text, or the parsed final reply.
type toolStep[R any] struct {
	text  string
	calls []provider.ToolCall
	reply R
}

// AnalyzeWithTools is AnalyzeMessages with tools. While the model asks for
// tools, it runs them in order and sends the results back; the first reply
// without tool calls is parsed and returned. Each model call is retried like
// Analyze. The provider must implement provider.ToolCaller; otherwise it
// returns provider.ErrToolsUnsupported.
func (t *Template[T, R]) AnalyzeWithTools(ctx context.Context, msgs []provider.Message, loop ToolLoop, jobID string, publish PublishFn) (R, error) {
	var zero R
	tools, err := loop.index()
	if err != nil {
		return zero, fmt.Errorf("analyzing with tools: %w", err)
	}
	if err := provider.ValidateMessages(msgs); err != nil {
		return zero, fmt.Errorf("analyzing with tools: %w", err)
	}
	tc, ok := provider.AsToolCaller(t.provider)
	if !ok {
		return zero, fmt.Errorf("%w: %s", provider.ErrToolsUnsupported, t.provider.ProviderLabel())
	}
	req := &toolRequest[T]{caller: tc, msgs: slices.Clip(msgs), specs: loop.specs(), jobID: jobID, publish: publish}
	maxSteps := cmp.Or(loop.MaxSteps, DefaultMaxToolSteps)
	for range maxSteps {
		step, err := t.nextToolStep(ctx, req)
		if err != nil {
			return zero, fmt.Errorf("analyzing with tools: %w", err)
		}
		if len(step.calls) == 0 {
			return step.reply, nil
		}
		results, err := tools.run(ctx, step.calls, loop.ReportInvalidCalls)
		if err != nil {
			return zero, fmt.Errorf("analyzing with tools: %w", err)
		}
		req.msgs = append(req.msgs, assistantToolMessage(step.text, step.calls), provider.Message{Role: provider.RoleTool, Parts: results})
	}
	return zero, fmt.Errorf("%w: %d", ErrMaxToolSteps, maxSteps)
}

// toolRequest is what a tool loop sends the model at each step.
type toolRequest[T any] struct {
	caller  provider.ToolCaller[T]
	msgs    []provider.Message // the history so far
	specs   []provider.ToolSpec
	jobID   string
	publish PublishFn
}

// nextToolStep asks the model for its next step, retrying like Analyze. Only
// a final reply goes through the parser.
func (t *Template[T, R]) nextToolStep(ctx context.Context, req *toolRequest[T]) (toolStep[R], error) {
	var calls []provider.ToolCall
	step, err := retry(ctx, t, &callOnceParams[T]{
		call: func(lease *provider.Lease[T]) provider.CallResult {
			res := req.caller.CallTools(ctx, lease, req.msgs, req.specs)
			calls = res.ToolCalls
			return res
		},
		logText: messagesText(req.msgs),
		jobID:   req.jobID,
		publish: req.publish,
	}, func(text string) (toolStep[R], error) {
		if len(calls) > 0 {
			return toolStep[R]{text: text, calls: calls}, nil
		}
		reply, err := t.parser(text)
		if err != nil {
			return toolStep[R]{}, fmt.Errorf("parsing final reply: %w", err)
		}
		return toolStep[R]{reply: reply}, nil
	})
	if err != nil {
		return toolStep[R]{}, fmt.Errorf("asking for the next step: %w", err)
	}
	return step, nil
}

// assistantToolMessage records a reply that called tools: its text, if any,
// then the calls.
func assistantToolMessage(text string, calls []provider.ToolCall) provider.Message {
	parts := make([]provider.Part, 0, len(calls)+1)
	if text != "" {
		parts = append(parts, provider.Part{Text: text})
	}
	for _, call := range calls {
		parts = append(parts, provider.Part{ToolCall: &call})
	}
	return provider.Message{Role: provider.RoleAssistant, Parts: parts}
}
