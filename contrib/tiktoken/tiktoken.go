// Package tiktoken counts OpenAI model tokens exactly with tiktoken-go and
// its embedded, offline encoding files. It satisfies the Counter interface in
// github.com/vmontagh/go-llm-pipeline/pkg/cost structurally and is a separate
// module so the core library stays free of the tokenizer and its several
// megabytes of encoding data; import it only if you need exact counts.
package tiktoken

import (
	"errors"
	"fmt"
	"sync"

	tk "github.com/pkoukk/tiktoken-go"
	loader "github.com/pkoukk/tiktoken-go-loader"
)

// ErrUnknownModel is returned for models tiktoken has no encoding for, such
// as non-OpenAI models; fall back to another counter for those.
var ErrUnknownModel = errors.New("no tiktoken encoding for model")

// offlineOnce installs the embedded encodings so counting never downloads.
// tiktoken-go keeps its loader in a package variable, so it is set once for
// the process rather than per Counter.
var offlineOnce sync.Once

// Counter counts tokens for OpenAI models. The zero value is not usable; call
// New. It is safe for concurrent use.
type Counter struct {
	mu       sync.Mutex
	encoders map[string]*tk.Tiktoken
}

// New returns a Counter that uses only the embedded encoding files.
func New() *Counter {
	offlineOnce.Do(func() { tk.SetBpeLoader(loader.NewOfflineLoader()) })
	return &Counter{encoders: make(map[string]*tk.Tiktoken)}
}

// Count returns the number of tokens text encodes to on model. Special-token
// markers in text are counted as ordinary text, as they are when sent as
// message content.
func (c *Counter) Count(model, text string) (int, error) {
	enc, err := c.encoder(model)
	if err != nil {
		return 0, err
	}
	return len(enc.EncodeOrdinary(text)), nil
}

func (c *Counter) encoder(model string) (*tk.Tiktoken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if enc, ok := c.encoders[model]; ok {
		return enc, nil
	}
	enc, err := tk.EncodingForModel(model)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrUnknownModel, model, err)
	}
	c.encoders[model] = enc
	return enc, nil
}
