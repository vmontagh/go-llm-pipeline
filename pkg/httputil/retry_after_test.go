package httputil

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "empty header yields zero", header: "", want: 0},
		{name: "positive integer seconds", header: "30", want: 30 * time.Second},
		{name: "one second", header: "1", want: time.Second},
		{name: "zero seconds yields zero", header: "0", want: 0},
		{name: "negative seconds yields zero", header: "-5", want: 0},
		{name: "garbage yields zero", header: "soon", want: 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseRetryAfter(tc.header); got != tc.want {
				t.Fatalf("ParseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestParseRetryAfter_HTTPDateFuture(t *testing.T) {
	t.Parallel()

	// An HTTP-date ~1 hour in the future should yield a positive
	// duration close to 1 hour. We allow a generous slack window to
	// account for clock drift and HTTP-date second-level resolution.
	future := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	header := future.Format(http.TimeFormat)

	got := ParseRetryAfter(header)
	if got <= 0 {
		t.Fatalf("ParseRetryAfter(%q) = %v, want > 0", header, got)
	}
	if got > time.Hour+5*time.Second {
		t.Fatalf("ParseRetryAfter(%q) = %v, want <= ~1h", header, got)
	}
	if got < time.Hour-5*time.Second {
		t.Fatalf("ParseRetryAfter(%q) = %v, want >= ~1h", header, got)
	}
}

func TestParseRetryAfter_HTTPDatePastYieldsZero(t *testing.T) {
	t.Parallel()

	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	header := past.Format(http.TimeFormat)

	if got := ParseRetryAfter(header); got != 0 {
		t.Fatalf("ParseRetryAfter(%q) = %v, want 0", header, got)
	}
}

func TestParseRetryAfter_LargeSeconds(t *testing.T) {
	t.Parallel()

	// 24 hours expressed as delta-seconds.
	const day = 24 * 60 * 60
	header := strconv.Itoa(day)
	want := time.Duration(day) * time.Second
	if got := ParseRetryAfter(header); got != want {
		t.Fatalf("ParseRetryAfter(%q) = %v, want %v", header, got, want)
	}
}
