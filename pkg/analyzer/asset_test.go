package analyzer

import (
	"errors"
	"io"
	"testing"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

var errReader = errors.New("read failed")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errReader }

func TestAssetConstructors(t *testing.T) {
	t.Parallel()
	meta := map[string]any{"contentType": "text/plain"}
	tests := []struct {
		name        string
		asset       Asset
		wantID      string
		wantPayload []byte
		wantMeta    map[string]any
	}{
		{
			name:        "from bytes",
			asset:       AssetFromBytes("id-bytes", []byte("hello"), meta),
			wantID:      "id-bytes",
			wantPayload: []byte("hello"),
			wantMeta:    meta,
		},
		{
			name:        "from string",
			asset:       AssetFromString("id-string", "world", nil),
			wantID:      "id-string",
			wantPayload: []byte("world"),
		},
		{
			name:        "from reader",
			asset:       AssetFromReader("id-reader", io.LimitReader(readerOf("stream"), 6), nil),
			wantID:      "id-reader",
			wantPayload: []byte("stream"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tt.wantID, tt.asset.ID); diff != "" {
				t.Errorf("ID mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantMeta, tt.asset.Metadata); diff != "" {
				t.Errorf("Metadata mismatch (-want +got):\n%s", diff)
			}
			got, err := tt.asset.payloadBytes()
			if err != nil {
				t.Fatalf("payloadBytes() error = %v", err)
			}
			if diff := cmp.Diff(tt.wantPayload, got); diff != "" {
				t.Errorf("payloadBytes() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAssetPayloadBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		asset   Asset
		want    []byte
		wantErr error
	}{
		{
			name:  "nil payload yields nil bytes",
			asset: Asset{ID: "no-payload"},
			want:  nil,
		},
		{
			name:    "read error is wrapped",
			asset:   Asset{ID: "bad", Payload: failingReader{}},
			wantErr: errReader,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.asset.payloadBytes()
			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("payloadBytes() error mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("payloadBytes() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTemplate_AnalyzeAsset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		asset       Asset
		wantErr     error
		wantValue   string
		wantJobID   string
		wantPayload []byte
		wantCalled  bool
	}{
		{
			name:        "forwards payload and id",
			asset:       AssetFromString("asset-1", "payload-bytes", nil),
			wantValue:   "ok",
			wantJobID:   "asset-1",
			wantPayload: []byte("payload-bytes"),
			wantCalled:  true,
		},
		{
			name:       "nil payload forwarded as no data",
			asset:      Asset{ID: "asset-2"},
			wantValue:  "ok",
			wantJobID:  "asset-2",
			wantCalled: true,
		},
		{
			name:    "payload read error skips provider",
			asset:   Asset{ID: "asset-3", Payload: failingReader{}},
			wantErr: errReader,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var gotPrompt string
			var gotPayload []byte
			called := false
			p := &mockProvider{
				name:        "mock/test-model",
				model:       "m",
				label:       "mock",
				traceRegion: "Analyze_Mock",
				keys:        provider.NewKeyPool([]string{"k1"}),
				callResult:  provider.CallResult{Outcome: provider.OutcomeSuccess, Text: "ok"},
				onCall: func(prompt string, imageData []byte) {
					called = true
					gotPrompt = prompt
					gotPayload = imageData
				},
			}
			tmpl, err := NewTemplate[string, parsedResult](
				p,
				func(raw string) (parsedResult, error) { return parsedResult{Value: raw}, nil },
				WithTemplateBackOff(&backoff.StopBackOff{}),
			)
			if err != nil {
				t.Fatalf("NewTemplate: %v", err)
			}

			var gotJobID string
			publish := func(_, itemID, _ string, _ bool) { gotJobID = itemID }

			got, err := tmpl.AnalyzeAsset(t.Context(), "prompt", tt.asset, publish)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("AnalyzeAsset error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantCalled, called); diff != "" {
				t.Errorf("provider called mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != nil {
				return
			}
			if diff := cmp.Diff(tt.wantValue, got.Value); diff != "" {
				t.Errorf("Value mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantJobID, gotJobID); diff != "" {
				t.Errorf("published jobID mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff("prompt", gotPrompt); diff != "" {
				t.Errorf("prompt mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantPayload, gotPayload); diff != "" {
				t.Errorf("payload mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type stringReader struct{ s string }

func readerOf(s string) io.Reader { return &stringReader{s: s} }

func (r *stringReader) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}
