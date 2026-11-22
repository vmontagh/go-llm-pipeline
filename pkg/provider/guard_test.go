package provider_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

const (
	statusOverloaded = 529 // fakeGuarded classifies it as TierShortRateLimit.
	openFor          = 30 * time.Second
)

var (
	okResult         = provider.CallResult{Text: "ok"}
	transientResult  = provider.CallResult{Err: errTestSentinel, Outcome: provider.OutcomeProviderError, StatusCode: http.StatusInternalServerError}
	tooManyResult    = provider.CallResult{Err: errTestSentinel, Outcome: provider.OutcomeProviderError, StatusCode: http.StatusTooManyRequests}
	retryAfterResult = provider.CallResult{Err: errTestSentinel, Outcome: provider.OutcomeProviderError, StatusCode: http.StatusServiceUnavailable, RetryAfter: time.Minute}
	overloadedResult = provider.CallResult{Err: errTestSentinel, Outcome: provider.OutcomeProviderError, StatusCode: statusOverloaded}
	badRequestResult = provider.CallResult{Err: errTestSentinel, Outcome: provider.OutcomeProviderError, StatusCode: http.StatusBadRequest}
	terminalResult   = provider.CallResult{Err: backoff.Permanent(errTestSentinel), Outcome: provider.OutcomeTerminal}
)

// fakeGuardClock fires After channels when advance passes their deadline.
type fakeGuardClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeTimer
}

type fakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newFakeGuardClock() *fakeGuardClock {
	return &fakeGuardClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeGuardClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeGuardClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, fakeTimer{at: c.now.Add(d), ch: ch})
	return ch
}

func (c *fakeGuardClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	var pending []fakeTimer
	for _, tm := range c.timers {
		if tm.at.After(c.now) {
			pending = append(pending, tm)
			continue
		}
		tm.ch <- c.now
	}
	c.timers = pending
}

// step scripts one provider call.
type step struct {
	before  time.Duration // clock advance before the call
	latency time.Duration // clock advance inside the call
	gate    chan struct{} // if set, the call signals entered and blocks until closed
	cancel  bool          // the caller's context is canceled during the call
	res     provider.CallResult
}

// fakeGuarded returns scripted results; only Call and ClassifyError are used.
type fakeGuarded struct {
	provider.Provider[string]
	clock   *fakeGuardClock
	entered chan struct{}

	mu     sync.Mutex
	steps  []step
	calls  int
	keys   []string // lease values seen by Call
	cancel context.CancelFunc
}

func (f *fakeGuarded) ClassifyError(in provider.ClassifyInput, _ provider.RetryContext) provider.ErrorTier {
	switch in.StatusCode {
	case http.StatusBadRequest:
		return provider.TierTerminal
	case statusOverloaded:
		return provider.TierShortRateLimit
	default:
		return provider.TierShortTransient
	}
}

func (f *fakeGuarded) Call(_ context.Context, lease *provider.Lease[string], _ string, _ []byte) provider.CallResult {
	f.mu.Lock()
	s := f.steps[f.calls]
	f.calls++
	if lease != nil {
		f.keys = append(f.keys, lease.Value)
	}
	if s.cancel {
		f.cancel()
	}
	f.mu.Unlock()
	if s.gate != nil {
		f.entered <- struct{}{}
		<-s.gate
	}
	f.clock.advance(s.latency)
	return s.res
}

func (f *fakeGuarded) ProviderLabel() string { return "fake" }

// messageGuarded is a fakeGuarded that also takes typed messages.
type messageGuarded struct {
	*fakeGuarded
	got []provider.Message
}

func (m *messageGuarded) CallMessages(ctx context.Context, lease *provider.Lease[string], msgs []provider.Message) provider.CallResult {
	m.got = msgs
	return m.Call(ctx, lease, "", nil)
}

func (f *fakeGuarded) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeAdder records counters and signals each guard wait.
type fakeAdder struct {
	waited chan struct{}

	mu     sync.Mutex
	counts map[string]int64
}

func (a *fakeAdder) Add(name string, delta int64) {
	a.mu.Lock()
	a.counts[name] += delta
	a.mu.Unlock()
	if name == provider.MetricGuardWaits {
		a.waited <- struct{}{}
	}
}

func (a *fakeAdder) snapshot() map[string]int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.counts
}

type guardFixture struct {
	guard   *provider.Guard[string]
	fake    *fakeGuarded
	clock   *fakeGuardClock
	metrics *fakeAdder
}

func newGuardFixture(t *testing.T, steps []step, opts ...provider.GuardOption) *guardFixture {
	t.Helper()
	clock := newFakeGuardClock()
	fake := &fakeGuarded{clock: clock, steps: steps, entered: make(chan struct{}, 1)}
	metrics := &fakeAdder{counts: map[string]int64{}, waited: make(chan struct{}, len(steps))}
	opts = append(opts, provider.WithGuardClock(clock), provider.WithGuardMetrics(metrics))
	g, err := provider.NewGuard[string](fake, opts...)
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	return &guardFixture{guard: g, fake: fake, clock: clock, metrics: metrics}
}

// runSteps makes one call per step, advancing the clock by step.before first.
// Gated steps stay in flight until all n calls have been made.
func (f *guardFixture) runSteps(t *testing.T, n int) {
	t.Helper()
	var gated []step
	var done []<-chan provider.CallResult
	for _, s := range f.fake.steps[:n] {
		f.clock.advance(s.before)
		if s.gate != nil {
			gated = append(gated, s)
			done = append(done, f.goCall(t.Context()))
			<-f.fake.entered
			continue
		}
		ctx, cancel := context.WithCancel(t.Context())
		f.fake.mu.Lock()
		f.fake.cancel = cancel
		f.fake.mu.Unlock()
		f.guard.Call(ctx, nil, "", nil)
		cancel()
	}
	for i, s := range gated {
		close(s.gate)
		<-done[i]
	}
}

// goCall runs Call in a goroutine and returns its result channel.
func (f *guardFixture) goCall(ctx context.Context) <-chan provider.CallResult {
	out := make(chan provider.CallResult, 1)
	go func() { out <- f.guard.Call(ctx, nil, "", nil) }()
	return out
}

func repeat(n int, res *provider.CallResult) []step {
	steps := make([]step, n)
	for i := range steps {
		steps[i] = step{res: *res}
	}
	return steps
}

func TestNewGuard(t *testing.T) {
	tests := []struct {
		name    string
		nilProv bool
		opts    []provider.GuardOption
		wantErr error
	}{
		{name: "defaults"},
		{name: "nilProvider", nilProv: true, wantErr: provider.ErrInvalidGuardConfig},
		{name: "zeroMinLimit", opts: []provider.GuardOption{provider.WithConcurrencyLimits(0, 4)}, wantErr: provider.ErrInvalidGuardConfig},
		{name: "maxBelowMin", opts: []provider.GuardOption{provider.WithConcurrencyLimits(4, 2)}, wantErr: provider.ErrInvalidGuardConfig},
		{name: "zeroFailures", opts: []provider.GuardOption{provider.WithBreaker(0, openFor)}, wantErr: provider.ErrInvalidGuardConfig},
		{name: "zeroOpenFor", opts: []provider.GuardOption{provider.WithBreaker(1, 0)}, wantErr: provider.ErrInvalidGuardConfig},
		{name: "negativeLatencyTarget", opts: []provider.GuardOption{provider.WithLatencyTarget(-time.Second)}, wantErr: provider.ErrInvalidGuardConfig},
		{name: "nilClock", opts: []provider.GuardOption{provider.WithGuardClock(nil)}, wantErr: provider.ErrInvalidGuardConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p provider.Provider[string] = &fakeGuarded{}
			if tt.nilProv {
				p = nil
			}

			_, err := provider.NewGuard(p, tt.opts...)

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("NewGuard() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGuardAdapts(t *testing.T) {
	type outcome struct {
		Limit   int
		State   provider.BreakerState
		Metrics map[string]int64
	}
	// Seven successes from limit 1 reach about 4.1.
	toFour := repeat(7, &okResult)
	tests := []struct {
		name         string
		opts         []provider.GuardOption
		steps        []step
		advanceAfter time.Duration
		want         outcome
	}{
		{
			name:  "successesGrowLimit",
			opts:  []provider.GuardOption{provider.WithConcurrencyLimits(1, 4)},
			steps: repeat(3, &okResult),
			want:  outcome{Limit: 2, State: provider.BreakerClosed},
		},
		{
			name:  "limitCappedAtMax",
			opts:  []provider.GuardOption{provider.WithConcurrencyLimits(1, 2)},
			steps: repeat(5, &okResult),
			want:  outcome{Limit: 2, State: provider.BreakerClosed},
		},
		{
			name:  "tooManyRequestsHalves",
			steps: append(toFour, step{res: tooManyResult}),
			want:  outcome{Limit: 2, State: provider.BreakerClosed, Metrics: map[string]int64{provider.MetricGuardLimitDecreases: 1}},
		},
		{
			name:  "retryAfterHalves",
			steps: append(toFour, step{res: retryAfterResult}),
			want:  outcome{Limit: 2, State: provider.BreakerClosed, Metrics: map[string]int64{provider.MetricGuardLimitDecreases: 1}},
		},
		{
			name:  "rateLimitTierHalves",
			steps: append(toFour, step{res: overloadedResult}),
			want:  outcome{Limit: 2, State: provider.BreakerClosed, Metrics: map[string]int64{provider.MetricGuardLimitDecreases: 1}},
		},
		{
			name:  "plainTransientKeepsLimit",
			steps: append(toFour, step{res: transientResult}),
			want:  outcome{Limit: 4, State: provider.BreakerClosed},
		},
		{
			name:  "limitFloorsAtMin",
			steps: []step{{res: tooManyResult}},
			want:  outcome{Limit: 1, State: provider.BreakerClosed},
		},
		{
			name:  "slowSuccessShrinks",
			opts:  []provider.GuardOption{provider.WithLatencyTarget(time.Second)},
			steps: append(toFour, step{res: okResult, latency: 2 * time.Second}),
			want:  outcome{Limit: 3, State: provider.BreakerClosed, Metrics: map[string]int64{provider.MetricGuardLimitDecreases: 1}},
		},
		{
			name:  "canceledFailureIgnored",
			opts:  []provider.GuardOption{provider.WithBreaker(1, openFor)},
			steps: append(toFour, step{res: tooManyResult, cancel: true}),
			want:  outcome{Limit: 4, State: provider.BreakerClosed},
		},
		{
			name:  "terminalOutcomesIgnored",
			opts:  []provider.GuardOption{provider.WithBreaker(1, openFor)},
			steps: []step{{res: terminalResult}, {res: badRequestResult}},
			want:  outcome{Limit: 1, State: provider.BreakerClosed},
		},
		{
			name:  "failuresOpenBreaker",
			opts:  []provider.GuardOption{provider.WithBreaker(2, openFor)},
			steps: repeat(2, &transientResult),
			want:  outcome{Limit: 1, State: provider.BreakerOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name:  "successResetsFailures",
			opts:  []provider.GuardOption{provider.WithBreaker(2, openFor)},
			steps: []step{{res: transientResult}, {res: okResult}, {res: transientResult}},
			want:  outcome{Limit: 2, State: provider.BreakerClosed},
		},
		{
			name:         "breakerHalfOpensAfterWindow",
			opts:         []provider.GuardOption{provider.WithBreaker(1, openFor)},
			steps:        []step{{res: transientResult}},
			advanceAfter: openFor,
			want:         outcome{Limit: 1, State: provider.BreakerHalfOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name:         "retryAfterExtendsOpen",
			opts:         []provider.GuardOption{provider.WithBreaker(1, openFor)},
			steps:        []step{{res: retryAfterResult}},
			advanceAfter: openFor,
			want:         outcome{Limit: 1, State: provider.BreakerOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name:         "retryAfterExtendsOpenBreaker",
			opts:         []provider.GuardOption{provider.WithConcurrencyLimits(2, 4), provider.WithBreaker(1, openFor)},
			steps:        []step{{gate: make(chan struct{}), res: retryAfterResult}, {res: transientResult}},
			advanceAfter: openFor,
			want:         outcome{Limit: 2, State: provider.BreakerOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name: "lateRetryAfterReopensHalfOpen",
			opts: []provider.GuardOption{provider.WithConcurrencyLimits(2, 4), provider.WithBreaker(1, openFor)},
			steps: []step{
				{gate: make(chan struct{}), res: retryAfterResult},
				{res: transientResult},
				{before: openFor, gate: make(chan struct{}), res: okResult}, // the probe
			},
			want: outcome{Limit: 2, State: provider.BreakerOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name: "failedProbeKeepsLaterRetryAfter",
			opts: []provider.GuardOption{provider.WithConcurrencyLimits(2, 4), provider.WithBreaker(1, openFor)},
			steps: []step{
				{gate: make(chan struct{}), res: retryAfterResult},
				{res: transientResult},
				{before: openFor + time.Second, gate: make(chan struct{}), res: transientResult}, // the probe
			},
			advanceAfter: openFor + time.Second,
			want:         outcome{Limit: 2, State: provider.BreakerOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 2}},
		},
		{
			name: "lateTransientKeepsProbe",
			opts: []provider.GuardOption{provider.WithConcurrencyLimits(2, 4), provider.WithBreaker(1, openFor)},
			steps: []step{
				{gate: make(chan struct{}), res: transientResult},
				{res: transientResult},
				{before: openFor + time.Second, gate: make(chan struct{}), res: okResult}, // the probe
			},
			want: outcome{Limit: 2, State: provider.BreakerClosed, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name:  "probeSuccessCloses",
			opts:  []provider.GuardOption{provider.WithBreaker(1, openFor)},
			steps: []step{{res: transientResult}, {before: openFor, res: okResult}},
			want:  outcome{Limit: 2, State: provider.BreakerClosed, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 1}},
		},
		{
			name:  "probeFailureReopens",
			opts:  []provider.GuardOption{provider.WithBreaker(1, openFor)},
			steps: []step{{res: transientResult}, {before: openFor, res: transientResult}},
			want:  outcome{Limit: 1, State: provider.BreakerOpen, Metrics: map[string]int64{provider.MetricGuardBreakerOpens: 2}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGuardFixture(t, tt.steps, tt.opts...)

			f.runSteps(t, len(tt.steps))
			f.clock.advance(tt.advanceAfter)

			got := outcome{Limit: f.guard.Limit(), State: f.guard.State(), Metrics: f.metrics.snapshot()}
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Guard after %d calls mismatch (-want +got):\n%s", len(tt.steps), diff)
			}
		})
	}
}

func TestGuardCallCanceledWhileWaiting(t *testing.T) {
	type outcome struct {
		Outcome provider.CallOutcome
		Err     error
		Calls   int
		Cooling bool // the key cooled by another call is still cooling after Release
	}
	tests := []struct {
		name  string
		opts  []provider.GuardOption
		prior []step // run to completion first
		block bool   // the call after prior blocks in the provider
		waits bool   // the call waits before it is canceled; otherwise it is canceled first
	}{
		{name: "canceledBeforeCall"},
		{name: "slotBusy", block: true, waits: true},
		{name: "breakerOpen", opts: []provider.GuardOption{provider.WithBreaker(1, openFor)}, prior: []step{{res: transientResult}}, waits: true},
		{
			name:  "probeInFlight",
			opts:  []provider.GuardOption{provider.WithConcurrencyLimits(2, 4), provider.WithBreaker(1, openFor)},
			prior: []step{{res: transientResult}},
			block: true,
			waits: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := make(chan struct{})
			f := newGuardFixture(t, append(tt.prior, step{gate: gate, res: okResult}), tt.opts...)
			f.runSteps(t, len(tt.prior))
			want := outcome{Outcome: provider.OutcomeTerminal, Err: context.Canceled, Calls: len(tt.prior), Cooling: tt.waits}
			pool := provider.NewKeyPoolWithClock([]string{"a"}, f.clock)
			lease := acquireLease(t, pool)
			if tt.block {
				f.clock.advance(openFor)
				done := f.goCall(t.Context())
				<-f.fake.entered
				t.Cleanup(func() { close(gate); <-done })
				want.Calls++
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if !tt.waits {
				cancel()
			}

			out := make(chan provider.CallResult, 1)
			go func() { out <- f.guard.Call(ctx, lease, "", nil) }()
			if tt.waits {
				<-f.metrics.waited
				coolKey(t, pool, lease.Index, time.Hour) // another call is throttled meanwhile
				cancel()
			}
			res := <-out
			lease.Release() // as Template.callOnce does after a terminal outcome

			got := outcome{Outcome: res.Outcome, Err: res.Err, Calls: f.fake.callCount(), Cooling: pool.MinCooldown() > 0}
			if diff := cmp.Diff(want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Call(canceled) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGuardWaitersResume(t *testing.T) {
	tests := []struct {
		name    string
		opts    []provider.GuardOption
		prior   []step        // run to completion first
		waiters int           // calls that wait before the gated call is released
		advance time.Duration // clock advance once they wait
	}{
		{name: "slotReleased", waiters: 1},
		{
			name:    "breakerProbeSucceeds",
			opts:    []provider.GuardOption{provider.WithConcurrencyLimits(2, 4), provider.WithBreaker(1, openFor)},
			prior:   []step{{res: transientResult}},
			waiters: 2,
			advance: openFor,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := make(chan struct{})
			f := newGuardFixture(t, append(tt.prior, step{gate: gate, res: okResult}, step{res: okResult}), tt.opts...)
			f.runSteps(t, len(tt.prior))
			first, second := f.goCall(t.Context()), f.goCall(t.Context())
			for range tt.waiters {
				<-f.metrics.waited
			}

			f.clock.advance(tt.advance)
			<-f.fake.entered
			close(gate)

			type outcome struct {
				Results []provider.CallResult
				State   provider.BreakerState
			}
			got := outcome{Results: []provider.CallResult{<-first, <-second}, State: f.guard.State()}
			want := outcome{Results: []provider.CallResult{okResult, okResult}, State: provider.BreakerClosed}
			if diff := cmp.Diff(want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Call() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGuardUsesReadyKey(t *testing.T) {
	const cooldown = 10 * time.Second
	type outcome struct {
		Result  provider.CallResult
		Keys    []string
		Cooling bool // the key is still cooling after Release
	}
	tests := []struct {
		name     string
		keys     []string
		cool     bool // the leased key enters cooldown before the call
		wantWait bool // the call waits for the cooldown to end
		cancel   bool // the call is canceled while waiting instead
		release  bool // another lease on the key is released while waiting instead
		want     outcome
	}{
		{name: "keyReady", keys: []string{"a", "b"}, want: outcome{Result: okResult, Keys: []string{"a"}}},
		{name: "keyCooledSwapped", keys: []string{"a", "b"}, cool: true, want: outcome{Result: okResult, Keys: []string{"b"}}},
		{name: "allKeysCooledWaits", keys: []string{"a"}, cool: true, wantWait: true, want: outcome{Result: okResult, Keys: []string{"a"}}},
		{name: "keyReleasedWakesWaiter", keys: []string{"a"}, cool: true, wantWait: true, release: true, want: outcome{Result: okResult, Keys: []string{"a"}}},
		{
			name: "canceledWhileKeyCooling", keys: []string{"a"}, cool: true, wantWait: true, cancel: true,
			want: outcome{Result: provider.CallResult{Outcome: provider.OutcomeTerminal, Err: context.Canceled}, Cooling: true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGuardFixture(t, []step{{res: okResult}})
			pool := provider.NewKeyPoolWithClock(tt.keys, f.clock)
			lease := acquireLease(t, pool)
			var other *provider.Lease[string]
			if tt.release {
				other = acquireLease(t, pool)
			}
			if tt.cool {
				coolKey(t, pool, lease.Index, cooldown)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan provider.CallResult, 1)
			go func() { done <- f.guard.Call(ctx, lease, "", nil) }()
			if tt.wantWait {
				<-f.metrics.waited
				switch {
				case tt.cancel:
					cancel()
				case tt.release:
					other.Release()
				default:
					f.clock.advance(cooldown)
				}
			}
			res := <-done
			lease.Release()

			got := outcome{Result: res, Keys: f.fake.keys, Cooling: pool.MinCooldown() > 0}
			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Call() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func acquireLease(t *testing.T, pool *provider.KeyPool[string]) *provider.Lease[string] {
	t.Helper()
	l, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	return l
}

// coolKey puts the key at index in cooldown through a second lease on it.
func coolKey(t *testing.T, pool *provider.KeyPool[string], index int, d time.Duration) {
	t.Helper()
	for range pool.Len() + 1 {
		if l := acquireLease(t, pool); l.Index == index {
			l.MarkCooldown(provider.TierPolicy{Cooldown: d}, 0)
			return
		}
	}
	t.Fatalf("no lease on key %d", index)
}

func TestGuardCallMessages(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{provider.TextMessage(provider.RoleUser, "hi")}
	tests := []struct {
		name      string
		messages  bool
		nested    bool
		canceled  bool // rejection must not wait, so a canceled context still gets it
		want      provider.CallResult
		wantCalls int
		wantMsgs  []provider.Message
	}{
		{
			name:      "forwards to a MessageCaller",
			messages:  true,
			want:      okResult,
			wantCalls: 1,
			wantMsgs:  msgs,
		},
		{
			name:     "rejects a nested guard over other providers",
			nested:   true,
			canceled: true,
			want:     provider.CallResult{Err: provider.ErrMessagesUnsupported, Outcome: provider.OutcomeTerminal},
		},
		{
			name:     "rejects other providers",
			canceled: true,
			want:     provider.CallResult{Err: provider.ErrMessagesUnsupported, Outcome: provider.OutcomeTerminal},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeGuarded{clock: newFakeGuardClock(), steps: []step{{res: okResult}}}
			inner := provider.Provider[string](fake)
			mg := &messageGuarded{fakeGuarded: fake}
			if tt.messages {
				inner = mg
			}
			if tt.nested {
				innerGuard, err := provider.NewGuard(inner, provider.WithGuardClock(fake.clock))
				if err != nil {
					t.Fatalf("NewGuard() error = %v", err)
				}
				inner = innerGuard
			}
			g, err := provider.NewGuard(inner, provider.WithGuardClock(fake.clock))
			if err != nil {
				t.Fatalf("NewGuard() error = %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			if tt.canceled {
				cancel()
			}
			defer cancel()

			got := g.CallMessages(ctx, nil, msgs)

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("CallMessages() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantCalls, fake.callCount()); diff != "" {
				t.Errorf("calls mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantMsgs, mg.got); diff != "" {
				t.Errorf("forwarded messages mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGuardCallMessagesDropsUnusedLease(t *testing.T) {
	t.Parallel()
	fake := &fakeGuarded{clock: newFakeGuardClock()}
	g, err := provider.NewGuard[string](fake, provider.WithGuardClock(fake.clock))
	if err != nil {
		t.Fatalf("NewGuard() error = %v", err)
	}
	pool := provider.NewKeyPool([]string{"k"})
	lease := acquireLease(t, pool)
	coolKey(t, pool, 0, time.Minute)

	g.CallMessages(t.Context(), lease, []provider.Message{provider.TextMessage(provider.RoleUser, "hi")})
	lease.Release()

	if _, err := pool.Acquire(t.Context()); !errors.Is(err, provider.ErrAllKeysCoolingDown) {
		t.Errorf("Acquire() after Release error = %v, want %v", err, provider.ErrAllKeysCoolingDown)
	}
}
