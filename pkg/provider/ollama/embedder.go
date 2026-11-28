package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/trace"
)

const embedEndpoint = "/api/embed"
const embedRegionName = "Embed_Ollama"

const (
	defaultEmbedMaxRetries  = 2
	defaultEmbedBaseBackoff = 200 * time.Millisecond
	defaultEmbedMaxBackoff  = 5 * time.Second
)

// Sentinel errors returned by the Embedder. Callers use errors.Is to detect a
// category without inspecting error text.
var (
	// ErrEmptyInput is returned when an input string is empty or a batch has
	// no inputs.
	ErrEmptyInput = errors.New("empty input")
	// ErrEmptyModel is returned when the embedder was constructed without a
	// model name.
	ErrEmptyModel = errors.New("empty model")
	// ErrDimensionMismatch is returned when a batch response contains
	// embeddings of differing lengths, which would make similarity scoring
	// undefined downstream.
	ErrDimensionMismatch = errors.New("dimension mismatch")
)

// embedRequest is the JSON body sent to Ollama's /api/embed endpoint. Input is
// a string for a single embedding or a []string for native batch embedding.
type embedRequest struct {
	Model string `json:"model"`
	Input any    `json:"input"`
}

// embedResponse is the JSON body returned by /api/embed. Embeddings is aligned
// positionally with the request inputs.
type embedResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// Embedder produces embedding vectors from a locally running Ollama instance
// via the /api/embed endpoint. It returns plain [][]float64 / []float64 so it
// stays decoupled from any vector store; callers convert the result into their
// own vector type (for example vectorstore.Vector, which is defined as
// []float64) for free.
//
// An Embedder is safe for concurrent use by multiple goroutines.
type Embedder struct {
	host        string
	model       string
	client      *http.Client
	logger      *slog.Logger
	tracer      Tracer
	maxRetries  int
	baseBackoff time.Duration
	maxBackoff  time.Duration
	// sleep waits for d or until ctx is cancelled. Injecting it decouples the
	// retry loop from the wall clock so callers (including tests) can supply a
	// fake clock.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewEmbedder returns an Embedder targeting the Ollama server at host using the
// given embedding model. It accepts the same options as NewProvider; the
// HTTP client, logger, tracer, timeout, and retry budget are honored, while
// generation-only options (num_ctx, num_predict, response schema, keep-alive)
// are ignored.
func NewEmbedder(host, model string, opts ...ProviderOption) *Embedder {
	cfg := providerConfig{
		maxRetries:       defaultEmbedMaxRetries,
		retryBaseBackoff: defaultEmbedBaseBackoff,
		retryMaxBackoff:  defaultEmbedMaxBackoff,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.tracer == nil {
		cfg.tracer = trace.NewNoOpTracer()
	}
	client := httputil.SameOriginClient(cfg.client, cfg.httpTimeout)
	maxRetries := cfg.maxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	baseBackoff := cfg.retryBaseBackoff
	if baseBackoff <= 0 {
		baseBackoff = defaultEmbedBaseBackoff
	}
	// Clamp the base to the cap so even the first retry honors maxDelay; a
	// longer server-provided Retry-After can still override it per attempt.
	if cfg.retryMaxBackoff > 0 && baseBackoff > cfg.retryMaxBackoff {
		baseBackoff = cfg.retryMaxBackoff
	}
	return &Embedder{
		host:        host,
		model:       model,
		client:      client,
		logger:      cfg.logger,
		tracer:      cfg.tracer,
		maxRetries:  maxRetries,
		baseBackoff: baseBackoff,
		maxBackoff:  cfg.retryMaxBackoff,
		sleep:       sleepCtx,
	}
}

// Model returns the embedding model this Embedder was constructed with.
func (e *Embedder) Model() string { return e.model }

// Embed returns the embedding vector for a single input string.
func (e *Embedder) Embed(ctx context.Context, input string) ([]float64, error) {
	var verr error
	if input == "" {
		verr = errors.Join(verr, ErrEmptyInput)
	}
	if e.model == "" {
		verr = errors.Join(verr, ErrEmptyModel)
	}
	if verr != nil {
		return nil, verr
	}
	embs, err := e.embed(ctx, input, 1)
	if err != nil {
		return nil, err
	}
	return embs[0], nil
}

// EmbedBatch returns embedding vectors for inputs in a single round-trip,
// positionally aligned with inputs. All returned vectors share the same
// dimension; a batch that returns mismatched dimensions yields
// ErrDimensionMismatch.
func (e *Embedder) EmbedBatch(ctx context.Context, inputs []string) ([][]float64, error) {
	var verr error
	if len(inputs) == 0 {
		verr = errors.Join(verr, ErrEmptyInput)
	}
	for i, in := range inputs {
		if in == "" {
			verr = errors.Join(verr, fmt.Errorf("%w: input at index %d", ErrEmptyInput, i))
		}
	}
	if e.model == "" {
		verr = errors.Join(verr, ErrEmptyModel)
	}
	if verr != nil {
		return nil, verr
	}
	return e.embed(ctx, inputs, len(inputs))
}

// embed performs the request with bounded retries and validates that the
// response contains wantN embeddings of a single, consistent dimension.
func (e *Embedder) embed(ctx context.Context, input any, wantN int) ([][]float64, error) {
	defer e.tracer.StartRegion(ctx, embedRegionName).End()

	var lastErr error
	backoff := e.baseBackoff
	for attempt := 0; attempt <= e.maxRetries; attempt++ {
		if attempt > 0 {
			if err := e.sleep(ctx, backoff); err != nil {
				return nil, fmt.Errorf("embed: %w", err)
			}
			backoff *= 2
			if e.maxBackoff > 0 && backoff > e.maxBackoff {
				backoff = e.maxBackoff
			}
		}
		res := e.doEmbed(ctx, input)
		if res.err == nil {
			return validateEmbeddings(res.embeddings, wantN)
		}
		lastErr = res.err
		if res.tier != provpkg.TierShortTransient && res.tier != provpkg.TierShortRateLimit {
			return nil, res.err
		}
		if res.retryAfter > backoff {
			backoff = res.retryAfter
		}
	}
	return nil, fmt.Errorf("embed: retry budget exhausted after %d attempts: %w", e.maxRetries+1, lastErr)
}

// embedResult carries one attempt's outcome so embed can decide whether to
// retry without re-reading the response.
type embedResult struct {
	embeddings [][]float64
	tier       provpkg.ErrorTier
	retryAfter time.Duration
	err        error
}

func (e *Embedder) doEmbed(ctx context.Context, input any) embedResult {
	res, err := doJSONPost(ctx, jsonPostRequest{
		client: e.client,
		logger: e.logger,
		host:   e.host,
		path:   embedEndpoint,
		body:   embedRequest{Model: e.model, Input: input},
	})
	if err != nil {
		if errors.Is(err, errRequestNotSent) {
			return embedResult{tier: provpkg.TierTerminal, err: err}
		}
		return embedResult{tier: provpkg.TierShortTransient, retryAfter: res.retryAfter, err: fmt.Errorf("embed: %w", err)}
	}

	if res.statusCode != http.StatusOK {
		return embedResult{
			tier:       classifyTier(res.statusCode, nil),
			retryAfter: res.retryAfter,
			err:        fmt.Errorf("%w: %d %s", provpkg.ErrUnexpectedStatus, res.statusCode, strings.TrimSpace(string(res.body))),
		}
	}

	var er embedResponse
	if err := json.Unmarshal(res.body, &er); err != nil {
		return embedResult{tier: provpkg.TierTerminal, err: fmt.Errorf("%w: parsing embed response: %w", provpkg.ErrInvalidResponsePart, err)}
	}
	return embedResult{embeddings: er.Embeddings}
}

// validateEmbeddings enforces that the response holds exactly wantN vectors of
// a single non-zero dimension.
func validateEmbeddings(embeddings [][]float64, wantN int) ([][]float64, error) {
	if len(embeddings) != wantN {
		return nil, fmt.Errorf("%w: got %d embeddings, want %d", provpkg.ErrInvalidResponsePart, len(embeddings), wantN)
	}
	dim := len(embeddings[0])
	for i, v := range embeddings {
		if len(v) != dim {
			return nil, fmt.Errorf("%w: embedding %d has dimension %d, want %d", ErrDimensionMismatch, i, len(v), dim)
		}
	}
	if dim == 0 {
		return nil, fmt.Errorf("%w: empty embedding vector", provpkg.ErrInvalidResponsePart)
	}
	return embeddings, nil
}

// sleepCtx waits for d or until ctx is cancelled, whichever comes first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
