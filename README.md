## go-llm-pipeline

An idiomatic, dependency-free Go toolkit for local-first, deterministic LLM orchestration and structured data extraction.

### Overview

`go-llm-pipeline` provides statically typed pipelines for high-throughput, batch LLM workloads.
It favors deterministic, VCR-style testing over live API calls, routes natively to local
inference engines (Ollama) as well as hosted providers (OpenAI, Gemini), and processes generic
assets through concurrent worker pools. It is a Go library with no server component and a light
dependency footprint.

### Installation

```sh
go get github.com/vmontagh/go-llm-pipeline
```

The module targets Go 1.26 (see [`go.mod`](go.mod)).

### Quick start

`pkg/pipeline` provides a generic worker-pool consumer. `Consume` starts a pool of workers that
read from a channel, honor context cancellation, and run your `process` function for each item:

```go
package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/vmontagh/go-llm-pipeline/pkg/pipeline"
)

func main() {
	ctx := context.Background()

	input := make(chan int)
	go func() {
		defer close(input)
		for i := 0; i < 5; i++ {
			input <- i
		}
	}()

	var wg sync.WaitGroup
	pipeline.Consume(ctx, input, 3, &wg, func(ctx context.Context, n int) {
		fmt.Println("processed", n)
	})
	wg.Wait()
}
```

`Consume` also accepts options such as `WithPauseGate` to pause and resume processing at runtime.

### Package map

| Package | Description |
| --- | --- |
| `pkg/analyzer` | Template Method and Builder contracts for LLM analysis (`Event`/`Analyzer`, sentinel errors). |
| `pkg/pipeline` | Concurrency utilities: generic worker-pool `Consume`, `PauseGate`, and a dead-letter queue. |
| `pkg/provider` | Concurrency-safe `KeyPool` of API credentials with per-key cooldown and rotation. |
| `pkg/provider/openai` | Transport half of the analyzer Template Method for OpenAI. |
| `pkg/provider/gemini` | Transport half of the analyzer Template Method for Gemini. |
| `pkg/provider/ollama` | Transport half of the analyzer Template Method for local Ollama. |
| `pkg/strategy` | Ensemble generation and judge-synthesis strategies (`Generator`/`Judge`). |
| `pkg/policy` | Provider-agnostic retry/error taxonomy (tiers, `RetryPolicy`, error classification). |
| `pkg/scrub` | Regexp-based text scrubbing to redact sensitive patterns and PII. |
| `pkg/vcrpatch` | Patch and rewrite VCR cassettes (match requests, apply actions). |
| `pkg/llmvcr` | Standalone VCR record/replay for provider `http.Client`s; auto-redacts secrets (via `pkg/scrub`) before writing cassettes; fail-fast on miss. |
| `pkg/llmtest` | Mock provider response/interaction fixtures for deterministic tests. |
| `pkg/observ` | Observability HTTP server (pprof) bound to a loopback address. |
| `pkg/trace` | `runtime/trace`-based `Tracer` plus a no-op implementation. |
| `pkg/httputil` | Small protocol-level HTTP helpers, independent of business logic. |
| `pkg/logger` | Helpers for creating `*slog.Logger` instances. |

### Determinism and testing

Deterministic, zero-cost CI is a first-class goal. The determinism toolkit is centered on
`pkg/llmvcr`, with supporting packages for cassette patching, redaction rules, and fixtures:

- `pkg/llmvcr` is the productized record/replay package. It returns a standard `*http.Client` that
  records provider HTTP interactions to YAML cassettes and replays them offline, failing fast with
  `llmvcr.ErrCassetteMiss` on a cassette miss instead of making a live call. It works with any client
  that accepts an `*http.Client`, including the OpenAI provider (`openai.WithHTTPClient`) and the
  Gemini `genai.Client` (`option.WithHTTPClient`). Sensitive headers, query parameters, and body
  tokens (bearer tokens, `sk-` and `AIza` keys) are redacted via `pkg/scrub` before any cassette is
  written.
- `pkg/vcrpatch` patches and rewrites recorded cassettes for scenario testing.
- `pkg/scrub` provides the regexp redaction rules reused by `pkg/llmvcr` (and usable for logs).
- `pkg/llmtest` provides mock provider response and interaction fixtures for tests.

```go
rec, err := llmvcr.New("testdata/openai_success", llmvcr.ModeReplayOnly)
if err != nil {
    // handle error
}
defer func() {
    if err := rec.Stop(); err != nil {
        log.Printf("stopping recorder: %v", err)
    }
}()
provider := openai.NewProvider(apiKeys, openai.WithHTTPClient(rec.HTTPClient()))
```

### Development

Common `make` targets:

| Target | Description |
| --- | --- |
| `make build` | Compile all packages. |
| `make test` | Run the full test suite with the race detector. |
| `make test-unit-cover` | Run tests with a coverage profile. |
| `make lint` | Run `golangci-lint`. |
| `make lint-arch` | Run the architecture linter (`go-arch-lint`). |
| `make fmt` | Format the code with `gofmt`. |
| `make vet` | Run `go vet`. |
| `make tidy` | Run `go mod tidy`. |
| `make clean` | Remove generated artifacts. |

### Roadmap

The product roadmap and positioning notes have moved to [`docs/ROADMAP.md`](docs/ROADMAP.md).

### License

Released under the [MIT License](LICENSE).
