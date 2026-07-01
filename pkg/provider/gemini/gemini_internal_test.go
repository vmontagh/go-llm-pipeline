package gemini

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/generative-ai-go/genai"
	"github.com/google/go-cmp/cmp"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

var errTest = errors.New("test sentinel")

func TestTransport_ClassifyError(t *testing.T) {
	t.Parallel()
	tr := &transport{model: DefaultModel}
	tests := []struct {
		name     string
		err      error
		wantTier provpkg.ErrorTier
	}{
		{"nil error → TierTerminal", nil, provpkg.TierTerminal},
		{"non-googleapi error → TierTerminal", errTest, provpkg.TierTerminal},
		{"429 → TierLongTransientInfinite",
			&googleapi.Error{Code: http.StatusTooManyRequests, Message: "quota"},
			provpkg.TierLongTransientInfinite},
		{"500 → TierShortTransient",
			&googleapi.Error{Code: http.StatusInternalServerError, Message: "internal"},
			provpkg.TierShortTransient},
		{"502 → TierShortTransient",
			&googleapi.Error{Code: http.StatusBadGateway, Message: "bad gateway"},
			provpkg.TierShortTransient},
		{"503 → TierShortTransient",
			&googleapi.Error{Code: http.StatusServiceUnavailable, Message: "unavailable"},
			provpkg.TierShortTransient},
		{"504 → TierShortTransient",
			&googleapi.Error{Code: http.StatusGatewayTimeout, Message: "gateway timeout"},
			provpkg.TierShortTransient},
		{"400 → TierTerminal",
			&googleapi.Error{Code: http.StatusBadRequest, Message: "bad request"},
			provpkg.TierTerminal},
		{"403 → TierTerminal",
			&googleapi.Error{Code: http.StatusForbidden, Message: "forbidden"},
			provpkg.TierTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tr.ClassifyError(provpkg.ClassifyInput{Err: tt.err}, provpkg.RetryContext{})
			if diff := cmp.Diff(tt.wantTier, got); diff != "" {
				t.Errorf("ClassifyError tier mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTransport_EnrichError(t *testing.T) {
	t.Parallel()
	tr := &transport{model: DefaultModel}
	raw := errTest

	tests := []struct {
		name    string
		tier    provpkg.ErrorTier
		wantErr error
	}{
		{"LongTransientInfinite wraps ErrRateLimitExceeded", provpkg.TierLongTransientInfinite, provpkg.ErrRateLimitExceeded},
		{"ShortTransient wraps ErrUnexpectedStatus", provpkg.TierShortTransient, provpkg.ErrUnexpectedStatus},
		{"Terminal passthrough", provpkg.TierTerminal, raw},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tr.EnrichError(tt.tier, raw, nil)
			if tt.wantErr == raw { //nolint:errorlint,err113 // checking exact pointer identity in mock test
				if got != raw { //nolint:errorlint,err113 // checking exact pointer identity in mock test
					t.Errorf("EnrichError(%v) = %v, want exact passthrough of raw error", tt.tier, got)
				}
				return
			}
			if !errors.Is(got, tt.wantErr) {
				t.Errorf("EnrichError(%v) = %v, want wrap of %v", tt.tier, got, tt.wantErr)
			}
		})
	}
}

func TestWithClockForTest(t *testing.T) {
	t.Parallel()
	opt := WithClockForTest(provpkg.RealPoolClock{})
	if opt == nil {
		t.Fatal("WithClockForTest returned nil option")
	}
}

func TestTransport_MetadataAndNewProvider(t *testing.T) {
	t.Parallel()
	p, ok := NewProvider("", nil).(*transport)
	if !ok {
		t.Fatal("NewProvider did not return *transport")
	}
	if diff := cmp.Diff("gemini/"+DefaultModel, p.Name()); diff != "" {
		t.Fatalf("Name mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(DefaultModel, p.Model()); diff != "" {
		t.Fatalf("Model mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(providerLabel, p.ProviderLabel()); diff != "" {
		t.Fatalf("ProviderLabel mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(traceRegionName, p.TraceRegionName()); diff != "" {
		t.Fatalf("TraceRegionName mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(0, p.Keys().Len()); diff != "" {
		t.Fatalf("Keys length mismatch (-want +got):\n%s", diff)
	}
}

func TestTransport_Call(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name          string
		status        int
		body          string
		wantOut       provpkg.CallOutcome
		wantText      string
		wantError     error
		wantGoogleErr bool
	}

	newClient := func(t *testing.T, status int, body string) *genai.Client {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
			rw.WriteHeader(status)
			if _, err := rw.Write([]byte(body)); err != nil {
				t.Errorf("rw.Write() = %v", err)
			}
		}))
		t.Cleanup(srv.Close)

		client, err := genai.NewClient(context.Background(),
			option.WithEndpoint(srv.URL), option.WithAPIKey("fake"))
		if err != nil {
			t.Fatalf("genai.NewClient: %v", err)
		}
		t.Cleanup(func() {
			if err := client.Close(); err != nil {
				t.Errorf("client.Close: %v", err)
			}
		})
		return client
	}

	assertResult := func(t *testing.T, tc testCase, got provpkg.CallResult) {
		t.Helper()
		if diff := cmp.Diff(tc.wantOut, got.Outcome); diff != "" {
			t.Fatalf("Outcome mismatch (-want +got):\n%s", diff)
		}
		if tc.wantText != "" {
			if diff := cmp.Diff(tc.wantText, got.Text); diff != "" {
				t.Fatalf("Text mismatch (-want +got):\n%s", diff)
			}
		}
		if tc.wantError != nil && !errors.Is(got.Err, tc.wantError) {
			t.Fatalf("error = %v, want errors.Is(_, %v)", got.Err, tc.wantError)
		}
		if tc.wantGoogleErr {
			var gerr *googleapi.Error
			if !errors.As(got.Err, &gerr) {
				t.Fatalf("error = %v, want googleapi.Error", got.Err)
			}
		}
	}

	tests := []testCase{
		{
			name:     "success",
			status:   http.StatusOK,
			body:     `{"candidates":[{"content":{"parts":[{"text":"ok"}]}}]}`,
			wantOut:  provpkg.OutcomeSuccess,
			wantText: "ok",
		},
		{
			name:      "empty candidates",
			status:    http.StatusOK,
			body:      `{"candidates":[]}`,
			wantOut:   provpkg.OutcomeTerminal,
			wantError: provpkg.ErrEmptyCandidate,
		},
		{
			name:      "invalid response part",
			status:    http.StatusOK,
			body:      `{"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/jpeg","data":"AA=="}}]}}]}`,
			wantOut:   provpkg.OutcomeTerminal,
			wantError: provpkg.ErrInvalidResponsePart,
		},
		{
			name:          "provider error",
			status:        http.StatusInternalServerError,
			body:          `{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`,
			wantOut:       provpkg.OutcomeProviderError,
			wantGoogleErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := newClient(t, tt.status, tt.body)
			tr := &transport{model: DefaultModel, tracer: trace.NewNoOpTracer()}
			got := tr.Call(context.Background(), &provpkg.Lease[*genai.Client]{Value: client}, "prompt", []byte("img"))
			assertResult(t, tt, got)
		})
	}
}

func TestNewProvider_ResponseSchema(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		opts         []ProviderOption
		wantSchema   bool
		wantType     genai.Type
		wantRequired []string
	}{
		{
			name:       "no option leaves schema nil",
			opts:       nil,
			wantSchema: false,
		},
		{
			name:       "nil schema leaves schema nil",
			opts:       []ProviderOption{WithResponseSchema(nil)},
			wantSchema: false,
		},
		{
			name: "object schema is converted",
			opts: []ProviderOption{WithResponseSchema(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"heading": map[string]any{"type": "string"},
				},
				"required": []any{"heading"},
			})},
			wantSchema:   true,
			wantType:     genai.TypeObject,
			wantRequired: []string{"heading"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr, ok := NewProvider("m", nil, tt.opts...).(*transport)
			if !ok {
				t.Fatal("NewProvider did not return *transport")
			}
			if !tt.wantSchema {
				if tr.responseSchema != nil {
					t.Errorf("responseSchema = %v, want nil", tr.responseSchema)
				}
				return
			}
			if tr.responseSchema == nil {
				t.Fatal("responseSchema = nil, want converted *genai.Schema")
			}
			if diff := cmp.Diff(tt.wantType, tr.responseSchema.Type); diff != "" {
				t.Errorf("type mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantRequired, tr.responseSchema.Required); diff != "" {
				t.Errorf("required mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
