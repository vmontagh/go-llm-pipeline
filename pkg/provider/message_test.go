package provider_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

func TestValidateMessages(t *testing.T) {
	t.Parallel()
	user := provider.TextMessage(provider.RoleUser, "u")
	call := provider.Part{ToolCall: &provider.ToolCall{ID: "c1", Name: "lookup", Arguments: "{}"}}
	result := provider.Part{ToolResult: &provider.ToolResult{CallID: "c1", Name: "lookup", Content: "42"}}
	msg := func(role provider.Role, parts ...provider.Part) provider.Message {
		return provider.Message{Role: role, Parts: parts}
	}
	tests := []struct {
		name         string
		msgs         []provider.Message
		wantErr      error
		wantProblems int
	}{
		{
			name: "valid conversation",
			msgs: []provider.Message{
				provider.TextMessage(provider.RoleSystem, "s"),
				{Role: provider.RoleUser, Parts: []provider.Part{{Text: "u"}, {Image: []byte("img")}}},
				provider.TextMessage(provider.RoleAssistant, "a"),
			},
		},
		{name: "empty", wantErr: provider.ErrInvalidMessages},
		{name: "unknown role", msgs: []provider.Message{provider.TextMessage(provider.RoleUnknown, "u")}, wantErr: provider.ErrInvalidMessages},
		{name: "no parts", msgs: []provider.Message{user, {Role: provider.RoleUser}}, wantErr: provider.ErrInvalidMessages},
		{name: "empty part", msgs: []provider.Message{{Role: provider.RoleUser, Parts: []provider.Part{{}}}}, wantErr: provider.ErrInvalidMessages},
		{name: "text and image in one part", msgs: []provider.Message{{Role: provider.RoleUser, Parts: []provider.Part{{Text: "u", Image: []byte("img")}}}}, wantErr: provider.ErrInvalidMessages},
		{name: "system image", msgs: []provider.Message{{Role: provider.RoleSystem, Parts: []provider.Part{{Image: []byte("img")}}}, user}, wantErr: provider.ErrInvalidMessages},
		{
			name:         "reports every problem",
			msgs:         []provider.Message{provider.TextMessage(provider.RoleUnknown, "u"), {Role: provider.RoleUser, Parts: []provider.Part{{}}}},
			wantErr:      provider.ErrInvalidMessages,
			wantProblems: 2,
		},
		{name: "assistant image", msgs: []provider.Message{user, {Role: provider.RoleAssistant, Parts: []provider.Part{{Image: []byte("img")}}}}, wantErr: provider.ErrInvalidMessages},
		{name: "only system", msgs: []provider.Message{provider.TextMessage(provider.RoleSystem, "s")}, wantErr: provider.ErrInvalidMessages},
		{
			name: "valid tool round-trip",
			msgs: []provider.Message{user, msg(provider.RoleAssistant, provider.Part{Text: "checking"}, call), msg(provider.RoleTool, result)},
		},
		{name: "signed empty assistant part", msgs: []provider.Message{user, msg(provider.RoleAssistant, provider.Part{Signature: "s"})}},
		{name: "signed empty user part", msgs: []provider.Message{msg(provider.RoleUser, provider.Part{Signature: "s"})}, wantErr: provider.ErrInvalidMessages},
		{name: "tool call and text in one part", msgs: []provider.Message{user, msg(provider.RoleAssistant, provider.Part{Text: "a", ToolCall: call.ToolCall})}, wantErr: provider.ErrInvalidMessages},
		{name: "user tool call", msgs: []provider.Message{msg(provider.RoleUser, call)}, wantErr: provider.ErrInvalidMessages},
		{name: "assistant tool result", msgs: []provider.Message{user, msg(provider.RoleAssistant, result)}, wantErr: provider.ErrInvalidMessages},
		{name: "tool message text", msgs: []provider.Message{user, msg(provider.RoleTool, provider.Part{Text: "42"})}, wantErr: provider.ErrInvalidMessages},
		{name: "tool message image", msgs: []provider.Message{user, msg(provider.RoleTool, provider.Part{Image: []byte("img")})}, wantErr: provider.ErrInvalidMessages},
		{name: "unnamed tool call", msgs: []provider.Message{user, msg(provider.RoleAssistant, provider.Part{ToolCall: &provider.ToolCall{ID: "c1"}})}, wantErr: provider.ErrInvalidMessages},
		{name: "unnamed tool result", msgs: []provider.Message{user, msg(provider.RoleTool, provider.Part{ToolResult: &provider.ToolResult{CallID: "c1"}})}, wantErr: provider.ErrInvalidMessages},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := provider.ValidateMessages(tt.msgs)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("ValidateMessages() error mismatch (-want +got):\n%s", diff)
			}
			if tt.wantProblems == 0 {
				return
			}
			joined, ok := err.(interface{ Unwrap() []error })
			if !ok {
				t.Fatalf("ValidateMessages() = %v, want joined errors", err)
			}
			if diff := cmp.Diff(tt.wantProblems, len(joined.Unwrap())); diff != "" {
				t.Errorf("problem count mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
