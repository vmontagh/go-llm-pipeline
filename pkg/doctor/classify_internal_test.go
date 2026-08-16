package doctor

import (
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

var errProbe = errors.New("probe network error")

func TestClassifyFileLimit(t *testing.T) {
	t.Parallel()
	const (
		minLimit  = 1024
		warnLimit = 4096
	)
	tests := []struct {
		name string
		soft uint64
		want Status
	}{
		{name: "below_min_fails", soft: 512, want: StatusFail},
		{name: "at_min_warns", soft: 1024, want: StatusWarn},
		{name: "below_warn_warns", soft: 2048, want: StatusWarn},
		{name: "at_warn_passes", soft: 4096, want: StatusPass},
		{name: "above_warn_passes", soft: 65536, want: StatusPass},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := classifyFileLimit(tt.soft, minLimit, warnLimit)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("classifyFileLimit(%d, %d, %d) status mismatch (-want +got):\n%s", tt.soft, minLimit, warnLimit, diff)
			}
		})
	}
}

func TestClassifyLatency(t *testing.T) {
	t.Parallel()
	warn := 2 * time.Second
	tests := []struct {
		name          string
		probe         httpProbe
		keyConfigured bool
		want          Status
	}{
		{name: "reachable_fast_passes", probe: httpProbe{status: 401, elapsed: 100 * time.Millisecond}, want: StatusPass},
		{name: "reachable_slow_warns", probe: httpProbe{status: 200, elapsed: 3 * time.Second}, want: StatusWarn},
		{name: "unreachable_without_key_warns", probe: httpProbe{elapsed: time.Second, err: errProbe}, want: StatusWarn},
		{name: "unreachable_with_key_fails", probe: httpProbe{elapsed: time.Second, err: errProbe}, keyConfigured: true, want: StatusFail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := classifyLatency(tt.probe, tt.keyConfigured, warn)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("classifyLatency(%+v, %v) status mismatch (-want +got):\n%s", tt.probe, tt.keyConfigured, diff)
			}
		})
	}
}

func TestClassifyKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		probe httpProbe
		want  Status
	}{
		{name: "ok_passes", probe: httpProbe{status: 200}, want: StatusPass},
		{name: "unauthorized_fails", probe: httpProbe{status: 401}, want: StatusFail},
		{name: "forbidden_fails", probe: httpProbe{status: 403}, want: StatusFail},
		{name: "rate_limited_warns", probe: httpProbe{status: 429}, want: StatusWarn},
		{name: "server_error_warns", probe: httpProbe{status: 503}, want: StatusWarn},
		{name: "unexpected_status_fails", probe: httpProbe{status: 418}, want: StatusFail},
		{name: "network_error_warns", probe: httpProbe{err: errProbe}, want: StatusWarn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := classifyKey(tt.probe)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("classifyKey(%+v) status mismatch (-want +got):\n%s", tt.probe, diff)
			}
		})
	}
}
