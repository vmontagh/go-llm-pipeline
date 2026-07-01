package pretty

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrpatch"
)

func TestFormatJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "valid unformatted JSON",
			input: `{"a":1,"b":[2,3]}`,
			want: `{
  "a": 1,
  "b": [
    2,
    3
  ]
}`,
		},
		{
			name:  "already formatted JSON",
			input: "{\n  \"a\": 1\n}",
			want:  "{\n  \"a\": 1\n}",
		},
		{
			name:  "malformed JSON",
			input: `{"a":1`,
			want:  `{"a":1`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := FormatJSON(tt.input)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("FormatJSON() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFormatInteraction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		interaction     vcrpatch.Interaction
		wantChanged     bool
		wantInteraction vcrpatch.Interaction
	}{
		{
			name: "non-JSON content type is no-op",
			interaction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: vcrpatch.InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantInteraction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: vcrpatch.InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
		},
		{
			name: "JSON request formatted, headers and length updated",
			interaction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"7"},
					},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: vcrpatch.InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantChanged: true,
			wantInteraction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"12"},
					},
					Body: `{
  "a": 1
}`,
					ContentLength: 12,
				},
				Response: vcrpatch.InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
		},
		{
			name: "JSON response formatted, headers and length updated",
			interaction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: vcrpatch.InteractionResp{
					Headers: map[string][]string{
						"Content-Type":   {"application/json; charset=utf-8"},
						"Content-Length": {"7"},
					},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantChanged: true,
			wantInteraction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: vcrpatch.InteractionResp{
					Headers: map[string][]string{
						"Content-Type":   {"application/json; charset=utf-8"},
						"Content-Length": {"12"},
					},
					Body: `{
  "b": 2
}`,
					ContentLength: 12,
				},
			},
		},
		{
			name: "malformed JSON request passes through",
			interaction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"6"},
					},
					Body:          `{"a":1`,
					ContentLength: 6,
				},
				Response: vcrpatch.InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantInteraction: vcrpatch.Interaction{
				Request: vcrpatch.InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"6"},
					},
					Body:          `{"a":1`,
					ContentLength: 6,
				},
				Response: vcrpatch.InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotChanged := FormatInteraction(&tt.interaction)

			if gotChanged != tt.wantChanged {
				t.Errorf("FormatInteraction() changed = %v, want %v", gotChanged, tt.wantChanged)
			}
			if diff := cmp.Diff(tt.wantInteraction, tt.interaction); diff != "" {
				t.Errorf("FormatInteraction() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
