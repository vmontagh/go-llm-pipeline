package analyzer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

type addArgs struct{ A, B int }

type sum struct{ Sum int }

type noArgs struct{}

var errTool = errors.New("tool failed")

// mustTool is NewTool that fails the test on error.
func mustTool[A, R any](t *testing.T, name string, fn func(context.Context, A) (R, error)) Tool {
	t.Helper()
	tool, err := NewTool(name, "", fn)
	if err != nil {
		t.Fatalf("NewTool(%q) error = %v", name, err)
	}
	return tool
}

// testTools returns the tools the loop tests offer. Each appends its name to
// ran when run; "stop" also calls cancel.
func testTools(t *testing.T, ran *[]string, cancel context.CancelFunc) []Tool {
	t.Helper()
	add := func(_ context.Context, a addArgs) (sum, error) {
		*ran = append(*ran, "add")
		return sum{Sum: a.A + a.B}, nil
	}
	div := func(_ context.Context, a addArgs) (int, error) {
		*ran = append(*ran, "div")
		if a.B == 0 {
			return 0, ErrInvalidToolArguments
		}
		return a.A / a.B, nil
	}
	fail := func(context.Context, noArgs) (int, error) {
		*ran = append(*ran, "fail")
		return 0, errTool
	}
	nested := func(context.Context, noArgs) (int, error) {
		*ran = append(*ran, "nested")
		return 0, ErrUnknownTool
	}
	stop := func(context.Context, noArgs) (int, error) {
		*ran = append(*ran, "stop")
		cancel()
		return 0, nil
	}
	return []Tool{mustTool(t, "add", add), mustTool(t, "div", div), mustTool(t, "fail", fail), mustTool(t, "nested", nested), mustTool(t, "stop", stop)}
}

// unparsable is a reply parseUnlessUnparsable rejects.
const unparsable = "unparsable"

var errUnparsable = errors.New("unparsable reply")

// parseUnlessUnparsable keeps the raw reply, but rejects unparsable.
func parseUnlessUnparsable(raw string) (parsedResult, error) {
	if raw == unparsable {
		return parsedResult{}, errUnparsable
	}
	return parsedResult{Value: raw}, nil
}

// toolCall returns a call of the named tool with args.
func toolCall(id, name, args string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: name, Arguments: args}
}

// callsReply is a model reply that calls tools.
func callsReply(text string, calls ...provider.ToolCall) provider.CallResult {
	return provider.CallResult{Outcome: provider.OutcomeSuccess, Text: text, ToolCalls: calls}
}

// textReply is a final model reply.
func textReply(text string) provider.CallResult {
	return provider.CallResult{Outcome: provider.OutcomeSuccess, Text: text}
}

// exchange is the history a tool round-trip adds: the assistant's text and
// calls, then the results, which pair with the calls in order.
func exchange(text string, calls []provider.ToolCall, contents ...string) []provider.Message {
	asst := provider.Message{Role: provider.RoleAssistant}
	if text != "" {
		asst.Parts = append(asst.Parts, provider.Part{Text: text})
	}
	results := provider.Message{Role: provider.RoleTool}
	for i, c := range calls {
		asst.Parts = append(asst.Parts, provider.Part{ToolCall: &c})
		results.Parts = append(results.Parts, provider.Part{ToolResult: &provider.ToolResult{CallID: c.ID, Name: c.Name, Content: contents[i]}})
	}
	return []provider.Message{asst, results}
}

func TestNewTool(t *testing.T) {
	t.Parallel()
	add := func(_ context.Context, a addArgs) (sum, error) { return sum{Sum: a.A + a.B}, nil }
	tests := []struct {
		name    string
		tool    func() (Tool, error)
		args    string
		want    string
		wantErr error
	}{
		{name: "runs with decoded args", tool: func() (Tool, error) { return NewTool("add", "", add) }, args: `{"A":1,"B":2}`, want: `{"Sum":3}`},
		{
			name: "empty args are an empty object",
			tool: func() (Tool, error) {
				return NewTool("ping", "", func(context.Context, noArgs) (string, error) { return "pong", nil })
			},
			want: `"pong"`,
		},
		{name: "args failing the schema", tool: func() (Tool, error) { return NewTool("add", "", add) }, args: `{"A":"one","B":2}`, wantErr: ErrInvalidToolArguments},
		{name: "args with unknown field", tool: func() (Tool, error) { return NewTool("add", "", add) }, args: `{"A":1,"B":2,"C":3}`, wantErr: ErrInvalidToolArguments},
		{name: "malformed args", tool: func() (Tool, error) { return NewTool("add", "", add) }, args: `{"A":1`, wantErr: ErrInvalidToolArguments},
		{
			name: "handler error",
			tool: func() (Tool, error) {
				return NewTool("fail", "", func(context.Context, noArgs) (int, error) { return 0, errTool })
			},
			wantErr: errTool,
		},
		{
			name: "unencodable result",
			tool: func() (Tool, error) {
				return NewTool("chan", "", func(context.Context, noArgs) (chan int, error) { return make(chan int), nil })
			},
			wantErr: cmpopts.AnyError,
		},
		{
			name: "args not an object",
			tool: func() (Tool, error) {
				return NewTool("at", "", func(context.Context, time.Time) (int, error) { return 0, nil })
			},
			wantErr: ErrInvalidTool,
		},
		{name: "empty name", tool: func() (Tool, error) { return NewTool("", "", add) }, wantErr: ErrInvalidTool},
		{name: "longest name", tool: func() (Tool, error) { return NewTool("_"+strings.Repeat("a-1", 21), "", add) }, args: `{"A":1,"B":2}`, want: `{"Sum":3}`},
		{name: "name too long", tool: func() (Tool, error) { return NewTool("_"+strings.Repeat("a-1", 21)+"x", "", add) }, wantErr: ErrInvalidTool},
		{name: "name with a dot", tool: func() (Tool, error) { return NewTool("weather.lookup", "", add) }, wantErr: ErrInvalidTool},
		{name: "name with a space", tool: func() (Tool, error) { return NewTool("get weather", "", add) }, wantErr: ErrInvalidTool},
		{name: "name starting with a digit", tool: func() (Tool, error) { return NewTool("1add", "", add) }, wantErr: ErrInvalidTool},
		{name: "nil function", tool: func() (Tool, error) { return NewTool[addArgs, sum]("add", "", nil) }, wantErr: ErrInvalidTool},
		{
			name: "args not a struct",
			tool: func() (Tool, error) {
				return NewTool("bad", "", func(context.Context, int) (int, error) { return 0, nil })
			},
			wantErr: ErrInvalidTool,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tool, err := tt.tool()
			got := ""
			if err == nil {
				got, err = tool.run(t.Context(), tt.args)
			}

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("run(%q) mismatch (-want +got):\n%s", tt.args, diff)
			}
		})
	}
}

func TestNewToolSpec(t *testing.T) {
	t.Parallel()
	tool, err := NewTool("add", "adds", func(_ context.Context, a addArgs) (sum, error) { return sum{}, nil })
	if err != nil {
		t.Fatalf("NewTool() error = %v", err)
	}
	schema, err := SchemaOf(addArgs{})
	if err != nil {
		t.Fatalf("SchemaOf() error = %v", err)
	}

	want := provider.ToolSpec{Name: "add", Description: "adds", Parameters: schema}
	if diff := cmp.Diff(want, tool.spec); diff != "" {
		t.Errorf("spec mismatch (-want +got):\n%s", diff)
	}
}

func TestTemplate_AnalyzeWithTools(t *testing.T) {
	t.Parallel()
	user := provider.TextMessage(provider.RoleUser, "q")
	add := toolCall("c1", "add", `{"A":1,"B":2}`)
	add2 := toolCall("c2", "add", `{"A":3,"B":4}`)
	unknown := toolCall("c1", "nope", `{}`)
	divByZero := toolCall("c1", "div", `{"A":1,"B":0}`)
	badArgs := toolCall("c1", "add", `{"A":"one"}`)
	malformed := toolCall("c1", "add", `{"A":1`)
	fail := toolCall("c1", "fail", `{}`)
	nested := toolCall("c1", "nested", `{}`)
	stop := toolCall("c1", "stop", `{}`)
	rateLimited := provider.CallResult{Outcome: provider.OutcomeProviderError, Err: provider.ErrRateLimitExceeded}
	// A reply whose provider keeps its parts in order, with signatures.
	signedParts := []provider.Part{{Text: "a", Signature: "s1"}, {ToolCall: &add, Signature: "s2"}, {Signature: "s3"}, {Text: "b"}, {ToolCall: &add2}}
	signedReply := callsReply("ab", add, add2)
	signedReply.Parts = signedParts
	signedResults := exchange("", []provider.ToolCall{add, add2}, `{"Sum":3}`, `{"Sum":7}`)[1]
	// Every default step calls add; the last request holds all earlier rounds.
	defaultSteps := make([]provider.CallResult, 0, DefaultMaxToolSteps)
	defaultRan := make([]string, 0, DefaultMaxToolSteps)
	defaultSent := []provider.Message{user}
	for i := range DefaultMaxToolSteps {
		defaultSteps = append(defaultSteps, callsReply("", add))
		defaultRan = append(defaultRan, "add")
		if i < DefaultMaxToolSteps-1 {
			defaultSent = append(defaultSent, exchange("", []provider.ToolCall{add}, `{"Sum":3}`)...)
		}
	}
	tests := []struct {
		name     string
		replies  []provider.CallResult
		maxSteps int
		report   bool
		want     string
		wantErr  error
		wantRan  []string
		wantSent []provider.Message // on the last call
	}{
		{
			name:     "reply without tool calls",
			replies:  []provider.CallResult{textReply("done")},
			want:     "done",
			wantSent: []provider.Message{user},
		},
		{
			name:     "one round-trip",
			replies:  []provider.CallResult{callsReply("", add), textReply("3")},
			want:     "3",
			wantRan:  []string{"add"},
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{add}, `{"Sum":3}`)...),
		},
		{
			name:     "calls run in order and the text is kept",
			replies:  []provider.CallResult{callsReply("adding", add, add2), textReply("3, 7")},
			want:     "3, 7",
			wantRan:  []string{"add", "add"},
			wantSent: append([]provider.Message{user}, exchange("adding", []provider.ToolCall{add, add2}, `{"Sum":3}`, `{"Sum":7}`)...),
		},
		{
			name:     "ordered signed parts are replayed intact",
			replies:  []provider.CallResult{signedReply, textReply("3, 7")},
			want:     "3, 7",
			wantRan:  []string{"add", "add"},
			wantSent: []provider.Message{user, {Role: provider.RoleAssistant, Parts: signedParts}, signedResults},
		},
		{
			name:     "rounds accumulate",
			replies:  []provider.CallResult{callsReply("", add), callsReply("", add2), textReply("done")},
			want:     "done",
			wantRan:  []string{"add", "add"},
			wantSent: append(append([]provider.Message{user}, exchange("", []provider.ToolCall{add}, `{"Sum":3}`)...), exchange("", []provider.ToolCall{add2}, `{"Sum":7}`)...),
		},
		{
			name:     "retries a failed call",
			replies:  []provider.CallResult{rateLimited, callsReply("", add), textReply("3")},
			want:     "3",
			wantRan:  []string{"add"},
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{add}, `{"Sum":3}`)...),
		},
		{
			name:     "retries a reply the parser rejects",
			replies:  []provider.CallResult{textReply(unparsable), textReply("done")},
			want:     "done",
			wantSent: []provider.Message{user},
		},
		{
			name:     "unknown tool fails",
			replies:  []provider.CallResult{callsReply("", unknown)},
			wantErr:  ErrUnknownTool,
			wantSent: []provider.Message{user},
		},
		{
			name:     "unknown tool reported",
			replies:  []provider.CallResult{callsReply("", unknown), textReply("sorry")},
			report:   true,
			want:     "sorry",
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{unknown}, `analyzer: unknown tool: "nope"`)...),
		},
		{
			name:     "unnamed tool fails even when reporting",
			replies:  []provider.CallResult{callsReply("", toolCall("c1", "", "{}"))},
			report:   true,
			wantErr:  ErrUnknownTool,
			wantSent: []provider.Message{user},
		},
		{
			name:     "invalid args fail",
			replies:  []provider.CallResult{callsReply("", badArgs)},
			wantErr:  ErrInvalidToolArguments,
			wantSent: []provider.Message{user},
		},
		{
			name:     "malformed args reported",
			replies:  []provider.CallResult{callsReply("", malformed), textReply("retry")},
			report:   true,
			want:     "retry",
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{malformed}, `decoding arguments: analyzer: invalid tool arguments: (root): json: invalid JSON: unexpected EOF`)...),
		},
		{
			name:     "args rejected by the tool reported",
			replies:  []provider.CallResult{callsReply("", divByZero), textReply("cannot")},
			report:   true,
			want:     "cannot",
			wantRan:  []string{"div"},
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{divByZero}, `calling handler: analyzer: invalid tool arguments`)...),
		},
		{
			name:     "tool error fails even when reporting",
			replies:  []provider.CallResult{callsReply("", fail)},
			report:   true,
			wantErr:  errTool,
			wantRan:  []string{"fail"},
			wantSent: []provider.Message{user},
		},
		{
			name:     "unknown tool from a handler fails even when reporting",
			replies:  []provider.CallResult{callsReply("", nested)},
			report:   true,
			wantErr:  ErrUnknownTool,
			wantRan:  []string{"nested"},
			wantSent: []provider.Message{user},
		},
		{
			name:     "cancel stops the remaining calls",
			replies:  []provider.CallResult{callsReply("", stop, add)},
			wantErr:  context.Canceled,
			wantRan:  []string{"stop"},
			wantSent: []provider.Message{user},
		},
		{
			name:     "max steps",
			replies:  []provider.CallResult{callsReply("", add), callsReply("", add2)},
			maxSteps: 2,
			wantErr:  ErrMaxToolSteps,
			wantRan:  []string{"add", "add"},
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{add}, `{"Sum":3}`)...),
		},
		{
			name:     "final reply on the last step",
			replies:  []provider.CallResult{callsReply("", add), textReply("3")},
			maxSteps: 2,
			want:     "3",
			wantRan:  []string{"add"},
			wantSent: append([]provider.Message{user}, exchange("", []provider.ToolCall{add}, `{"Sum":3}`)...),
		},
		{
			name:     "default max steps",
			replies:  defaultSteps,
			wantErr:  ErrMaxToolSteps,
			wantRan:  defaultRan,
			wantSent: defaultSent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var ran []string
			tp := newToolProvider(tt.replies)
			// Guarded, so the loop must look past the Guard's own CallTools.
			tmpl := buildTemplate(t, NewBuilder(wrapInGuards(t, tp, 1), parseUnlessUnparsable).WithBackOff(&backoff.ZeroBackOff{}))
			loop := ToolLoop{Tools: testTools(t, &ran, cancel), MaxSteps: tt.maxSteps, ReportInvalidCalls: tt.report}

			got, err := tmpl.AnalyzeWithTools(ctx, []provider.Message{user}, loop, "job", nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("AnalyzeWithTools() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(parsedResult{Value: tt.want}, got); diff != "" {
				t.Errorf("AnalyzeWithTools() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantRan, ran); diff != "" {
				t.Errorf("tools run mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantSent, tp.sent[len(tp.sent)-1]); diff != "" {
				t.Errorf("last messages sent mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(loop.specs(), tp.gotTools); diff != "" {
				t.Errorf("tools sent mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTemplate_AnalyzeWithToolsRejects(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{provider.TextMessage(provider.RoleUser, "u")}
	tests := []struct {
		name     string
		guards   int
		msgs     []provider.Message
		tools    []string // names; "" is a Tool not built with NewTool
		maxSteps int
		wantErr  error
	}{
		{name: "no tools", msgs: msgs, wantErr: ErrInvalidTool},
		{name: "duplicate names", msgs: msgs, tools: []string{"ping", "ping"}, wantErr: ErrInvalidTool},
		{name: "tool not from NewTool", msgs: msgs, tools: []string{""}, wantErr: ErrInvalidTool},
		{name: "negative max steps", msgs: msgs, tools: []string{"ping"}, maxSteps: -1, wantErr: ErrInvalidTool},
		{name: "invalid messages", tools: []string{"ping"}, wantErr: provider.ErrInvalidMessages},
		{name: "provider without tools", msgs: msgs, tools: []string{"ping"}, wantErr: provider.ErrToolsUnsupported},
		{name: "guarded provider without tools", guards: 1, msgs: msgs, tools: []string{"ping"}, wantErr: provider.ErrToolsUnsupported},
		{name: "nested guarded provider without tools", guards: 2, msgs: msgs, tools: []string{"ping"}, wantErr: provider.ErrToolsUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// No keys: a rejection must come before any key is needed.
			mock := newMockProvider(&provider.CallResult{Outcome: provider.OutcomeSuccess})
			mock.keys = provider.NewKeyPool[string](nil)
			tmpl := newEchoTemplate(t, wrapInGuards(t, mock, tt.guards))
			loop := ToolLoop{Tools: pingTools(t, tt.tools), MaxSteps: tt.maxSteps}

			_, err := tmpl.AnalyzeWithTools(t.Context(), tt.msgs, loop, "job", nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("AnalyzeWithTools() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// pingTools returns a no-op tool per name; "" gives a zero Tool.
func pingTools(t *testing.T, names []string) []Tool {
	t.Helper()
	ping := func(context.Context, noArgs) (string, error) { return "pong", nil }
	var tools []Tool
	for _, name := range names {
		if name == "" {
			tools = append(tools, Tool{})
			continue
		}
		tools = append(tools, mustTool(t, name, ping))
	}
	return tools
}

// toolHistory is a conversation with a completed tool round-trip.
func toolHistory() []provider.Message {
	call := toolCall("c1", "add", `{"A":1,"B":2}`)
	return append([]provider.Message{provider.TextMessage(provider.RoleUser, "q")}, exchange("", []provider.ToolCall{call}, `{"Sum":3}`)...)
}
