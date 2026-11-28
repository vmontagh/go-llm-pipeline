package llmvcr

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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
		interaction     Interaction
		wantChanged     bool
		wantInteraction Interaction
	}{
		{
			name: "non-JSON content type is no-op",
			interaction: Interaction{
				Request: InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantInteraction: Interaction{
				Request: InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
		},
		{
			name: "JSON request formatted, headers and length updated",
			interaction: Interaction{
				Request: InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"7"},
					},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantChanged: true,
			wantInteraction: Interaction{
				Request: InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"12"},
					},
					Body: `{
  "a": 1
}`,
					ContentLength: 12,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
		},
		{
			name: "JSON response formatted, headers and length updated",
			interaction: Interaction{
				Request: InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: InteractionResp{
					Headers: map[string][]string{
						"Content-Type":   {"application/json; charset=utf-8"},
						"Content-Length": {"7"},
					},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantChanged: true,
			wantInteraction: Interaction{
				Request: InteractionReq{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: InteractionResp{
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
			interaction: Interaction{
				Request: InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"6"},
					},
					Body:          `{"a":1`,
					ContentLength: 6,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantInteraction: Interaction{
				Request: InteractionReq{
					Headers: map[string][]string{
						"Content-Type":   {"application/json"},
						"Content-Length": {"6"},
					},
					Body:          `{"a":1`,
					ContentLength: 6,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
		},
		{
			name: "case-insensitive Content-Type is detected as JSON",
			interaction: Interaction{
				Request: InteractionReq{
					Headers: map[string][]string{
						"content-type":   {"Application/JSON"},
						"Content-Length": {"7"},
					},
					Body:          `{"a":1}`,
					ContentLength: 7,
				},
				Response: InteractionResp{
					Headers:       map[string][]string{"Content-Type": {"text/plain"}},
					Body:          `{"b":2}`,
					ContentLength: 7,
				},
			},
			wantChanged: true,
			wantInteraction: Interaction{
				Request: InteractionReq{
					Headers: map[string][]string{
						"content-type":   {"Application/JSON"},
						"Content-Length": {"12"},
					},
					Body: `{
  "a": 1
}`,
					ContentLength: 12,
				},
				Response: InteractionResp{
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

var errWriteFailed = errors.New("write failed")

type errorWriter struct{}

func (errorWriter) Write(p []byte) (int, error) {
	return 0, errWriteFailed
}

func TestPrintPrettyResult(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		err := PrintPrettyResult(&buf, "some/path", true)
		if err != nil {
			t.Errorf("PrintPrettyResult() unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "Formatted JSON") {
			t.Errorf("PrintPrettyResult() output = %q, want it to contain 'Formatted JSON'", buf.String())
		}
	})

	t.Run("error on write failure", func(t *testing.T) {
		t.Parallel()
		err := PrintPrettyResult(errorWriter{}, "some/path", true)
		if !errors.Is(err, errWriteFailed) {
			t.Errorf("PrintPrettyResult() error = %v, want %v", err, errWriteFailed)
		}
	})
}

func TestFormatInteraction_Nil(t *testing.T) {
	t.Parallel()
	if FormatInteraction(nil) {
		t.Error("FormatInteraction(nil) got true, want false")
	}
}
