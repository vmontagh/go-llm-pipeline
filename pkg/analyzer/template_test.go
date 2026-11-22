package analyzer

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

type parsedResult struct {
	Value string
}

type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time { return c.now }

func TestTemplate_Analyze(t *testing.T) {
	t.Parallel()
	validText := "ok"
	tests := []struct {
		name         string
		callResult   provider.CallResult
		classifyTier provider.ErrorTier
		keys         []string
		imageData    []byte
		jobID        string
		cancelCtx    bool
		wantErr      error
		wantValue    string
		wantName     string
		wantPublish  string
	}{
		{
			name:       "Name returns provider name",
			callResult: provider.CallResult{Outcome: provider.OutcomeSuccess, Text: validText},
			keys:       []string{"k1"},
			wantName:   "mock/test-model",
			wantValue:  "ok",
		},
		{
			name:       "success with data",
			callResult: provider.CallResult{Outcome: provider.OutcomeSuccess, Text: validText},
			keys:       []string{"k1"},
			imageData:  []byte("x"),
			jobID:      "job1",
			wantValue:  "ok",
		},
		{
			name:        "LLM_STARTED publish event emitted",
			callResult:  provider.CallResult{Outcome: provider.OutcomeSuccess, Text: validText},
			keys:        []string{"k1"},
			jobID:       "job2",
			wantPublish: llmStartedEventType,
			wantValue:   "ok",
		},
		{
			name: "terminal outcome propagates sentinel",
			callResult: provider.CallResult{
				Outcome: provider.OutcomeTerminal,
				Err:     backoff.Permanent(ErrRateLimitExceeded),
			},
			keys:    []string{"k1"},
			wantErr: ErrRateLimitExceeded,
		},
		{
			name: "provider error short-transient exits via StopBackOff",
			callResult: provider.CallResult{
				Outcome: provider.OutcomeProviderError,
				Err:     ErrRateLimitExceeded,
			},
			classifyTier: provider.TierShortTransient,
			keys:         []string{"k1"},
			wantErr:      ErrRateLimitExceeded,
		},
		{
			name: "missing API key returns ErrMissingAPIKey",
			keys: nil,
			callResult: provider.CallResult{
				Outcome: provider.OutcomeSuccess,
				Text:    validText,
			},
			wantErr: ErrMissingAPIKey,
		},
		{
			name: "context cancellation returns wrapped context.Canceled",
			callResult: provider.CallResult{
				Outcome: provider.OutcomeProviderError,
				Err:     context.Canceled,
			},
			keys:      []string{"k1"},
			cancelCtx: true,
			wantErr:   context.Canceled,
		},
		{
			name: "unknown outcome returns error",
			callResult: provider.CallResult{
				Outcome: provider.CallOutcome(99),
			},
			keys:    []string{"k1"},
			wantErr: cmpopts.AnyError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &mockProvider{
				name:         "mock/test-model",
				model:        "m",
				label:        "mock",
				traceRegion:  "Analyze_Mock",
				keys:         provider.NewKeyPool(tt.keys),
				callResult:   tt.callResult,
				classifyTier: tt.classifyTier,
			}
			mustTemplate := func(p provider.Provider[string], parser func(string) (parsedResult, error), opts ...TemplateConfigurator) *Template[string, parsedResult] {
				tInst, err := NewTemplate[string, parsedResult](p, parser, opts...)
				if err != nil {
					t.Fatalf("NewTemplate() = %v, want nil", err)
				}
				return tInst
			}
			tmpl := mustTemplate(p, func(raw string) (parsedResult, error) { return parsedResult{Value: raw}, nil }, WithTemplateBackOff(&backoff.StopBackOff{}))

			if tt.wantName != "" {
				if diff := cmp.Diff(tt.wantName, tmpl.Name()); diff != "" {
					t.Errorf("Name() mismatch (-want +got):\n%s", diff)
				}
			}
			if tmpl.logger == nil {
				t.Error("default logger must not be nil")
			}
			if tmpl.tracer == nil {
				t.Error("default tracer must not be nil")
			}
			if tmpl.Provider() != p {
				t.Error("Provider() must return the configured provider")
			}

			ctx := context.Background()
			if tt.cancelCtx {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			var gotPublish string
			publish := func(eventType, _, _ string, _ bool) { gotPublish = eventType }

			got, err := tmpl.Analyze(ctx, "prompt", tt.imageData, tt.jobID, publish)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}
			if tt.wantErr != nil {
				return
			}
			if tt.wantValue != "" {
				if diff := cmp.Diff(tt.wantValue, got.Value); diff != "" {
					t.Errorf("Value mismatch (-want +got):\n%s", diff)
				}
			}
			if tt.wantPublish != "" {
				if diff := cmp.Diff(tt.wantPublish, gotPublish); diff != "" {
					t.Errorf("first publish event mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestTemplate_AnalyzeMessages(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{
		provider.TextMessage(provider.RoleSystem, "s"),
		{Role: provider.RoleUser, Parts: []provider.Part{{Text: "u"}, {Image: []byte("img")}}},
	}
	tests := []struct {
		name     string
		messages bool
		guards   int
		noKeys   bool
		msgs     []provider.Message
		want     parsedResult
		wantMsgs []provider.Message
		wantLog  string
		wantErr  error
	}{
		{
			name:     "sends messages to a MessageCaller",
			messages: true,
			msgs:     msgs,
			want:     parsedResult{Value: "ok"},
			wantMsgs: msgs,
			wantLog:  "system: s\n\tuser: u [image]",
		},
		{name: "rejects invalid messages", messages: true, wantErr: provider.ErrInvalidMessages},
		{name: "rejects a provider without messages", msgs: msgs, wantErr: provider.ErrMessagesUnsupported},
		{name: "rejects a guarded provider without messages", guards: 1, noKeys: true, msgs: msgs, wantErr: provider.ErrMessagesUnsupported},
		{name: "rejects a nested guarded provider without messages", guards: 2, noKeys: true, msgs: msgs, wantErr: provider.ErrMessagesUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockProvider(&provider.CallResult{Outcome: provider.OutcomeSuccess, Text: "ok"})
			mp := &messageProvider{mockProvider: mock}
			p := provider.Provider[string](mock)
			if tt.messages {
				p = mp
			}
			if tt.noKeys {
				mock.keys = provider.NewKeyPool[string](nil)
			}
			for range tt.guards {
				g, err := provider.NewGuard(p)
				if err != nil {
					t.Fatalf("NewGuard() error = %v", err)
				}
				p = g
			}
			logs := &capturingHandler{}
			parse := func(raw string) (parsedResult, error) { return parsedResult{Value: raw}, nil }
			tmpl, err := NewTemplate[string, parsedResult](p, parse, WithTemplateLogger(slog.New(logs)))
			if err != nil {
				t.Fatalf("NewTemplate() = %v, want nil", err)
			}

			got, err := tmpl.AnalyzeMessages(t.Context(), tt.msgs, "job", nil)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("AnalyzeMessages() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("AnalyzeMessages() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantMsgs, mp.got); diff != "" {
				t.Errorf("sent messages mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantLog, logs.attr("prompt sent", "body")); diff != "" {
				t.Errorf("logged prompt mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTemplate_callOnce_AllKeysCoolingDown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		buildPool     func(t *testing.T) *provider.KeyPool[string]
		threshold     time.Duration
		wantPermanent bool
	}{
		{
			name: "immediate retry stays in L1 when a key is already ready",
			buildPool: func(t *testing.T) *provider.KeyPool[string] {
				clk := &advancingClock{
					now:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
					step: time.Second,
				}
				pool := provider.NewKeyPoolWithClock([]string{"k"}, clk)
				lease, err := pool.Acquire(context.Background())
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				lease.MarkCooldown(provider.TierPolicy{Cooldown: 1500 * time.Millisecond}, 0)
				return pool
			},
			threshold:     defaultShortCooldownRetryThreshold,
			wantPermanent: false,
		},
		{
			name: "short cooldown stays in L1 when within threshold",
			buildPool: func(t *testing.T) *provider.KeyPool[string] {
				clk := newFakeClock()
				pool := provider.NewKeyPoolWithClock([]string{"k"}, clk)
				lease, err := pool.Acquire(context.Background())
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				lease.MarkCooldown(provider.TierPolicy{Cooldown: 2 * time.Second}, 0)
				return pool
			},
			threshold:     defaultShortCooldownRetryThreshold,
			wantPermanent: false,
		},
		{
			name: "long cooldown escalates beyond threshold",
			buildPool: func(t *testing.T) *provider.KeyPool[string] {
				clk := newFakeClock()
				pool := provider.NewKeyPoolWithClock([]string{"k"}, clk)
				lease, err := pool.Acquire(context.Background())
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				lease.MarkCooldown(provider.TierPolicy{Cooldown: 6 * time.Second}, 0)
				return pool
			},
			threshold:     5 * time.Second,
			wantPermanent: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := tt.buildPool(t)
			tmpl, err := NewTemplate[string, parsedResult](
				&mockProvider{
					name:        "mock/test-model",
					model:       "test-model",
					label:       "mock",
					traceRegion: "Analyze_Mock",
					keys:        pool,
				},
				func(raw string) (parsedResult, error) { return parsedResult{Value: raw}, nil },
				WithTemplateShortCooldownRetryThreshold(tt.threshold),
			)
			if err != nil {
				t.Fatalf("NewTemplate() = %v, want nil", err)
			}

			_, err = tmpl.callOnce(context.Background(), &callOnceParams[string]{prompt: "prompt"})
			if diff := cmp.Diff(provider.ErrAllKeysCoolingDown, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("callOnce error mismatch (-want +got):\n%s", diff)
			}

			var permanentErr *backoff.PermanentError
			gotPermanent := errors.As(err, &permanentErr)
			if diff := cmp.Diff(tt.wantPermanent, gotPermanent); diff != "" {
				t.Errorf("permanent mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

type advancingClock struct {
	now  time.Time
	step time.Duration
}

func (c *advancingClock) Now() time.Time {
	now := c.now
	c.now = c.now.Add(c.step)
	return now
}

func TestWithTemplateScrubFunc(t *testing.T) {
	t.Parallel()

	logHandler := &capturingHandler{}
	lg := slog.New(logHandler)

	p := newMockProvider(&provider.CallResult{
		Outcome: provider.OutcomeSuccess,
		Text:    "response: secret=abc123",
	})
	p.keys = provider.NewKeyPool([]string{"k1"})

	scrub := func(s string) string {
		return strings.ReplaceAll(s, "secret=abc123", "secret=[REDACTED]")
	}

	tmpl, err := NewTemplate[string, parsedResult](
		p,
		func(raw string) (parsedResult, error) { return parsedResult{Value: raw}, nil },
		WithTemplateBackOff(&backoff.StopBackOff{}),
		WithTemplateLogger(lg),
		WithTemplateScrubFunc(scrub),
	)
	if err != nil {
		t.Fatalf("NewTemplate: %v", err)
	}

	got, err := tmpl.Analyze(t.Context(), "prompt: secret=abc123", nil, "job1", func(string, string, string, bool) {})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Scrubbing must not interfere with the returned result.
	if want := "response: secret=abc123"; got.Value != want {
		t.Errorf("Analyze() value = %q, want %q", got.Value, want)
	}

	// Verify the scrub function was applied: raw secret must not appear in any log record.
	for recIdx, rec := range logHandler.records {
		for i, a := range rec.attrs {
			if strings.Contains(a.Value.String(), "secret=abc123") {
				t.Errorf("log record %d attr %d contains unscrubbed secret: %q", recIdx, i, a.Value.String())
			}
		}
	}
}

// capturingHandler is a slog.Handler that records all logged attributes for inspection.
type capturingHandler struct {
	records []logRecord
}

type logRecord struct {
	msg   string
	attrs []slog.Attr
}

// attr returns the value of key in the first record with message msg, or "".
func (h *capturingHandler) attr(msg, key string) string {
	for _, r := range h.records {
		if r.msg != msg {
			continue
		}
		for _, a := range r.attrs {
			if a.Key == key {
				return a.Value.String()
			}
		}
	}
	return ""
}

func (h *capturingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error { //nolint:gocritic // slog.Handler interface requires slog.Record by value
	lr := logRecord{msg: r.Message}
	r.Attrs(func(a slog.Attr) bool {
		lr.attrs = append(lr.attrs, a)
		return true
	})
	h.records = append(h.records, lr)
	return nil
}

func (h *capturingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

func (h *capturingHandler) WithGroup(name string) slog.Handler {
	return h
}

func TestNewTemplate_Validation(t *testing.T) {
	t.Parallel()
	mockP := newMockProvider(&provider.CallResult{})

	tests := []struct {
		name    string
		p       provider.Provider[string]
		parser  func(string) (string, error)
		wantErr error
	}{
		{
			name:    "nil provider returns ErrNilProvider",
			parser:  func(string) (string, error) { return "", nil },
			wantErr: ErrNilProvider,
		},
		{
			name:    "nil parser returns ErrNilParser",
			p:       mockP,
			wantErr: ErrNilParser,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewTemplate[string, string](tt.p, tt.parser)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("NewTemplate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
