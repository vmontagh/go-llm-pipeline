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

#### Resuming an interrupted batch

A long batch that crashes, is stopped, or hits a rate limit does not have to start over. Open a
`FileCheckpoint` and wrap your process function with `Checkpointed`. Each item that succeeds is
recorded in an append-only journal file. When you run the batch again with the same journal, items
already in it are skipped, so only unfinished or failed items are processed (and paid for) again.
Failures go to your error handler, for example a `DeadLetterQueue` entry with the attempt count:

```go
cp, err := pipeline.OpenFileCheckpoint("state/done.jsonl")
if err != nil {
	return err
}
process := pipeline.Checkpointed(cp, func(a Asset) string { return a.ID }, analyze,
	func(a Asset, err error) {
		entry := pipeline.DLQEntry{ItemID: a.ID, Stage: "analyze", Error: err.Error()}
		if err := dlq.Add(&entry); err != nil {
			logger.Error("recording failed asset", "asset", a.ID, "err", err)
		}
	})
err = pipeline.ConsumeAll(ctx, assets, 8, process) // 8 workers; returns ctx.Err() if stopped early
return errors.Join(err, cp.Close())                // Close flushes the journal
```

Records reach the operating system as soon as an item completes, so a process crash loses nothing;
`WithSyncEvery(n)` trades how often the journal is flushed to disk against machine-crash safety.
If a write or flush fails, the item stays unmarked and is retried by the next run.
Only one run can open a journal at a time; a second open returns `ErrCheckpointInUse`.
Use `ReadDLQ` to list failed items for inspection or a replay run. Implement the two-method
`Checkpoint` interface to keep progress somewhere else, such as a database.

#### Adapting to rate limits

Wrap a provider in a `provider.Guard` to adapt how many calls run at once and to stop retry storms.
The in-flight limit starts at the minimum and grows by one slot after about `limit` successes. It
halves on a rate limit (HTTP 429, a `Retry-After`, or a `TierShortRateLimit` error) and shrinks a
little when a call is slower than an optional latency target. Calls over the limit wait. After
several transient errors in a row, the circuit breaker opens for a while (or for the `Retry-After`,
if longer); calls then wait until one probe call succeeds. A call whose key went into cooldown
while it waited switches to a ready key, or waits for one. A call whose context ends while waiting
returns a terminal error, and a canceled call does not count as a provider failure.

```go
a, err := analyzer.NewBuilder(p, parse).
	WithGuard(
		provider.WithConcurrencyLimits(1, 16),      // the defaults
		provider.WithBreaker(5, 30*time.Second),    // the defaults
		provider.WithLatencyTarget(20*time.Second), // off by default
		provider.WithGuardMetrics(metrics),         // guard_waits, guard_limit_decreases, guard_breaker_opens
	).
	Build()
```

Share the built analyzer across workers so every call goes through the same guard. To read
`Limit()` and `State()`, call `provider.NewGuard` yourself and pass the guard to `NewBuilder`.

#### Multi-turn conversations

`AnalyzeMessages` sends a typed conversation instead of a single prompt. Each `provider.Message`
has a role (system, user, assistant, or tool; see [Tool calling](#tool-calling)) and parts holding text or, in user messages, JPEG image bytes. The OpenAI,
Gemini and Ollama providers map the roles natively, and a `Guard` passes messages through:

```go
res, err := a.AnalyzeMessages(ctx, []provider.Message{
	provider.TextMessage(provider.RoleSystem, "Answer in JSON."),
	provider.TextMessage(provider.RoleUser, "Describe this photo."),
	provider.TextMessage(provider.RoleAssistant, `{"heading":"A beach"}`),
	{Role: provider.RoleUser, Parts: []provider.Part{{Text: "Now this one."}, {Image: jpeg}}},
}, "job-1", nil)
```

Invalid messages return `provider.ErrInvalidMessages`. A custom provider takes messages by
implementing `provider.MessageCaller`; otherwise `AnalyzeMessages` returns
`provider.ErrMessagesUnsupported`. Keeping the history within the model's context window is up to
the caller.

#### Streaming

`StreamMessages` yields the reply in chunks as they arrive, without the parser. The last chunk
carries the token usage when the provider reports it, so callers can budget the conversation:

```go
for chunk, err := range a.StreamMessages(ctx, msgs, "job-1", nil) {
	if err != nil {
		return err
	}
	fmt.Print(chunk.Text)
	if chunk.Usage != nil {
		log.Printf("tokens in=%d out=%d", chunk.Usage.Input, chunk.Usage.Output)
	}
}
```

Opening the stream is retried like `Analyze`. An error after that ends the stream, since the
chunks are already delivered; a stream cut off early yields `provider.ErrIncompleteStream`.
Stopping the loop early closes the connection. Ollama, OpenAI, and Gemini stream; a custom
provider streams by implementing `provider.Streamer`, otherwise `StreamMessages` yields
`provider.ErrStreamUnsupported`. Through a `Guard`, a stream keeps its concurrency slot until it
ends.

#### Tool calling

`AnalyzeWithTools` lets the model call typed Go functions before it answers. `NewTool` derives
the parameter schema from the argument struct, checks the model's arguments against it, and sends
the result back as JSON:

```go
type weatherArgs struct {
	City string `json:"city" jsonschema:"description=City name"`
}
type weather struct {
	TempC int `json:"temp_c"`
}

lookup, err := analyzer.NewTool("weather", "Current weather in a city",
	func(ctx context.Context, a weatherArgs) (weather, error) { return forecast(ctx, a.City) })
if err != nil {
	return err
}
res, err := a.AnalyzeWithTools(ctx, msgs, analyzer.ToolLoop{Tools: []analyzer.Tool{lookup}}, "job-1", nil)
```

While the model asks for tools, the loop runs them in order and sends the results back; its first
reply without tool calls is parsed and returned. Each model call is retried like `Analyze`. After
`MaxSteps` replies (default 8) that still call tools, it returns `analyzer.ErrMaxToolSteps`.

An unknown tool name returns `analyzer.ErrUnknownTool`, and arguments that fail the schema return
`analyzer.ErrInvalidToolArguments`; a tool may return that error too, to reject arguments the
schema cannot express. With `ReportInvalidCalls` set, both are sent back to the model as the tool
result instead, so it can correct itself. Any other tool error ends the loop.

The OpenAI provider supports tools; a custom provider does so by implementing
`provider.ToolCaller`, otherwise `AnalyzeWithTools` returns `provider.ErrToolsUnsupported`. The
same error is returned for messages with tool parts sent to `AnalyzeMessages` or `StreamMessages`.

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
| `pkg/pipeline` | Concurrency utilities: generic worker-pool `Consume`, `PauseGate`, resumable runs (`Checkpoint`, `FileCheckpoint`, `Checkpointed`), and a dead-letter queue. |
| `pkg/provider` | Concurrency-safe `KeyPool` of API credentials with per-key cooldown and rotation, and `Guard` for adaptive concurrency and a circuit breaker. |
| `pkg/provider/openai` | Transport half of the analyzer Template Method for OpenAI. |
| `pkg/provider/gemini` | Transport half of the analyzer Template Method for Gemini, calling the `generateContent` REST API over `net/http`. |
| `pkg/provider/ollama` | Transport half of the analyzer Template Method for local Ollama, plus a local `Embedder` (`/api/embed`) that returns `[]float64` for `pkg/vectorstore`. |
| `pkg/strategy` | Ensemble generation and judge-synthesis strategies (`Generator`/`Judge`). |
| `pkg/policy` | Provider-agnostic retry/error taxonomy (tiers, `RetryPolicy`, error classification). |
| `pkg/scrub` | Regexp-based text scrubbing to redact sensitive patterns and PII. |
| `pkg/vcrpatch` | Patch and rewrite VCR cassettes (match requests, apply actions). |
| `pkg/llmvcr` | Standalone VCR record/replay for provider `http.Client`s; auto-redacts secrets (via `pkg/scrub`) before writing cassettes; fail-fast on miss. |
| `pkg/llmtest` | Mock provider fixtures and golden-file snapshots for deterministic tests. |
| `pkg/eval` | Offline evaluation: YAML datasets, field-match and LLM-judge scoring, CI pass-rate gates. |
| `pkg/observ` | Observability HTTP server (pprof) bound to a loopback address. |
| `pkg/trace` | `runtime/trace`-based `Tracer` plus a no-op implementation. |
| `pkg/httputil` | Small protocol-level HTTP helpers, independent of business logic. |
| `pkg/logger` | Helpers for creating `*slog.Logger` instances. |
| `pkg/vectorstore` | Optional, dependency-free local vector store: in-memory nearest-neighbor search, deduplication, and threshold clustering over caller-supplied embeddings. |
| `pkg/promptregistry` | Versioned, offline-rendered prompt templates loaded from an `fs.FS`, with versioned rule overlays (`WithOverlay`, `VersionID`), byte budgets and golden-file tests. |
| `pkg/cost` | Token usage and exact cost estimates: parses provider-reported usage, prices it with a caller-supplied `PriceTable`, aggregates per asset and model in a `Tracker` (optionally exporting metrics), and compares reports against a baseline. |
| `pkg/cost/costtest` | Test helper that prices the calls recorded in cassettes and fails the test on unpriced models or a cost regression over a saved baseline, offline (`costtest.Check`). Responses without provider-reported usage are skipped and not counted. |
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
- `pkg/llmtest` provides mock provider fixtures and the `Golden` snapshot helper for tests.

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

#### Locking results with golden files

`llmtest.Golden` catches unexpected changes to an extraction result. It writes the result as
indented JSON (map keys sorted) and compares it with `testdata/<name>.golden`, printing a
`cmp.Diff` on mismatch. Replay a cassette so the result is the same on every run:

```go
func TestExtract(t *testing.T) {
    // Build the provider on a replay-only llmvcr client, as above.
    got, err := extractTags(t.Context(), provider) // your code under test
    if err != nil {
        t.Fatal(err)
    }
    llmtest.Golden(t, "extract_cat", got)
}
```

Run `LLMTEST_UPDATE=1 go test ./...` to create or refresh the golden files, then review and
commit them.

#### Evaluating quality in CI

`pkg/eval` scores pipeline output against a labeled dataset, so a prompt or model change that makes
results worse fails the build. With a cassette replay client the run is offline and free.

1. Write `testdata/dataset.yaml`. Each entry is an input and the result you expect, in the same
   shape as your result type:

   ```yaml
   - name: cat
     input: cat.jpg
     want: {heading: Cute Cat, tags: [cat, pet]}
   ```

2. Pick evaluators. They score your pipeline's actual result against `want`:
   - `eval.FieldMatch` checks each top-level field exactly. If the pipeline returns
     `{heading: Cute Cat, tags: [cat]}`, `heading` matches and `tags` does not, so the example
     scores 0.5 and fails.
   - `eval.NewJudge` asks a judge model for a score from 0 to 1, which suits free text such as a
     differently worded heading. Any analyzer whose result type is `*eval.JudgeScore` can be the
     judge.
3. Run them and fail the test below a pass rate:

```go
func TestQuality(t *testing.T) {
    examples, err := eval.LoadDataset[string, Photo](os.DirFS("testdata"), "dataset.yaml")
    if err != nil {
        t.Fatal(err)
    }
    report, err := eval.Run(t.Context(), examples, describePhoto, eval.FieldMatch[Photo]{})
    if err != nil {
        t.Fatal(err)
    }
    if err := report.Check(0.9); err != nil {
        t.Error(err)
    }
    llmtest.Golden(t, "quality_report", report) // optional: track per-example changes
}
```

The report lists examples sorted by name, with the pass rate, each evaluator's mean score and pass
rate, and per-field accuracy.

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
4. Run that test once with `-update-cost`, naming only its package (for example
   `go test ./myapp -run TestPromptCost -update-cost`, not `./...`, because other test binaries
   reject the unknown flag), and commit `testdata/cost-baseline.json`.

```go
report := costtest.Check(t, os.DirFS("testdata/cassettes"), prices, costtest.Options{
    Baseline:           &baseline, // the committed cost-baseline.json
    MaxIncreasePercent: 10,
})
```

Responses that carry no provider-reported usage are skipped, so they add nothing to the total; check
the `CALLS` column in the `-v` output if a model looks under-counted. The cost changes only when someone re-records cassettes, for example after editing a prompt. The
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
