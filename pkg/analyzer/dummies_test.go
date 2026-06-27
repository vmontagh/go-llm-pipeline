package analyzer

import (
	"context"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

type dummyLogger struct{}

func (d *dummyLogger) Debugf(format string, args ...interface{}) {}
func (d *dummyLogger) Infof(format string, args ...interface{})  {}
func (d *dummyLogger) Warnf(format string, args ...interface{})  {}
func (d *dummyLogger) Errorf(format string, args ...interface{}) {}

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
func (m *mockProvider) Call(_ context.Context, _ *provider.Lease[string], _ string, _ []byte) provider.CallResult {
	return m.callResult
}
