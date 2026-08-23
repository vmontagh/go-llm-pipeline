package httputil

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()

	const day = 24 * 60 * 60

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
		{name: "large delta-seconds", header: strconv.Itoa(day), want: time.Duration(day) * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ParseRetryAfter(tc.header); got != tc.want {
				t.Fatalf("ParseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
			}
		})
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	t.Parallel()

	// Fixed reference instant so HTTP-date fixtures are fully
	// deterministic. http.TimeFormat has second-level resolution and now
	// has no sub-second component, so the resulting durations are exact.
	now := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{
			name:   "one hour in the future",
			header: now.Add(time.Hour).Format(http.TimeFormat),
			want:   time.Hour,
		},
		{
			name:   "one hour in the past yields zero",
			header: now.Add(-time.Hour).Format(http.TimeFormat),
			want:   0,
		},
		{
			name:   "the reference instant yields zero",
			header: now.Format(http.TimeFormat),
			want:   0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseRetryAfter(tc.header, now); got != tc.want {
				t.Fatalf("parseRetryAfter(%q, %v) = %v, want %v", tc.header, now, got, tc.want)
			}
		})
	}
}
