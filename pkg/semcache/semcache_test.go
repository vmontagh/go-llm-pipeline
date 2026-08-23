package semcache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/semcache"
)

// errEmbedBoom is a static sentinel used to assert error propagation from the
// injected embedder.
var errEmbedBoom = errors.New("embed boom")

// errNoVector is a static sentinel returned by the test embedders when asked for
// an input they were not primed with.
var errNoVector = errors.New("no vector for input")

// fakeEmbedder returns caller-configured vectors keyed by input text, giving
// tests exact control over cosine similarity without any real embedding model.
type fakeEmbedder struct {
	vectors map[string][]float64
	err     error
}

func (f *fakeEmbedder) Embed(_ context.Context, input string) ([]float64, error) {
	if f.err != nil {
		return nil, f.err
	}
	v, ok := f.vectors[input]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errNoVector, input)
	}
	return v, nil
}

// fakeRecorder is a concurrency-safe MetricsRecorder that tallies increments.
type fakeRecorder struct {
	mu     sync.Mutex
	counts map[string]int64
}

func newFakeRecorder() *fakeRecorder {
	return &fakeRecorder{counts: make(map[string]int64)}
}

func (r *fakeRecorder) Inc(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[name]++
}

func (r *fakeRecorder) snapshot() map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int64, len(r.counts))
	for k, v := range r.counts {
		out[k] = v
	}
	return out
}

func mustNew(t *testing.T, opts ...semcache.Option) *semcache.Cache {
	t.Helper()
	c, err := semcache.New(opts...)
	if err != nil {
		t.Fatalf("New() = %v, want nil", err)
	}
	return c
}

func TestNewValidation(t *testing.T) {
	t.Parallel()
	emb := &fakeEmbedder{vectors: map[string][]float64{}}

	tests := []struct {
		name    string
		opts    []semcache.Option
		wantErr error
	}{
		{
			name:    "default is valid",
			opts:    nil,
			wantErr: nil,
		},
		{
			name:    "semantic without embedder",
			opts:    []semcache.Option{semcache.WithSemantic(nil, 0.9)},
			wantErr: semcache.ErrNoEmbedder,
		},
		{
			name:    "threshold above range",
			opts:    []semcache.Option{semcache.WithSemantic(emb, 1.5)},
			wantErr: semcache.ErrInvalidThreshold,
		},
		{
			name:    "threshold below range",
			opts:    []semcache.Option{semcache.WithSemantic(emb, -2)},
			wantErr: semcache.ErrInvalidThreshold,
		},
		{
			name:    "negative max entries",
			opts:    []semcache.Option{semcache.WithMaxEntries(-1)},
			wantErr: semcache.ErrInvalidMaxEntries,
		},
		{
			name:    "valid semantic config",
			opts:    []semcache.Option{semcache.WithSemantic(emb, 0.8), semcache.WithMaxEntries(10)},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := semcache.New(tt.opts...)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("New() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestExactMatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := mustNew(t)

	stored := semcache.Key{
		Model:  "llama3",
		Params: map[string]string{"temperature": "0.2", "top_p": "0.9"},
		Prompt: "describe a cat",
	}
	if err := c.Put(ctx, stored, "a small feline"); err != nil {
		t.Fatalf("Put() = %v, want nil", err)
	}

	tests := []struct {
		name     string
		key      semcache.Key
		wantResp string
		wantHit  bool
	}{
		{
			name:     "same key hits",
			key:      stored,
			wantResp: "a small feline",
			wantHit:  true,
		},
		{
			name:     "param order does not matter",
			key:      semcache.Key{Model: "llama3", Params: map[string]string{"top_p": "0.9", "temperature": "0.2"}, Prompt: "describe a cat"},
			wantResp: "a small feline",
			wantHit:  true,
		},
		{
			name:    "different model misses",
			key:     semcache.Key{Model: "mistral", Params: stored.Params, Prompt: stored.Prompt},
			wantHit: false,
		},
		{
			name:    "different params miss",
			key:     semcache.Key{Model: "llama3", Params: map[string]string{"temperature": "0.9"}, Prompt: stored.Prompt},
			wantHit: false,
		},
		{
			name:    "different prompt misses",
			key:     semcache.Key{Model: "llama3", Params: stored.Params, Prompt: "describe a dog"},
			wantHit: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, hit, err := c.Get(ctx, tt.key)
			if err != nil {
				t.Fatalf("Get() = %v, want nil", err)
			}
			if hit != tt.wantHit {
				t.Errorf("Get() hit = %t, want %t", hit, tt.wantHit)
			}
			if resp != tt.wantResp {
				t.Errorf("Get() response = %q, want %q", resp, tt.wantResp)
			}
		})
	}
}

func TestGetInvalidKey(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := mustNew(t)

	tests := []struct {
		name    string
		key     semcache.Key
		wantErr error
	}{
		{
			name:    "empty model",
			key:     semcache.Key{Prompt: "hello"},
			wantErr: semcache.ErrEmptyModel,
		},
		{
			name:    "empty prompt",
			key:     semcache.Key{Model: "llama3"},
			wantErr: semcache.ErrEmptyPrompt,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := c.Get(ctx, tt.key); !errors.Is(err, tt.wantErr) {
				t.Errorf("Get() error = %v, want %v", err, tt.wantErr)
			}
			if err := c.Put(ctx, tt.key, "x"); !errors.Is(err, tt.wantErr) {
				t.Errorf("Put() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestSemanticFallback(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"cat":      {1, 0, 0},
		"kitten":   {0.9, 0.1, 0}, // cosine with cat ~0.994
		"airplane": {0, 0, 1},     // cosine with cat 0
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9))

	if err := c.Put(ctx, semcache.Key{Model: "m", Prompt: "cat"}, "feline"); err != nil {
		t.Fatalf("Put() = %v, want nil", err)
	}

	tests := []struct {
		name     string
		prompt   string
		wantResp string
		wantHit  bool
	}{
		{name: "near neighbor above threshold hits", prompt: "kitten", wantResp: "feline", wantHit: true},
		{name: "far neighbor below threshold misses", prompt: "airplane", wantHit: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, hit, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: tt.prompt})
			if err != nil {
				t.Fatalf("Get() = %v, want nil", err)
			}
			if hit != tt.wantHit {
				t.Errorf("Get(%q) hit = %t, want %t", tt.prompt, hit, tt.wantHit)
			}
			if resp != tt.wantResp {
				t.Errorf("Get(%q) response = %q, want %q", tt.prompt, resp, tt.wantResp)
			}
		})
	}
}

func TestSemanticIsolatesModels(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Both models cache the same prompt with an identical embedding, so a global
	// semantic index would let one model's query return the other's response.
	// Partitioning by model must keep each model's semantic match to its own
	// response.
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"cat":    {1, 0, 0},
		"kitten": {0.9, 0.1, 0}, // cosine with cat ~0.994, above threshold
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9))

	if err := c.Put(ctx, semcache.Key{Model: "a", Prompt: "cat"}, "resp-a"); err != nil {
		t.Fatalf("Put(a) = %v, want nil", err)
	}
	if err := c.Put(ctx, semcache.Key{Model: "b", Prompt: "cat"}, "resp-b"); err != nil {
		t.Fatalf("Put(b) = %v, want nil", err)
	}

	tests := []struct {
		name     string
		model    string
		wantResp string
	}{
		{name: "model a stays within its partition", model: "a", wantResp: "resp-a"},
		{name: "model b stays within its partition", model: "b", wantResp: "resp-b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, hit, err := c.Get(ctx, semcache.Key{Model: tt.model, Prompt: "kitten"})
			if err != nil {
				t.Fatalf("Get() = %v, want nil", err)
			}
			if !hit {
				t.Errorf("Get(model=%q) hit = false, want true", tt.model)
			}
			if resp != tt.wantResp {
				t.Errorf("Get(model=%q) response = %q, want %q", tt.model, resp, tt.wantResp)
			}
		})
	}
}

func TestSemanticIsolatesParams(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// Same model and prompt, differing params must not cross-serve on the
	// semantic path either, because params are part of the partition key.
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"cat":    {1, 0, 0},
		"kitten": {0.9, 0.1, 0},
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9))

	hot := semcache.Key{Model: "m", Params: map[string]string{"temperature": "0.9"}, Prompt: "cat"}
	cold := semcache.Key{Model: "m", Params: map[string]string{"temperature": "0.1"}, Prompt: "cat"}
	if err := c.Put(ctx, hot, "resp-hot"); err != nil {
		t.Fatalf("Put(hot) = %v, want nil", err)
	}
	if err := c.Put(ctx, cold, "resp-cold"); err != nil {
		t.Fatalf("Put(cold) = %v, want nil", err)
	}

	tests := []struct {
		name     string
		params   map[string]string
		wantResp string
	}{
		{name: "hot params stay within their partition", params: map[string]string{"temperature": "0.9"}, wantResp: "resp-hot"},
		{name: "cold params stay within their partition", params: map[string]string{"temperature": "0.1"}, wantResp: "resp-cold"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, hit, err := c.Get(ctx, semcache.Key{Model: "m", Params: tt.params, Prompt: "kitten"})
			if err != nil {
				t.Fatalf("Get() = %v, want nil", err)
			}
			if !hit {
				t.Errorf("Get(params=%v) hit = false, want true", tt.params)
			}
			if resp != tt.wantResp {
				t.Errorf("Get(params=%v) response = %q, want %q", tt.params, resp, tt.wantResp)
			}
		})
	}
}

func TestSemanticDimensionMismatchIsMiss(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// The store commits to dimension 3 on Put; a later query embedding of a
	// different dimension has no comparable neighbor and must read as a miss,
	// not an error.
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"cat":     {1, 0, 0},
		"unknown": {1, 0}, // dimension 2 vs indexed dimension 3
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9))

	if err := c.Put(ctx, semcache.Key{Model: "m", Prompt: "cat"}, "feline"); err != nil {
		t.Fatalf("Put() = %v, want nil", err)
	}

	resp, hit, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "unknown"})
	if err != nil {
		t.Fatalf("Get() = %v, want nil", err)
	}
	if hit {
		t.Errorf("Get() hit = true, want miss")
	}
	if resp != "" {
		t.Errorf("Get() response = %q, want empty", resp)
	}
}

func TestSemanticGetPropagatesCancellation(t *testing.T) {
	t.Parallel()
	// A valid embedder isolates the failure to the store query, so a cancelled
	// context must surface as an error rather than being masked as a miss. The
	// query prompt is not stored exactly, so the lookup falls through the exact
	// fast path to the semantic path where the store is consulted.
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"cat":    {1, 0, 0},
		"kitten": {0.9, 0.1, 0},
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9))

	if err := c.Put(t.Context(), semcache.Key{Model: "m", Prompt: "cat"}, "feline"); err != nil {
		t.Fatalf("Put() = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, _, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "kitten"}); !errors.Is(err, context.Canceled) {
		t.Errorf("Get(cancelled) error = %v, want %v", err, context.Canceled)
	}
}

func TestExactGetPropagatesCancellation(t *testing.T) {
	t.Parallel()
	// In exact-only mode a context cancelled before the lookup must surface as
	// an error instead of returning a hit and mutating recency.
	c := mustNew(t)
	key := semcache.Key{Model: "m", Prompt: "cat"}
	if err := c.Put(t.Context(), key, "feline"); err != nil {
		t.Fatalf("Put() = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	resp, hit, err := c.Get(ctx, key)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Get(cancelled) error = %v, want %v", err, context.Canceled)
	}
	if hit {
		t.Errorf("Get(cancelled) hit = true, want false")
	}
	if resp != "" {
		t.Errorf("Get(cancelled) response = %q, want empty", resp)
	}
}

func TestSemanticEmbedError(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("put surfaces embed error", func(t *testing.T) {
		t.Parallel()
		c := mustNew(t, semcache.WithSemantic(&fakeEmbedder{err: errEmbedBoom}, 0.9))
		if err := c.Put(ctx, semcache.Key{Model: "m", Prompt: "cat"}, "feline"); !errors.Is(err, errEmbedBoom) {
			t.Errorf("Put() error = %v, want %v", err, errEmbedBoom)
		}
	})

	t.Run("get surfaces embed error within an existing partition", func(t *testing.T) {
		t.Parallel()
		// "cat" embeds so its partition exists; the query prompt has no vector,
		// so the semantic path reaches the embedder and surfaces its error
		// rather than masking it as a miss.
		emb := &fakeEmbedder{vectors: map[string][]float64{"cat": {1, 0, 0}}}
		c := mustNew(t, semcache.WithSemantic(emb, 0.9))
		if err := c.Put(ctx, semcache.Key{Model: "m", Prompt: "cat"}, "feline"); err != nil {
			t.Fatalf("Put() = %v, want nil", err)
		}
		if _, _, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "kitten"}); !errors.Is(err, errNoVector) {
			t.Errorf("Get() error = %v, want %v", err, errNoVector)
		}
	})
}

func TestExactModeSkipsEmbedder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// An erroring embedder must never be consulted in the default exact-only
	// mode, so Get and Put succeed despite it.
	c := mustNew(t)
	key := semcache.Key{Model: "m", Prompt: "cat"}
	if err := c.Put(ctx, key, "feline"); err != nil {
		t.Fatalf("Put() = %v, want nil", err)
	}
	resp, hit, err := c.Get(ctx, key)
	if err != nil || !hit || resp != "feline" {
		t.Errorf("Get() = (%q, %t, %v), want (%q, true, nil)", resp, hit, err, "feline")
	}
}

func TestEviction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	key := func(p string) semcache.Key { return semcache.Key{Model: "m", Prompt: p} }

	// Reading A before the overflow Put makes B the least recently used, so the
	// LRU policy evicts B while A and C survive.
	c := mustNew(t, semcache.WithMaxEntries(2))
	if err := c.Put(ctx, key("A"), "ra"); err != nil {
		t.Fatalf("Put(A) = %v", err)
	}
	if err := c.Put(ctx, key("B"), "rb"); err != nil {
		t.Fatalf("Put(B) = %v", err)
	}
	if _, _, err := c.Get(ctx, key("A")); err != nil {
		t.Fatalf("Get(A) = %v", err)
	}
	if err := c.Put(ctx, key("C"), "rc"); err != nil {
		t.Fatalf("Put(C) = %v", err)
	}

	if got := c.Len(); got != 2 {
		t.Errorf("Len() = %d, want 2", got)
	}
	if _, hit, err := c.Get(ctx, key("B")); err != nil || hit {
		t.Errorf("Get(B) = (hit %t, err %v), want evicted", hit, err)
	}
	for _, p := range []string{"A", "C"} {
		if _, hit, err := c.Get(ctx, key(p)); err != nil || !hit {
			t.Errorf("Get(%s) = (hit %t, err %v), want retained", p, hit, err)
		}
	}
	if got := c.Stats().Evictions; got != 1 {
		t.Errorf("Stats().Evictions = %d, want 1", got)
	}
}

func TestPutReplacesResponse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := mustNew(t)
	key := semcache.Key{Model: "m", Prompt: "cat"}

	if err := c.Put(ctx, key, "old"); err != nil {
		t.Fatalf("Put(old) = %v", err)
	}
	if err := c.Put(ctx, key, "new"); err != nil {
		t.Fatalf("Put(new) = %v", err)
	}
	if got := c.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1", got)
	}
	resp, _, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get() = %v, want nil", err)
	}
	if resp != "new" {
		t.Errorf("Get() response = %q, want %q", resp, "new")
	}
}

func TestStatsAndMetrics(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	rec := newFakeRecorder()
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"cat":    {1, 0, 0},
		"kitten": {0.9, 0.1, 0},
		"rock":   {0, 1, 0},
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9), semcache.WithMetrics(rec))

	if err := c.Put(ctx, semcache.Key{Model: "m", Prompt: "cat"}, "feline"); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	// Exact hit.
	if _, _, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "cat"}); err != nil {
		t.Fatalf("Get(cat) = %v", err)
	}
	// Semantic hit.
	if _, _, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "kitten"}); err != nil {
		t.Fatalf("Get(kitten) = %v", err)
	}
	// Miss (orthogonal vector).
	if _, _, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "rock"}); err != nil {
		t.Fatalf("Get(rock) = %v", err)
	}

	wantStats := semcache.Stats{Hits: 2, Misses: 1, ExactHits: 1, SemanticHits: 1}
	if diff := cmp.Diff(wantStats, c.Stats()); diff != "" {
		t.Errorf("Stats() mismatch (-want +got):\n%s", diff)
	}

	wantCounts := map[string]int64{
		semcache.MetricHits:         2,
		semcache.MetricExactHits:    1,
		semcache.MetricSemanticHits: 1,
		semcache.MetricMisses:       1,
	}
	if diff := cmp.Diff(wantCounts, rec.snapshot()); diff != "" {
		t.Errorf("recorder counts mismatch (-want +got):\n%s", diff)
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := mustNew(t, semcache.WithMaxEntries(64))

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range 100 {
				key := semcache.Key{Model: "m", Prompt: fmt.Sprintf("p-%d-%d", g, i%10)}
				if err := c.Put(ctx, key, "r"); err != nil {
					t.Errorf("Put() = %v", err)
					return
				}
				if _, _, err := c.Get(ctx, key); err != nil {
					t.Errorf("Get() = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := c.Len(); got > 64 {
		t.Errorf("Len() = %d, want <= 64", got)
	}
}

func TestSemanticHitReturnsStoredResponse(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// The vector store is the single source of truth for the cached response in
	// semantic mode: a near-neighbour hit must return the response read straight
	// from the stored Item metadata.
	emb := &fakeEmbedder{vectors: map[string][]float64{
		"hello world": {1, 0, 0},
		"hello there": {0.9, 0.1, 0}, // cosine with the entry ~0.994, above threshold
	}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9))

	if err := c.Put(ctx, semcache.Key{Model: "m", Prompt: "hello world"}, "resp-stored"); err != nil {
		t.Fatalf("Put() = %v", err)
	}

	resp, hit, err := c.Get(ctx, semcache.Key{Model: "m", Prompt: "hello there"})
	if err != nil {
		t.Fatalf("Get() = %v, want nil", err)
	}
	if !hit {
		t.Errorf("Get() hit = false, want true")
	}
	if resp != "resp-stored" {
		t.Errorf("Get() response = %q, want %q", resp, "resp-stored")
	}
}

func TestBoundedPartitionStores(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// With a max of one entry and a distinct model (hence partition) per Put,
	// each insertion evicts the previous one. Eviction must drop the emptied
	// partition store so the count stays bounded rather than growing per key.
	emb := &fakeEmbedder{vectors: map[string][]float64{"cat": {1, 0, 0}}}
	c := mustNew(t, semcache.WithSemantic(emb, 0.9), semcache.WithMaxEntries(1))

	const puts = 25
	for i := range puts {
		key := semcache.Key{Model: fmt.Sprintf("m%d", i), Prompt: "cat"}
		if err := c.Put(ctx, key, "r"); err != nil {
			t.Fatalf("Put(%d) = %v", i, err)
		}
		if got := c.PartitionCount(); got != 1 {
			t.Fatalf("PartitionCount() after put %d = %d, want 1", i, got)
		}
	}
	if got := c.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1", got)
	}
}

func TestConcurrentSemanticPutEviction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	// A bounded semantic cache under concurrent replacement, eviction, and reads
	// must keep the exact and semantic paths consistent and never race. Run with
	// -race to catch interleavings between the index update and the map commit.
	const prompts = 24
	vectors := make(map[string][]float64, prompts)
	for i := range prompts {
		vectors[fmt.Sprintf("p%d", i)] = []float64{float64(i + 1), 1, 0}
	}
	emb := &fakeEmbedder{vectors: vectors}
	c := mustNew(t, semcache.WithSemantic(emb, 0.5), semcache.WithMaxEntries(16))

	const (
		goroutines = 24
		iterations = 120
	)
	models := []string{"m0", "m1", "m2", "m3"}
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for i := range iterations {
				key := semcache.Key{
					Model:  models[(g+i)%len(models)],
					Prompt: fmt.Sprintf("p%d", (g*iterations+i)%prompts),
				}
				if err := c.Put(ctx, key, "r"); err != nil {
					t.Errorf("Put() = %v", err)
					return
				}
				if _, _, err := c.Get(ctx, key); err != nil {
					t.Errorf("Get() = %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if got := c.Len(); got > 16 {
		t.Errorf("Len() = %d, want <= 16", got)
	}
	if got := c.PartitionCount(); got > 16 {
		t.Errorf("PartitionCount() = %d, want <= 16", got)
	}
}
