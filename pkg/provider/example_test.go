package provider_test

import (
	"context"
	"fmt"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// echoProvider answers every prompt with itself; Guard only needs Call here.
type echoProvider struct {
	provider.Provider[string]
}

func (echoProvider) Call(_ context.Context, _ *provider.Lease[string], prompt string, _ []byte) provider.CallResult {
	return provider.CallResult{Text: prompt}
}

func ExampleNewGuard() {
	g, err := provider.NewGuard[string](echoProvider{},
		provider.WithConcurrencyLimits(2, 8),
		provider.WithBreaker(5, 30*time.Second),
	)
	if err != nil {
		fmt.Println(err)
		return
	}
	for range 4 {
		g.Call(context.Background(), nil, "hi", nil)
	}
	fmt.Println(g.Limit(), g.State())
	// Output: 3 closed
}
