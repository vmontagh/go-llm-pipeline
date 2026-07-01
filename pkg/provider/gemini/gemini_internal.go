package gemini

import provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"

// WithClockForTest injects a PoolClock into the assembled KeyPool.
func WithClockForTest(clk provpkg.PoolClock) ProviderOption { return withClock(clk) }
