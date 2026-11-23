package provider

import (
	"context"
	"errors"
)

// ErrToolsUnsupported is returned for a tool request, or for messages with
// tool parts, to a provider that is not a ToolCaller.
var ErrToolsUnsupported = errors.New("provider does not support tools")

// ToolSpec declares a tool the model may call.
type ToolSpec struct {
	Name        string
	Description string
	// Parameters is the JSON Schema of the arguments object.
	Parameters map[string]any
}

// ToolCall is a model's request to run a tool.
type ToolCall struct {
	// ID matches the call to its result. Some providers leave it empty.
	ID   string
	Name string
	// Arguments is the JSON arguments object as the model sent it. It may be
	// malformed.
	Arguments string
	// Signature is opaque provider state that must go back with the call,
	// such as Gemini's thought signature. Keep it as received.
	Signature string
}

// ToolResult is the outcome of a ToolCall, sent back to the model.
type ToolResult struct {
	// CallID and Name repeat the ToolCall's ID and Name.
	CallID  string
	Name    string
	Content string
}

// ToolCaller is implemented by providers that can offer tools to the model.
// msgs has passed ValidateMessages. On OutcomeSuccess, CallResult.ToolCalls
// lists the tools to run, in order, and Text may hold commentary; with no
// ToolCalls, Text is the reply. The lease rules of Provider.Call apply.
type ToolCaller[T any] interface {
	CallTools(ctx context.Context, lease *Lease[T], msgs []Message, tools []ToolSpec) CallResult
}

// AsToolCaller returns p as a ToolCaller when it can offer tools. Guards
// count only if the provider they wrap does, though a Guard always has
// CallTools.
func AsToolCaller[T any](p Provider[T]) (ToolCaller[T], bool) {
	return as[ToolCaller[T]](p)
}
