package analyzer

import "context"

// Preflighter is the cross-provider hook for backends that can verify
// — before the first real request — that the configured model is
// structurally able to serve the analyzer's prompts (context window,
// modality, quota, etc.).
type Preflighter interface {
	Preflight(ctx context.Context) error
}

// RunPreflight invokes pf.Preflight. If pf is nil, it is treated as a no-op and returns nil.
func RunPreflight(ctx context.Context, pf Preflighter) error {
	if pf == nil {
		return nil
	}
	return pf.Preflight(ctx)
}
