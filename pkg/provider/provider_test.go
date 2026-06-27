package provider_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

var errTestSentinel = errors.New("test sentinel")

type fakeClock struct {
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func TestKeyPool_Len(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string
		wantLen int
	}{
		{name: "empty", keys: nil, wantLen: 0},
		{name: "single", keys: []string{"a"}, wantLen: 1},
		{name: "multiple", keys: []string{"a", "b", "c"}, wantLen: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := provider.NewKeyPoolWithClock(tt.keys, newFakeClock())
			if diff := cmp.Diff(tt.wantLen, p.Len()); diff != "" {
				t.Errorf("Len mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestKeyPool_Available(t *testing.T) {
	const cooldown = 10 * time.Second
	policy := provider.TierPolicy{Cooldown: cooldown}

	tests := []struct {
		name      string
		keys      []string
		setup     func(*fakeClock, *provider.KeyPool[string])
		wantAvail int
	}{
		{
			name:      "empty pool",
			keys:      nil,
			wantAvail: 0,
		},
		{
			name:      "all slots available",
			keys:      []string{"a", "b"},
			wantAvail: 2,
		},
		{
			name: "one slot cooling",
			keys: []string{"a", "b"},
			setup: func(_ *fakeClock, p *provider.KeyPool[string]) {
				l, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l.MarkCooldown(policy, 0)
			},
			wantAvail: 1,
		},
		{
			name: "all slots cooling",
			keys: []string{"a", "b"},
			setup: func(_ *fakeClock, p *provider.KeyPool[string]) {
				l1, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l1.MarkCooldown(policy, 0)
				l2, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l2.MarkCooldown(policy, 0)
			},
			wantAvail: 0,
		},
		{
			name: "cooling slot recovers after expiry",
			keys: []string{"a"},
			setup: func(clk *fakeClock, p *provider.KeyPool[string]) {
				l, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l.MarkCooldown(policy, 0)
				clk.advance(cooldown + time.Second)
			},
			wantAvail: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newFakeClock()
			p := provider.NewKeyPoolWithClock(tt.keys, clk)
			if tt.setup != nil {
				tt.setup(clk, p)
			}
			if diff := cmp.Diff(tt.wantAvail, p.Available()); diff != "" {
				t.Errorf("Available mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestKeyPool_Acquire(t *testing.T) {
	type leaseResult struct {
		Val string
		Idx int
	}

	const cooldown = 10 * time.Second
	policy := provider.TierPolicy{Cooldown: cooldown}

	tests := []struct {
		name    string
		keys    []string
		ctx     func() context.Context
		setup   func(*fakeClock, *provider.KeyPool[string])
		want    *leaseResult
		wantErr error
	}{
		{
			name:    "empty pool returns ErrMissingAPIKey",
			keys:    nil,
			ctx:     context.Background,
			wantErr: provider.ErrMissingAPIKey,
		},
		{
			name: "cancelled context propagated",
			keys: []string{"a"},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: context.Canceled,
		},
		{
			name: "single key success",
			keys: []string{"a"},
			ctx:  context.Background,
			want: &leaseResult{Val: "a", Idx: 0},
		},
		{
			name: "all keys cooling returns ErrAllKeysCoolingDown",
			keys: []string{"a", "b"},
			ctx:  context.Background,
			setup: func(_ *fakeClock, p *provider.KeyPool[string]) {
				l1, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l1.MarkCooldown(policy, 0)
				l2, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l2.MarkCooldown(policy, 0)
			},
			wantErr: provider.ErrAllKeysCoolingDown,
		},
		{
			name: "cooling key becomes available after expiry",
			keys: []string{"a"},
			ctx:  context.Background,
			setup: func(clk *fakeClock, p *provider.KeyPool[string]) {
				l, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l.MarkCooldown(policy, 0)
				clk.advance(cooldown + time.Second)
			},
			want: &leaseResult{Val: "a", Idx: 0},
		},
		{
			name: "LRU selects least recently used key",
			keys: []string{"a", "b"},
			ctx:  context.Background,
			setup: func(_ *fakeClock, p *provider.KeyPool[string]) {
				l, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l.Release()
			},
			want: &leaseResult{Val: "b", Idx: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newFakeClock()
			p := provider.NewKeyPoolWithClock(tt.keys, clk)
			if tt.setup != nil {
				tt.setup(clk, p)
			}

			lease, err := p.Acquire(tt.ctx())

			if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Acquire error mismatch (-want +got):\n%s", diff)
			}
			if tt.want == nil {
				return
			}
			if lease == nil {
				t.Fatal("Acquire returned nil lease, want non-nil")
			}
			got := leaseResult{Val: lease.Value, Idx: lease.Index}
			if diff := cmp.Diff(*tt.want, got); diff != "" {
				t.Errorf("Acquire lease mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLease_NilSafety(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{
			name: "Release on nil lease",
			fn:   func() { (*provider.Lease[string])(nil).Release() },
		},
		{
			name: "MarkCooldown on nil lease",
			fn: func() {
				(*provider.Lease[string])(nil).MarkCooldown(
					provider.TierPolicy{Cooldown: time.Second}, 0,
				)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn()
		})
	}
}

func TestLease_Release(t *testing.T) {
	tests := []struct {
		name    string
		keys    []string
		setup   func(*fakeClock, *provider.KeyPool[string])
		wantVal string
		wantIdx int
	}{
		{
			name: "released key stays available for next acquire",
			keys: []string{"a"},
			setup: func(_ *fakeClock, p *provider.KeyPool[string]) {
				l, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l.Release()
			},
			wantVal: "a",
			wantIdx: 0,
		},
		{
			name: "release updates LRU order",
			keys: []string{"a", "b"},
			setup: func(_ *fakeClock, p *provider.KeyPool[string]) {
				l, err := p.Acquire(context.Background())
				if err != nil {
					panic(err)
				}
				l.Release()
			},
			wantVal: "b",
			wantIdx: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newFakeClock()
			p := provider.NewKeyPoolWithClock(tt.keys, clk)
			if tt.setup != nil {
				tt.setup(clk, p)
			}
			lease, err := p.Acquire(context.Background())
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			got := struct {
				Val string
				Idx int
			}{lease.Value, lease.Index}
			want := struct {
				Val string
				Idx int
			}{tt.wantVal, tt.wantIdx}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("lease mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLease_MarkCooldown(t *testing.T) {
	tests := []struct {
		name         string
		policy       provider.TierPolicy
		retryAfter   time.Duration
		advanceMid   time.Duration
		advanceEnd   time.Duration
		wantAvailMid int
		wantAvailEnd int
	}{
		{
			name:         "zero cooldown releases slot immediately",
			policy:       provider.TierPolicy{Cooldown: 0},
			wantAvailMid: 1,
			wantAvailEnd: 1,
		},
		{
			name:         "slot parked during cooldown window",
			policy:       provider.TierPolicy{Cooldown: 10 * time.Second},
			advanceMid:   5 * time.Second,
			advanceEnd:   6 * time.Second,
			wantAvailMid: 0,
			wantAvailEnd: 1,
		},
		{
			name:         "HonorRetryAfter uses retryAfter when larger than cooldown",
			policy:       provider.TierPolicy{Cooldown: 5 * time.Second, HonorRetryAfter: true},
			retryAfter:   20 * time.Second,
			advanceMid:   15 * time.Second,
			advanceEnd:   6 * time.Second,
			wantAvailMid: 0,
			wantAvailEnd: 1,
		},
		{
			name:         "HonorRetryAfter uses cooldown when larger than retryAfter",
			policy:       provider.TierPolicy{Cooldown: 10 * time.Second, HonorRetryAfter: true},
			retryAfter:   3 * time.Second,
			advanceMid:   8 * time.Second,
			advanceEnd:   3 * time.Second,
			wantAvailMid: 0,
			wantAvailEnd: 1,
		},
		{
			name:         "retryAfter ignored when HonorRetryAfter is false",
			policy:       provider.TierPolicy{Cooldown: 10 * time.Second, HonorRetryAfter: false},
			retryAfter:   60 * time.Second,
			advanceMid:   5 * time.Second,
			advanceEnd:   6 * time.Second,
			wantAvailMid: 0,
			wantAvailEnd: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := newFakeClock()
			p := provider.NewKeyPoolWithClock([]string{"a"}, clk)

			l, err := p.Acquire(context.Background())
			if err != nil {
				t.Fatalf("Acquire: %v", err)
			}
			l.MarkCooldown(tt.policy, tt.retryAfter)

			if tt.advanceMid > 0 {
				clk.advance(tt.advanceMid)
			}
			if diff := cmp.Diff(tt.wantAvailMid, p.Available()); diff != "" {
				t.Errorf("Available (mid) mismatch (-want +got):\n%s", diff)
			}

			if tt.advanceEnd > 0 {
				clk.advance(tt.advanceEnd)
			}
			if diff := cmp.Diff(tt.wantAvailEnd, p.Available()); diff != "" {
				t.Errorf("Available (end) mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefaultEnrichError(t *testing.T) {
	sentinel := errTestSentinel

	tests := []struct {
		name    string
		tier    provider.ErrorTier
		rawErr  error
		body    []byte
		wantErr error
	}{
		{
			name:    "returns raw error unchanged",
			tier:    provider.TierTerminal,
			rawErr:  sentinel,
			wantErr: sentinel,
		},
		{
			name:    "returns nil for nil error",
			tier:    provider.TierShortTransient,
			rawErr:  nil,
			wantErr: nil,
		},
		{
			name:    "tier and body have no effect",
			tier:    provider.TierLongTransientBounded,
			rawErr:  sentinel,
			body:    []byte("body"),
			wantErr: sentinel,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := provider.DefaultEnrichError(tt.tier, tt.rawErr, tt.body)
			if diff := cmp.Diff(tt.wantErr, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("DefaultEnrichError mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestErrorTier_Ordering(t *testing.T) {
	tests := []struct {
		name string
		less provider.ErrorTier
		more provider.ErrorTier
	}{
		{
			name: "Terminal < ShortTransient",
			less: provider.TierTerminal,
			more: provider.TierShortTransient,
		},
		{
			name: "ShortTransient < ShortRateLimit",
			less: provider.TierShortTransient,
			more: provider.TierShortRateLimit,
		},
		{
			name: "ShortRateLimit < LongTransientBounded",
			less: provider.TierShortRateLimit,
			more: provider.TierLongTransientBounded,
		},
		{
			name: "LongTransientBounded < LongTransientInfinite",
			less: provider.TierLongTransientBounded,
			more: provider.TierLongTransientInfinite,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(true, tt.less < tt.more); diff != "" {
				t.Errorf("ErrorTier ordering violated (-want +got):\n%s", diff)
			}
		})
	}
}

func TestKeyPool_MinCooldown(t *testing.T) {
	steps := []struct {
		name     string
		cooldown time.Duration
		advance  time.Duration
		want     time.Duration
	}{
		{name: "initially ready", want: 0},
		{name: "one key cooling leaves another ready", cooldown: 10 * time.Second, want: 0},
		{name: "shortest cooldown wins", cooldown: 5 * time.Second, want: 5 * time.Second},
		{name: "advancing logical clock reduces wait", advance: 2 * time.Second, want: 3 * time.Second},
		{name: "ready key returns zero wait", advance: 4 * time.Second, want: 0},
	}

	clk := newFakeClock()
	pool := provider.NewKeyPoolWithClock([]string{"a", "b"}, clk)

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			if step.cooldown > 0 {
				lease, err := pool.Acquire(context.Background())
				if err != nil {
					t.Fatalf("Acquire: %v", err)
				}
				lease.MarkCooldown(provider.TierPolicy{Cooldown: step.cooldown}, 0)
			}
			if step.advance > 0 {
				clk.advance(step.advance)
			}
			if diff := cmp.Diff(step.want, pool.MinCooldown()); diff != "" {
				t.Errorf("MinCooldown mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
