package scrub

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRegexpRule(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		pattern     string
		replacement string
		input       string
		want        string
		wantErr     bool
	}{
		{
			name:        "replaces email address",
			pattern:     `[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`,
			replacement: "[EMAIL]",
			input:       "Contact user@example.com for details",
			want:        "Contact [EMAIL] for details",
		},
		{
			name:        "no match passes through unchanged",
			pattern:     `\d{3}-\d{2}-\d{4}`,
			replacement: "[SSN]",
			input:       "no ssn here",
			want:        "no ssn here",
		},
		{
			name:    "invalid pattern returns error",
			pattern: `[invalid`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r, err := RegexpRule(tt.pattern, tt.replacement)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RegexpRule() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got := NewScrubber(r).Scrub(tt.input)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Scrub() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestMustRegexpRule_Panics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for invalid pattern, got none")
		}
	}()
	MustRegexpRule(`[invalid`, "[REDACTED]")
}

func TestScrubber_RulesAppliedInOrder(t *testing.T) {
	t.Parallel()
	// First rule turns "foo" into "bar", second turns "bar" into "baz".
	// Applied in order: "foo" -> "bar" -> "baz".
	s := NewScrubber(
		MustRegexpRule(`foo`, "bar"),
		MustRegexpRule(`bar`, "baz"),
	)
	got := s.Scrub("foo")
	if diff := cmp.Diff("baz", got); diff != "" {
		t.Errorf("Scrub() mismatch (-want +got):\n%s", diff)
	}
}

func TestScrubber_MultipleMatches(t *testing.T) {
	t.Parallel()
	s := NewScrubber(MustRegexpRule(`\d+`, "[NUM]"))
	got := s.Scrub("id=123, count=456")
	if diff := cmp.Diff("id=[NUM], count=[NUM]", got); diff != "" {
		t.Errorf("Scrub() mismatch (-want +got):\n%s", diff)
	}
}

func TestScrubber_ZeroValue(t *testing.T) {
	t.Parallel()
	var s Scrubber
	got := s.Scrub("unchanged")
	if diff := cmp.Diff("unchanged", got); diff != "" {
		t.Errorf("zero-value Scrubber must pass text through: (-want +got):\n%s", diff)
	}
}
