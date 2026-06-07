# go-llm-pipeline

An idiomatic, dependency-free Go toolkit for local-first, deterministic LLM orchestration and structured data extraction.

## Roadmap (extension of `vmontagh/flickr-organizer`)

This project extends the `flickr-organizer` approach into a reusable, domain-agnostic Go toolkit.

### Proposal: The go-llm-pipeline Project

- **Objective:** Build an idiomatic, dependency-free Go toolkit for local-first, deterministic LLM orchestration and structured data extraction.
- **Stance:** Most AI frameworks are centered on Python, dynamic chat interfaces, and live API calls. This project provides a contrasting approach with statically typed pipelines, deterministic VCR-style testing, and batch-processing generic assets using concurrent worker pools.

### Core Value Propositions

- **Zero-Cost, Deterministic CI/CD:** An LLM-aware cassette testing package that records HTTP interactions and replays them locally, with sensitive token scrubbing by default.
- **Type-Safe Extraction:** Force LLM outputs directly into strict Go structs.
- **Local-First:** Route natively to local inference engines (Ollama) with configurable concurrency caps.

### Phase 1: The Testing Core and Abstraction (Next 1-2 Months)

- **Productize the VCR Package (`pkg/llmvcr`):**
  - Extract the current cassette/proxy workflow into a standalone package.
  - Ensure it natively intercepts requests from standard Go HTTP clients used by OpenAI and Gemini SDKs.
  - Implement automatic redaction for common sensitive headers (for example, `Authorization` and API keys) before writing YAML/JSON cassettes.
- **Define the Generic Asset Interface:**
  - Replace the Flickr-specific `Photo` type in the analyzer with a domain-agnostic `Asset` struct.
  
```go
type Asset struct {
    ID       string         // Stable unique identifier (for example, URI, filename, or external ID).
    Payload  io.Reader      // Raw asset bytes consumed by the pipeline.
    Metadata map[string]any // Provider-specific attributes that should not be part of raw payload bytes.
}
```

This interface should support a `.DNG` from a Pixii, an `.ARW` from a Sony camera, or a raw `.txt` file using the same pipeline contract.

- **Update the Reference App:** Refactor `flickr-organizer` to import these extracted packages and validate real-world batch-processing utility.

### Phase 2: Structured Data & Schema Enforcement (Months 3-4)

- **Struct Mapping:** Implement utilities using JSON Schema validation or provider-specific structured output features to unmarshal LLM responses directly into Go structs.
- **Domain Applications:** Document rigid data extraction use cases, such as parsing unstructured clinical notes into FHIR `VerificationResult` resources or normalizing inconsistent logs into a standard schema.
- **Fallback & Retry Logic:** Add automatic retries that feed validation errors back to the LLM (for example, field-level schema failures and JSON formatting corrections).

### Phase 3: Operator Ergonomics & Local Execution (Months 5-6)

- **Diagnostic Subcommand (`<app> doctor`):**
  - Check local environment readiness before batch jobs.
  - Verify OS file descriptors.
  - Test network latency to hosted APIs.
  - Validate API keys.
  - Probe local Ollama instances and list loaded models.
- **Local Embeddings (Optional Add-on):** Introduce a lightweight in-memory vector store (or SQLite extension) for local semantic similarity, deduplication, and clustering without external network calls.

### Phase 4: Platform Stabilization (Month 7+)

- **API Freeze:** Lock public APIs for `pkg/analyzer` and `pkg/llmvcr` under semantic versioning.
- **Documentation:** Add comprehensive GoDoc examples and document concurrency patterns (worker pools, `errgroup` usage) for mixed local/remote LLM workload orchestration.
