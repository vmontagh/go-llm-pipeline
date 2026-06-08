# go-llm-pipeline

An idiomatic, dependency-free Go toolkit for local-first, deterministic LLM orchestration and structured data extraction.

## Product Roadmap

`go-llm-pipeline` is the primary product: a reusable, domain-agnostic toolkit for deterministic LLM workflows.
`vmontagh/flickr-organizer` is a reference application and one of several target use cases built on top of this toolkit.

### Proposal: The go-llm-pipeline Project

- **Objective:** Build an idiomatic, dependency-free Go toolkit for local-first, deterministic LLM orchestration and structured data extraction.
- **Stance:** Most AI frameworks are centered on Python, dynamic chat interfaces, and live API calls. This project provides a contrasting approach with statically typed pipelines, deterministic VCR-style testing, and batch-processing generic assets using concurrent worker pools.

### Core Value Propositions

- **Zero-Cost, Deterministic CI/CD:** An LLM-aware cassette testing package that records HTTP interactions and replays them locally, with sensitive token scrubbing by default.
- **Type-Safe Extraction:** Force LLM outputs directly into strict Go structs.
- **Local-First:** Route natively to local inference engines (Ollama) with configurable concurrency caps.
- **Use-Case Agnostic:** Support pipelines across media, document, and operational data workflows; not tied to a single vertical application.

### Positioning vs. Workflow Orchestrators like Eino

Eino is an excellent fit for autonomous conversational systems (complex state graphs, ReAct loops, multi-agent delegation, and streaming chat UX). `go-llm-pipeline` is intentionally optimized for deterministic, production batch workflows.

- **Deterministic testing as a first-class feature:** `pkg/llmvcr` records and replays provider HTTP interactions so CI remains fast, stable, and low-cost.
- **Batch extraction over chat streaming:** The core workload is high-throughput, typed enrichment of large asset sets into strict Go structs.
- **Developer-orchestrated execution over autonomous loops:** Pipeline flow is explicit and predictable (for example, ensemble generation and judge synthesis), reducing drift and runtime surprises.
- **Resource-aware local concurrency:** Local compute-bound workers and remote API-bound workers are tuned independently for practical VRAM/CPU/network constraints.
- **Reliability toolkit, not orchestration sprawl:** Parse/retry loops, rate-limit handling, provider portability, and deterministic behavior are prioritized over broad agent abstractions.

**Strategic framing:** If you are building an open-ended AI assistant, Eino may be a better fit. If you are building ETL-style enrichment, legacy data migration, bulk tagging/classification, or strict structured extraction with CI-grade reproducibility, `go-llm-pipeline` is the better fit.

### Phase 1: The Testing Core and Abstraction (Next 1-2 Months)

- **Productize the VCR Package (`pkg/llmvcr`):**
  - Extract the current cassette/proxy workflow into a standalone package.
  - Ensure it natively intercepts requests from standard Go HTTP clients used by OpenAI and Gemini SDKs.
  - Implement automatic redaction for common sensitive headers (for example, `Authorization` and API keys) before writing YAML/JSON cassettes.
- **Define the Generic Asset Interface:**
  - Define and standardize the domain-agnostic `Asset` contract in the analyzer.
  
```go
type Asset struct {
    ID       string         // Stable unique identifier (for example, URI, filename, or external ID).
    Payload  io.Reader      // Raw asset bytes consumed by the pipeline.
    Metadata map[string]any // Provider-specific attributes that should not be part of raw payload bytes.
}
```

This interface should support image raws (for example, `.DNG` and `.ARW`), plain-text documents (for example, `.txt` or `.md`), and structured payloads (for example, `.json`) using the same pipeline contract.

- **Reference Implementation Migration:** Refactor `flickr-organizer` to consume these extracted packages as a proof point, without coupling core APIs to Flickr-specific concepts.

### Phase 2: Structured Data & Schema Enforcement (Months 3-4)

- **Struct Mapping:** Implement utilities using JSON Schema validation or provider-specific structured output features to unmarshal LLM responses directly into Go structs.
- **Domain Applications:** Document structured data extraction use cases, such as parsing unstructured clinical notes into FHIR resources or normalizing inconsistent logs into a standard schema.
- **Fallback & Retry Logic:** Add automatic retries that feed validation errors back to the LLM (for example, field-level schema failures and JSON formatting corrections).
- **Product Packaging:** Publish clear package boundaries (`pkg/llmvcr`, `pkg/analyzer`, structured extraction helpers) to support adoption by independent applications.

### Phase 3: Operator Ergonomics & Local Execution (Months 5-6)

- **Diagnostic Subcommand (`<app> doctor`):**
  - Check local environment readiness before batch jobs.
  - Verify OS file descriptors.
  - Test network latency to hosted APIs.
  - Validate API keys.
  - Probe local Ollama instances and list loaded models.
- **Local Embeddings (Optional Add-on):** Introduce a lightweight in-memory vector store (or SQLite extension) for local semantic similarity, deduplication, and clustering without external network calls.
- **Developer Experience:** Add practical onboarding guidance and templates for teams creating new vertical applications on top of `go-llm-pipeline`.

### Phase 4: Platform Stabilization (Month 7+)

- **API Freeze:** Lock public APIs for `pkg/analyzer` and `pkg/llmvcr` under semantic versioning.
- **Documentation:** Add comprehensive GoDoc examples and document concurrency patterns (worker pools, `errgroup` usage) for mixed local/remote LLM workload orchestration.
- **Ecosystem Validation:** Maintain `flickr-organizer` as a first-party reference app and add at least one additional non-photo domain example to validate cross-domain product fit.
