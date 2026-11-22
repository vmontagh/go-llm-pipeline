## go-llm-pipeline

[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/vmontagh/go-llm-pipeline/badge)](https://scorecard.dev/viewer/?uri=github.com/vmontagh/go-llm-pipeline)

An idiomatic, low-dependency Go toolkit for local-first, deterministic LLM orchestration and structured data extraction.

### Overview

`go-llm-pipeline` provides statically typed pipelines for high-throughput, batch LLM workloads.
It favors deterministic, VCR-style testing over live API calls, routes natively to local
inference engines (Ollama) as well as hosted providers (OpenAI, Gemini), and processes generic
assets through concurrent worker pools. It is a Go library with no server component and a light
dependency footprint: every provider speaks its REST API over `net/http`, so no vendor SDKs (or
their gRPC and cloud client trees) are pulled in.

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

### Doctor diagnostics

`pkg/doctor` checks local environment readiness before batch jobs. It is exposed
as a library API — not a repo-level binary — so consumer applications wire it
into their own command surface (for example a `doctor` subcommand or a
`--dry-run` preflight). A `Runner` executes a fixed set of checks and returns a
`Report` of pass/warn/fail/skip `Result`s; the doctor package never reads
environment variables itself, so callers stay in control of credentials.

Checks:

- OS file-descriptor soft limit against configurable floors.
- Network latency/reachability to the OpenAI and Gemini hosted APIs.
- OpenAI and Gemini API key validity.
- Local Ollama reachability and installed-model listing, with an optional
  context-window preflight when a specific model is requested.

```go
cfg := doctor.DefaultConfig()
cfg.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
cfg.GeminiAPIKey = cmp.Or(os.Getenv("GEMINI_API_KEY"), os.Getenv("GOOGLE_API_KEY"))
cfg.OllamaHost = cmp.Or(os.Getenv("OLLAMA_HOST"), cfg.OllamaHost)

report := doctor.NewRunner(&cfg).Run(ctx)
report.Log(ctx, logger)   // pass/skip at INFO, warn at WARN, fail at ERROR
fmt.Print(report.String()) // or render a plain-text summary for a CLI

if report.HasFailure() {
	os.Exit(1) // hard failures should block batch jobs; warnings do not
}
```

Inject a custom `*http.Client` with `doctor.WithHTTPClient` (used in tests to
avoid live network calls) and a `doctor.FileLimitReader` with
`doctor.WithFileLimitReader`. Warnings never mark the report as failed, so
borderline conditions surface without blocking automation.

### Package map

| Package | Description |
| --- | --- |
| `pkg/analyzer` | Template Method and Builder contracts for LLM analysis (`Event`/`Analyzer`, domain-agnostic `Asset`, sentinel errors). |
| `pkg/pipeline` | Concurrency utilities: generic worker-pool `Consume`, `PauseGate`, and a dead-letter queue. |
| `pkg/provider` | Concurrency-safe `KeyPool` of API credentials with per-key cooldown and rotation. |
| `pkg/provider/openai` | Transport half of the analyzer Template Method for OpenAI. |
| `pkg/provider/gemini` | Transport half of the analyzer Template Method for Gemini, calling the `generateContent` REST API over `net/http`. |
| `pkg/provider/ollama` | Transport half of the analyzer Template Method for local Ollama, plus a local `Embedder` (`/api/embed`) that returns `[]float64` for `pkg/vectorstore`. |
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
| `pkg/vectorstore` | Optional, dependency-free local vector store: in-memory nearest-neighbor search, deduplication, and threshold clustering over caller-supplied embeddings. |
| `pkg/promptregistry` | Versioned, offline-rendered prompt templates loaded from an `fs.FS`, with versioned rule overlays (`WithOverlay`, `VersionID`), byte budgets and golden-file tests. |
| `pkg/cost` | Token usage and exact cost estimates: parses provider-reported usage, prices it with a caller-supplied `PriceTable`, aggregates per asset and model in a `Tracker` (optionally exporting metrics), and compares reports against a baseline. |
| `pkg/cost/costtest` | Test helper that prices the calls recorded in cassettes and fails the test on unpriced models or a cost regression over a saved baseline, offline (`costtest.Check`). A pluggable `Counter` estimates tokens when the provider reports none. |
| `contrib/tiktoken` | Optional separate module: an exact OpenAI `cost.Counter` built on tiktoken-go with embedded encodings. The core module does not depend on it. |
| `pkg/doctor` | Environment-readiness diagnostics (file descriptors, hosted-API latency and key validity, local Ollama) exposed as a library API. |

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

#### Catching cost regressions in CI

`pkg/cost/costtest` turns recorded cassettes into a cost check that runs in an ordinary `go test`,
offline and for free. It never calls a provider. Each recorded response already contains the
provider's token counts (for example `"usage":{"prompt_tokens":1000,"completion_tokens":500}`), so
the check multiplies them by a price table you commit and compares the total with a committed
baseline:

1. Record cassettes once with `pkg/llmvcr` and commit them.
2. Commit a price table in USD per million tokens, such as
   `models: {gpt-4o-mini: {input: 0.15, output: 0.60}}`.
3. Add a test that calls `costtest.Check`. The package documentation has a complete test with an
   `-update-cost` flag that writes the baseline.
4. Run that test once with `-update-cost` and commit `testdata/cost-baseline.json`.

```go
report := costtest.Check(t, os.DirFS("testdata/cassettes"), prices, costtest.Options{
    Baseline:           &baseline, // the committed cost-baseline.json
    MaxIncreasePercent: 10,
})
```

The cost changes only when someone re-records cassettes, for example after editing a prompt. The
test then fails if the new recordings cost more than the allowed increase, or if a call uses a model
missing from the price table. Run it with `-v` to see the per-model table. When an increase is
intended, rerun with `-update-cost` and commit the new baseline in the same pull request, so
reviewers see the cost difference in the diff.

#### Differential fuzzing of the schema layer

`pkg/analyzer` derives a JSON schema from a Go type (`SchemaOf`), then later validates a
model's response against it (`Validate`). Those two paths and `encoding/json` are three
independent sources of truth that must agree, and past regressions were subtle drifts
between them. `differential_test.go` pins two invariants with Go's native fuzzing framework:

- **Property A** — `Validate(SchemaOf(T), json.Marshal(v))` never rejects a value the model
  could legitimately produce.
- **Property B** — anything `Validate` accepts also decodes into `T` (the validator is never
  more permissive than the decoder).

Both targets seed a generated corpus covering a catalog of tricky types, so `make test`
exercises the properties deterministically with no extra step. For deeper, coverage-guided
exploration, run a target directly:

```sh
go test ./pkg/analyzer -run '^$' -fuzz 'FuzzValidateImpliesDecode$' -fuzztime 2m
```

Any failing input the engine finds is written under `pkg/analyzer/testdata/fuzz/` and
replays as a permanent regression seed on the next `go test`.

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
