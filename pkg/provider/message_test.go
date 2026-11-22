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
	tests := []struct {
		name    string
		msgs    []provider.Message
		wantErr error
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
		{name: "only system", msgs: []provider.Message{provider.TextMessage(provider.RoleSystem, "s")}, wantErr: provider.ErrInvalidMessages},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := provider.ValidateMessages(tt.msgs)
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("ValidateMessages() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
