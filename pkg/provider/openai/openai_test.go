package openai_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/openai"
)

func TestProvider_HTTPClient_CustomPreserved(t *testing.T) {
	t.Parallel()
	c := &http.Client{}
	p := openai.NewProvider(nil, openai.WithHTTPClient(c), openai.WithHTTPClientTimeout(2*time.Second))
	if p == nil {
		t.Fatal("NewProvider returned nil")
	}
	if c.Timeout != 2*time.Second {
		t.Errorf("timeout not applied to supplied client: got %v", c.Timeout)
	}
}
