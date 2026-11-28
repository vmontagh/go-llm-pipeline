package analyzer

import (
	"context"
	"errors"
	"strings"
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
			name:        "from bytes preserves metadata",
			asset:       AssetFromBytes("id-bytes", []byte("hello"), meta),
			wantID:      "id-bytes",
			wantPayload: []byte("hello"),
			wantMeta:    meta,
		},
		{
			name:        "from string defaults content type to text",
			asset:       AssetFromString("id-string", "world", nil),
			wantID:      "id-string",
			wantPayload: []byte("world"),
			wantMeta:    map[string]any{"contentType": "text/plain; charset=utf-8"},
		},
		{
			name:        "from string keeps explicit content type",
			asset:       AssetFromString("id-json", "{}", map[string]any{"contentType": "application/json"}),
			wantID:      "id-json",
			wantPayload: []byte("{}"),
			wantMeta:    map[string]any{"contentType": "application/json"},
		},
		{
			name:        "from reader has no default metadata",
			asset:       AssetFromReader("id-reader", strings.NewReader("stream"), nil),
			wantID:      "id-reader",
			wantPayload: []byte("stream"),
			wantMeta:    nil,
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
			got, err := tt.asset.payloadBytes(t.Context(), 0)
			if err != nil {
				t.Fatalf("payloadBytes() error = %v", err)
			}
			if diff := cmp.Diff(tt.wantPayload, got); diff != "" {
				t.Errorf("payloadBytes() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAssetFromStringDoesNotMutateCallerMetadata(t *testing.T) {
	t.Parallel()
	caller := map[string]any{"source": "unit"}
	_ = AssetFromString("id", "hi", caller)
	if diff := cmp.Diff(map[string]any{"source": "unit"}, caller); diff != "" {
		t.Errorf("caller metadata mutated (-want +got):\n%s", diff)
	}
}

func TestAssetPayloadBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		asset   Asset
		limit   int64
		want    []byte
		wantErr error
	}{
		{
			name:  "nil payload yields nil bytes",
			asset: Asset{ID: "no-payload"},
			want:  nil,
		},
		{
			name:  "under limit reads fully",
			asset: AssetFromBytes("small", []byte("hello"), nil),
			limit: 16,
			want:  []byte("hello"),
		},
		{
			name:    "over limit returns ErrPayloadTooLarge",
			asset:   AssetFromBytes("big", []byte("0123456789"), nil),
			limit:   4,
			wantErr: ErrPayloadTooLarge,
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
			got, err := tt.asset.payloadBytes(t.Context(), tt.limit)
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

func TestAssetPayloadBytes_ContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := AssetFromString("id", "data", nil).payloadBytes(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("payloadBytes() error = %v, want context.Canceled", err)
	}
}

func TestTemplate_AnalyzeAsset(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		asset       Asset
		maxBytes    int64
		wantErr     error
		wantJobID   string
		wantPrompt  string
		wantPayload []byte
		wantCalled  bool
	}{
		{
			name:       "textual asset is folded into the prompt",
			asset:      AssetFromString("asset-text", "document body", nil),
			wantJobID:  "asset-text",
			wantPrompt: "prompt\n\ndocument body",
			wantCalled: true,
		},
		{
			name:        "binary asset is forwarded as image data",
			asset:       AssetFromBytes("asset-img", []byte{0xFF, 0xD8, 0xFF}, map[string]any{"contentType": "image/jpeg"}),
			wantJobID:   "asset-img",
			wantPrompt:  "prompt",
			wantPayload: []byte{0xFF, 0xD8, 0xFF},
			wantCalled:  true,
		},
		{
			name:       "nil payload forwarded as no data",
			asset:      Asset{ID: "asset-empty"},
			wantJobID:  "asset-empty",
			wantPrompt: "prompt",
			wantCalled: true,
		},
		{
			name:    "payload read error skips provider",
			asset:   Asset{ID: "asset-bad", Payload: failingReader{}},
			wantErr: errReader,
		},
		{
			name:     "oversized payload skips provider",
			asset:    AssetFromBytes("asset-huge", []byte("0123456789"), map[string]any{"contentType": "image/jpeg"}),
			maxBytes: 4,
			wantErr:  ErrPayloadTooLarge,
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
			b := echoBuilder(p).WithBackOff(&backoff.StopBackOff{})
			if tt.maxBytes != 0 {
				b.WithMaxAssetBytes(tt.maxBytes)
			}
			tmpl := buildTemplate(t, b)

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
			if diff := cmp.Diff("ok", got.Value); diff != "" {
				t.Errorf("Value mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantJobID, gotJobID); diff != "" {
				t.Errorf("published jobID mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantPrompt, gotPrompt); diff != "" {
				t.Errorf("prompt mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantPayload, gotPayload); diff != "" {
				t.Errorf("payload mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
