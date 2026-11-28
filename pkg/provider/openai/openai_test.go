package openai_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/openai"
)

func TestProvider_CrossOriginRedirectRefused(t *testing.T) {
	t.Parallel()
	const timeout = 2 * time.Second
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Store(true) }))
	t.Cleanup(other.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	caller := &http.Client{}
	p := openai.NewProvider(nil, openai.WithBaseURL(redirector.URL), openai.WithHTTPClient(caller), openai.WithHTTPClientTimeout(timeout))

	got := p.Call(t.Context(), &provpkg.Lease[string]{Value: "k"}, "prompt", []byte("img"))

	if !errors.Is(got.Err, httputil.ErrCrossOriginRedirect) {
		t.Errorf("Call() error = %v, want %v", got.Err, httputil.ErrCrossOriginRedirect)
	}
	if leaked.Load() {
		t.Error("redirect target received the request")
	}
	if caller.Timeout != 0 || caller.CheckRedirect != nil {
		t.Errorf("caller client was mutated: Timeout = %v, CheckRedirect set = %t", caller.Timeout, caller.CheckRedirect != nil)
	}
}
