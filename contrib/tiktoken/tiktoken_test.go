package tiktoken_test

import (
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/contrib/tiktoken"
)

// counter mirrors cost.Counter without importing the core module, which this
// module must not depend on.
type counter interface {
	Count(model, text string) (int, error)
}

var _ counter = (*tiktoken.Counter)(nil)

func TestCount(t *testing.T) {
	t.Parallel()
	c := tiktoken.New()
	tests := []struct {
		model   string
		text    string
		want    int
		wantErr error
	}{
		{model: "gpt-4o", text: "hello world", want: 2},
		{model: "gpt-4o-mini-2024-07-18", text: "", want: 0},
		{model: "gpt-3.5-turbo", text: "tiktoken is great!", want: 6},
		{model: "gpt-4o", text: "<|endoftext|>", want: 7},
		{model: "llama3.2", text: "hi", wantErr: tiktoken.ErrUnknownModel},
	}
	for _, tt := range tests {
		got, err := c.Count(tt.model, tt.text)
		if diff := cmp.Diff(tt.wantErr, err, cmpopts.EquateErrors()); diff != "" {
			t.Errorf("Count(%q, %q) error (-want +got):\n%s", tt.model, tt.text, diff)
		}
		if got != tt.want {
			t.Errorf("Count(%q, %q) = %d, want %d", tt.model, tt.text, got, tt.want)
		}
	}
}

func TestCountConcurrent(t *testing.T) {
	t.Parallel()
	const goroutines = 8
	c := tiktoken.New()
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			if got, err := c.Count("gpt-4o", "hello world"); err != nil || got != 2 {
				t.Errorf("Count() = %d, %v, want 2, nil", got, err)
			}
		})
	}
	wg.Wait()
}
