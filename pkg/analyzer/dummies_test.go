package analyzer

import (
	"context"
	"iter"
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

// streamProvider is a mockProvider that streams chunks. Each attempt to open
// the stream takes the next of opens.
type streamProvider struct {
	*mockProvider
	opens  []provider.CallResult
	chunks []provider.Chunk
	got    []provider.Message
}

func (s *streamProvider) Stream(_ context.Context, _ *provider.Lease[string], msgs []provider.Message) (iter.Seq2[provider.Chunk, error], provider.CallResult) {
	s.got = msgs
	res := s.opens[0]
	s.opens = s.opens[1:]
	if res.Outcome != provider.OutcomeSuccess {
		return nil, res
	}
	return func(yield func(provider.Chunk, error) bool) {
		for _, c := range s.chunks {
			if !yield(c, nil) {
				return
			}
		}
	}, res
}
