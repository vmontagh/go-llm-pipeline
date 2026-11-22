package analyzer

import (
	"context"
	"log/slog"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// discardLogger is a *slog.Logger that discards all output, used as a test double.
var discardLogger = slog.New(slog.DiscardHandler)

type noOpTracer struct{}

type noOpRegion struct{}

func (noOpRegion) End() {}
func (noOpTracer) StartRegion(_ context.Context, _ string) interface{ End() } {
	return noOpRegion{}
}

type mockProvider struct {
	name         string
	model        string
	label        string
	traceRegion  string
	keys         *provider.KeyPool[string]
	callResult   provider.CallResult
	classifyTier provider.ErrorTier
	onCall       func(prompt string, imageData []byte)
}

func newMockProvider(callResult *provider.CallResult) *mockProvider {
	return &mockProvider{
		name:        "mock/test-model",
		model:       "test-model",
		label:       "mock",
		traceRegion: "Analyze_Mock",
		keys:        provider.NewKeyPool([]string{"key1"}),
		callResult:  *callResult,
	}
}

func (m *mockProvider) Name() string                    { return m.name }
func (m *mockProvider) Model() string                   { return m.model }
func (m *mockProvider) ProviderLabel() string           { return m.label }
func (m *mockProvider) TraceRegionName() string         { return m.traceRegion }
func (m *mockProvider) Keys() *provider.KeyPool[string] { return m.keys }
func (m *mockProvider) ClassifyError(provider.ClassifyInput, provider.RetryContext) provider.ErrorTier {
	return m.classifyTier
}
func (m *mockProvider) EnrichError(_ provider.ErrorTier, rawErr error, _ []byte) error {
	return rawErr
}
func (m *mockProvider) Call(_ context.Context, _ *provider.Lease[string], prompt string, imageData []byte) provider.CallResult {
	if m.onCall != nil {
		m.onCall(prompt, imageData)
	}
	return m.callResult
}

// messageProvider is a mockProvider that also takes typed messages.
type messageProvider struct {
	*mockProvider
	got []provider.Message
}

func (m *messageProvider) CallMessages(_ context.Context, _ *provider.Lease[string], msgs []provider.Message) provider.CallResult {
	m.got = msgs
	return m.callResult
}
