// Package semcache provides an optional, local-first semantic response cache
// that sits in front of provider calls and reuses prior responses for repeated
// or near-duplicate prompts.
//
// The cache offers two lookup paths. The fast path is an exact match on a
// content hash of the normalized (model, params, prompt) key; it needs no
// embeddings and is always active, so the default configuration is
// exact-match-only and fully offline. The optional fallback is a semantic match:
// when semantic mode is enabled via [WithSemantic], the prompt is embedded by a
// caller-supplied [Embedder] and the nearest neighbor in an embedding index is
// accepted when its cosine similarity meets the configured threshold. Semantic
// reuse is therefore strictly opt-in.
//
// Entries are keyed on model, params, and prompt together, so responses from
// different models or parameter sets never collide. In semantic mode the vector
// store is the single source of truth for a cached response: the response is
// carried in the indexed item's metadata and the recency index holds only
// bookkeeping. The cache never computes embeddings itself: callers own the
// embedding model and inject it through the [Embedder] seam. The cache is safe
// for concurrent use.
package semcache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
)

// Sentinel errors returned by the package. Callers use errors.Is to detect a
// category without inspecting error text.
var (
	// ErrEmptyModel is returned when a Key has no model.
	ErrEmptyModel = errors.New("empty model")
	// ErrEmptyPrompt is returned when a Key has no prompt.
	ErrEmptyPrompt = errors.New("empty prompt")
	// ErrNoEmbedder is returned by New when semantic mode is requested without
	// an embedder to produce query vectors.
	ErrNoEmbedder = errors.New("nil embedder")
	// ErrInvalidThreshold is returned when a semantic similarity threshold is
	// NaN or falls outside the cosine range [-1, 1].
	ErrInvalidThreshold = errors.New("invalid threshold")
	// ErrInvalidMaxEntries is returned when a maximum entry count is negative.
	ErrInvalidMaxEntries = errors.New("invalid max entries")
)

// Metric names reported through a [MetricsRecorder].
const (
	// MetricHits counts lookups served from the cache by either path.
	MetricHits = "semcache_hits"
	// MetricMisses counts lookups with no usable cached response.
	MetricMisses = "semcache_misses"
	// MetricExactHits counts hits served by the exact-match fast path.
	MetricExactHits = "semcache_exact_hits"
	// MetricSemanticHits counts hits served by the semantic fallback.
	MetricSemanticHits = "semcache_semantic_hits"
	// MetricEvictions counts entries removed to satisfy the max-entries bound.
	MetricEvictions = "semcache_evictions"
)

// Metadata keys carried on each indexed vectorstore.Item. In semantic mode
// metaResponse holds the authoritative cached response.
const (
	metaHash     = "hash"
	metaModel    = "model"
	metaResponse = "response"
)

// Embedder converts prompt text into an embedding vector. It is declared here,
// at the consumer, so the cache stays decoupled from any concrete embedding
// provider; callers inject an implementation (for example an Ollama-backed
// embedder) through [WithSemantic].
type Embedder interface {
	// Embed returns the embedding for input.
	Embed(ctx context.Context, input string) ([]float64, error)
}

// MetricsRecorder receives named counter increments for cache activity. It is a
// minimal consumer-side seam so the cache can report to an external registry
// (such as *observ.Metrics) without importing it. A nil recorder disables
// external reporting; the cache always maintains its own [Stats] regardless.
type MetricsRecorder interface {
	// Inc increments the counter identified by name by one.
	Inc(name string)
}

// Key identifies a cached response. Entries are keyed on all three fields
// together, so responses from different models or parameter sets never collide.
type Key struct {
	// Model is the provider model identifier, for example "llama3".
	Model string
	// Params holds request parameters that affect the response, such as
	// temperature or max tokens. It is canonicalized (sorted by key) before
	// hashing, so ordering does not affect the identity of a key.
	Params map[string]string
	// Prompt is the full prompt text.
	Prompt string
}

// Stats is a point-in-time snapshot of cache activity counters.
type Stats struct {
	// Hits is the total number of lookups served from the cache.
	Hits int64
	// Misses is the number of lookups with no usable cached response.
	Misses int64
	// ExactHits is the number of hits served by the exact-match fast path.
	ExactHits int64
	// SemanticHits is the number of hits served by the semantic fallback.
	SemanticHits int64
	// Evictions is the number of entries removed to satisfy the max-entries
	// bound.
	Evictions int64
}

// indexValue is the per-hash payload kept in the eviction index. In exact-only
// mode response holds the cached response and partition is empty. In semantic
// mode partition names the vector store that owns the response (the single
// source of truth) and response is empty.
type indexValue struct {
	partition string
	response  string
}

// cacheIndex maps a key hash to its indexValue with recency-aware eviction. get
// promotes recency; add reports whether inserting evicted an entry. Both
// implementations are used only while the caller holds Cache.mu.
type cacheIndex interface {
	get(hash string) (indexValue, bool)
	contains(hash string) bool
	add(hash string, v indexValue) (evicted bool)
	len() int
}

// boundedIndex is an LRU-bounded cacheIndex backed by golang-lru. Its eviction
// callback runs synchronously inside add.
type boundedIndex struct {
	lru *lru.Cache[string, indexValue]
}

func (b *boundedIndex) get(hash string) (indexValue, bool) { return b.lru.Get(hash) }
func (b *boundedIndex) contains(hash string) bool          { return b.lru.Contains(hash) }
func (b *boundedIndex) add(hash string, v indexValue) bool { return b.lru.Add(hash, v) }
func (b *boundedIndex) len() int                           { return b.lru.Len() }

// unboundedIndex is an unbounded cacheIndex backed by a plain map. It never
// evicts, so golang-lru's positive-size requirement does not apply.
type unboundedIndex struct {
	items map[string]indexValue
}

func newUnboundedIndex() *unboundedIndex {
	return &unboundedIndex{items: make(map[string]indexValue)}
}

func (u *unboundedIndex) get(hash string) (indexValue, bool) {
	v, ok := u.items[hash]
	return v, ok
}

func (u *unboundedIndex) contains(hash string) bool {
	_, ok := u.items[hash]
	return ok
}

func (u *unboundedIndex) add(hash string, v indexValue) bool {
	u.items[hash] = v
	return false
}

func (u *unboundedIndex) len() int { return len(u.items) }

// config accumulates option values before New validates them.
type config struct {
	embedder   Embedder
	threshold  float64
	semantic   bool
	maxEntries int
	metrics    MetricsRecorder
	logger     *slog.Logger
}

// Option customizes a Cache at construction time.
type Option func(*config)

// WithSemantic enables the semantic fallback path using embedder to embed
// prompts and accepting a neighbor whose cosine similarity is at least
// threshold. threshold must lie in the cosine range [-1, 1]. Without this
// option the cache is exact-match-only.
func WithSemantic(embedder Embedder, threshold float64) Option {
	return func(c *config) {
		c.semantic = true
		c.embedder = embedder
		c.threshold = threshold
	}
}

// WithMaxEntries bounds the cache to at most n entries, evicting the least
// recently used entry once full. n must not be negative; zero leaves the cache
// unbounded, as does omitting this option.
func WithMaxEntries(n int) Option {
	return func(c *config) { c.maxEntries = n }
}

// WithMetrics reports hit, miss, and eviction counters to recorder in addition
// to the always-maintained [Cache.Stats]. A nil recorder is ignored.
func WithMetrics(recorder MetricsRecorder) Option {
	return func(c *config) { c.metrics = recorder }
}

// WithLogger sets the logger used for debug-level diagnostics. Without it the
// cache does not log.
func WithLogger(logger *slog.Logger) Option {
	return func(c *config) { c.logger = logger }
}

// Cache is a concurrency-safe semantic response cache. The zero value is not
// usable; construct one with [New].
type Cache struct {
	embedder   Embedder
	threshold  float64
	semantic   bool
	maxEntries int
	metrics    MetricsRecorder
	logger     *slog.Logger

	mu        sync.Mutex
	index     cacheIndex
	stores    map[string]*vectorstore.MemoryStore // partitionKey -> semantic index; nil unless semantic is enabled
	partCount map[string]int                      // partitionKey -> live entry count; nil unless semantic is enabled

	stats Stats
}

// New returns a Cache configured by opts. By default the cache is
// exact-match-only and unbounded, discarding the least recently used entry once
// a configured maximum is reached. It returns an error when semantic mode is
// requested without an embedder or with an out-of-range threshold, or when a
// negative max-entries value is supplied.
func New(opts ...Option) (*Cache, error) {
	var cfg config
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	if cfg.semantic {
		if cfg.embedder == nil {
			return nil, fmt.Errorf("configuring semantic cache: %w", ErrNoEmbedder)
		}
		if math.IsNaN(cfg.threshold) || cfg.threshold < -1 || cfg.threshold > 1 {
			return nil, fmt.Errorf("configuring semantic cache: %w: %v", ErrInvalidThreshold, cfg.threshold)
		}
	}
	if cfg.maxEntries < 0 {
		return nil, fmt.Errorf("configuring cache: %w: %d", ErrInvalidMaxEntries, cfg.maxEntries)
	}

	c := &Cache{
		embedder:   cfg.embedder,
		threshold:  cfg.threshold,
		semantic:   cfg.semantic,
		maxEntries: cfg.maxEntries,
		metrics:    cfg.metrics,
		logger:     cfg.logger,
	}
	if cfg.semantic {
		c.stores = make(map[string]*vectorstore.MemoryStore)
		c.partCount = make(map[string]int)
	}
	if cfg.maxEntries > 0 {
		l, err := lru.NewWithEvict[string, indexValue](cfg.maxEntries, c.onEvict)
		if err != nil {
			return nil, fmt.Errorf("configuring cache index: %w", err)
		}
		c.index = &boundedIndex{lru: l}
	} else {
		c.index = newUnboundedIndex()
	}
	return c, nil
}

// Get returns a cached response for key. It first tries the exact-match fast
// path, then, when semantic mode is enabled, the embedding nearest-neighbor
// fallback. The boolean reports whether a usable response was found; a miss is
// not an error. An error is returned only for an invalid key, a cancelled
// context, or an embedding failure on the semantic path.
func (c *Cache) Get(ctx context.Context, key Key) (response string, hit bool, err error) {
	hash, err := hashKey(key)
	if err != nil {
		return "", false, fmt.Errorf("cache get: %w", err)
	}

	response, ok, err := c.getExact(ctx, hash)
	if err != nil {
		return "", false, fmt.Errorf("cache get: %w", err)
	}
	if ok {
		c.record(true, MetricExactHits)
		return response, true, nil
	}

	if !c.semantic {
		c.record(false, "")
		return "", false, nil
	}

	response, ok, err = c.getSemantic(ctx, key)
	if err != nil {
		return "", false, fmt.Errorf("cache get: %w", err)
	}
	if ok {
		c.record(true, MetricSemanticHits)
		return response, true, nil
	}
	c.record(false, "")
	return "", false, nil
}

// getExact returns the response stored under hash via the exact-match fast
// path, promoting its recency. In semantic mode the response is read from the
// partition's vector store, the single source of truth; in exact-only mode it
// comes straight from the index value.
func (c *Cache) getExact(ctx context.Context, hash string) (response string, hit bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", false, fmt.Errorf("cache get: %w", err)
	}
	v, ok := c.index.get(hash)
	if !ok {
		return "", false, nil
	}
	if !c.semantic {
		return v.response, true, nil
	}
	store := c.stores[v.partition]
	if store == nil {
		return "", false, nil
	}
	item, ok, err := store.Get(ctx, hash)
	if err != nil {
		return "", false, fmt.Errorf("reading cached response: %w", err)
	}
	if !ok {
		return "", false, nil
	}
	return item.Metadata[metaResponse], true, nil
}

// getSemantic embeds key.Prompt and returns the nearest stored response within
// the key's model+params partition whose cosine similarity meets the threshold.
// Partitioning by model and params keeps semantic matches from crossing model
// or parameter boundaries, so a match never returns another model's response.
// The response travels with the neighbor's metadata, so a vector present in the
// store always carries a valid payload.
func (c *Cache) getSemantic(ctx context.Context, key Key) (response string, hit bool, err error) {
	partition, err := partitionKey(key)
	if err != nil {
		return "", false, err
	}

	store := c.lookupPartition(partition)
	if store == nil {
		// No entries for this model+params yet: a clean miss, no query needed.
		return "", false, nil
	}

	vector, err := c.embedder.Embed(ctx, key.Prompt)
	if err != nil {
		return "", false, fmt.Errorf("embedding prompt: %w", err)
	}

	neighbors, err := store.Nearest(ctx, vector, 1)
	if err != nil {
		if errors.Is(err, vectorstore.ErrDimensionMismatch) {
			// A different embedding dimension than the indexed vectors means no
			// comparable neighbor exists, so treat it as a miss. Every other
			// error, including context cancellation, propagates.
			return "", false, nil
		}
		return "", false, fmt.Errorf("querying semantic index: %w", err)
	}
	if len(neighbors) == 0 || neighbors[0].Score < c.threshold {
		return "", false, nil
	}

	top := neighbors[0]
	response = top.Item.Metadata[metaResponse]

	c.mu.Lock()
	// Promote the matched entry's recency; if it was concurrently evicted the
	// lookup is a no-op and the already-read response is still returned.
	c.index.get(top.Item.ID)
	c.mu.Unlock()
	if c.logger != nil {
		c.logger.Debug("semcache semantic hit",
			slog.String("model", key.Model),
			slog.String("match_hash", top.Item.ID),
			slog.Float64("score", top.Score))
	}
	return response, true, nil
}

// lookupPartition returns the semantic store for partition, or nil if none has
// been created yet.
func (c *Cache) lookupPartition(partition string) *vectorstore.MemoryStore {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stores[partition]
}

// Put stores response under key, replacing any existing entry with the same
// key. When semantic mode is enabled it also indexes the prompt embedding so
// future near-duplicate prompts can be served from the semantic path. Storing
// past the configured maximum evicts the least recently used entry.
func (c *Cache) Put(ctx context.Context, key Key, response string) error {
	hash, err := hashKey(key)
	if err != nil {
		return fmt.Errorf("cache put: %w", err)
	}

	var (
		partition string
		item      vectorstore.Item
	)
	if c.semantic {
		partition, err = partitionKey(key)
		if err != nil {
			return fmt.Errorf("cache put: %w", err)
		}
		// Embed before locking: embedding is slow and external, so it must not
		// hold c.mu. The index add and the map commit then happen together
		// under a single lock hold so no concurrent Put or eviction can observe
		// a half-applied entry.
		vector, embErr := c.embedder.Embed(ctx, key.Prompt)
		if embErr != nil {
			return fmt.Errorf("cache put: embedding prompt: %w", embErr)
		}
		item = vectorstore.Item{
			ID:     hash,
			Vector: vector,
			Metadata: map[string]string{
				metaHash:     hash,
				metaModel:    key.Model,
				metaResponse: response,
			},
		}
	}

	evicted, err := c.commit(ctx, hash, partition, item, response)
	if err != nil {
		return fmt.Errorf("cache put: %w", err)
	}
	// Report the eviction to the external recorder only after releasing the
	// lock, so a blocking or re-entrant recorder cannot deadlock the cache.
	if evicted {
		c.recordEvictions(1)
	}
	return nil
}

// commit applies one Put atomically under c.mu: it indexes the semantic vector
// (when enabled) and records the entry in the eviction index, whose onEvict
// callback performs any semantic cleanup synchronously on this same goroutine.
// It reports whether an entry was evicted so the caller can notify the recorder
// after releasing the lock. The vectorstore methods it calls hold only their
// own locks and never re-enter the cache, so the c.mu-then-store order cannot
// deadlock.
func (c *Cache) commit(ctx context.Context, hash, partition string, item vectorstore.Item, response string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Re-check cancellation before any mutation, mirroring getExact and the
	// MemoryStore methods; this guards exact-only Put, whose only ctx-aware
	// call (store.Add) is skipped.
	if err := ctx.Err(); err != nil {
		return false, err
	}

	v := indexValue{partition: partition, response: response}
	if c.semantic {
		// The vector store is the single source of truth in semantic mode, so
		// the index keeps no second copy of the response.
		v.response = ""
		store := c.stores[partition]
		created := store == nil
		if created {
			store = vectorstore.NewMemoryStore()
		}
		if err := store.Add(ctx, item); err != nil {
			return false, fmt.Errorf("indexing embedding: %w", err)
		}
		if created {
			c.stores[partition] = store
		}
		if !c.index.contains(hash) {
			c.partCount[partition]++
		}
	}
	return c.index.add(hash, v), nil
}

// onEvict runs when the bounded index discards an entry. The bounded index only
// mutates from commit while c.mu is held, and golang-lru invokes this callback
// synchronously on that same goroutine, so it may touch stores, partCount, and
// stats without re-locking (re-locking would deadlock). It must not call the
// external recorder; the caller reports the eviction after releasing the lock.
func (c *Cache) onEvict(hash string, v indexValue) {
	c.stats.Evictions++
	if !c.semantic {
		return
	}
	if store := c.stores[v.partition]; store != nil {
		// A background context makes the internal delete uncancellable, so a
		// cancelled Get or Put can never leave a stale vector behind.
		if _, err := store.Delete(context.Background(), hash); err != nil && c.logger != nil {
			c.logger.Debug("semcache evict: semantic delete failed",
				slog.String("hash", hash), slog.Any("error", err))
		}
	}
	c.partCount[v.partition]--
	if c.partCount[v.partition] <= 0 {
		// The partition is empty: drop its store and counter so a bounded cache
		// does not accumulate one empty store per distinct key.
		delete(c.partCount, v.partition)
		delete(c.stores, v.partition)
	}
}

// recordEvictions mirrors evicted eviction counts to the external recorder. It
// is called after c.mu is released so the recorder never runs under the lock.
func (c *Cache) recordEvictions(evicted int) {
	if c.metrics == nil {
		return
	}
	for range evicted {
		c.metrics.Inc(MetricEvictions)
	}
}

// Len returns the number of entries currently held.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.index.len()
}

// Stats returns a snapshot of cache activity counters.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// record updates the internal counters and mirrors them to the external
// recorder. metric is the path-specific hit counter name and is empty on a
// miss.
func (c *Cache) record(hit bool, metric string) {
	c.mu.Lock()
	if hit {
		c.stats.Hits++
		switch metric {
		case MetricExactHits:
			c.stats.ExactHits++
		case MetricSemanticHits:
			c.stats.SemanticHits++
		}
	} else {
		c.stats.Misses++
	}
	c.mu.Unlock()

	if c.metrics == nil {
		return
	}
	if hit {
		c.metrics.Inc(MetricHits)
		if metric != "" {
			c.metrics.Inc(metric)
		}
		return
	}
	c.metrics.Inc(MetricMisses)
}

// encodeModelParams writes the length-prefixed model and canonicalized params
// of key to b. Params are sorted so map iteration order cannot change the
// encoding. Prompt is intentionally excluded so this encoding also forms the
// semantic partition key.
func encodeModelParams(b *strings.Builder, key Key) {
	writeField(b, key.Model)

	names := make([]string, 0, len(key.Params))
	for name := range key.Params {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeField(b, name)
		writeField(b, key.Params[name])
	}
}

// hashKey returns a stable SHA-256 hash of the normalized model, params, and
// prompt. Every field is length-prefixed so no combination of field contents
// can collide with another.
func hashKey(key Key) (string, error) {
	if key.Model == "" {
		return "", ErrEmptyModel
	}
	if key.Prompt == "" {
		return "", ErrEmptyPrompt
	}

	var b strings.Builder
	encodeModelParams(&b, key)
	writeField(&b, key.Prompt)

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

// partitionKey returns a stable SHA-256 hash of the model and params only.
// Entries that share a partition key share a semantic index, so semantic
// nearest-neighbor matches can never cross model or parameter boundaries.
func partitionKey(key Key) (string, error) {
	if key.Model == "" {
		return "", ErrEmptyModel
	}

	var b strings.Builder
	encodeModelParams(&b, key)

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:]), nil
}

// writeField appends a length-prefixed field to b so field boundaries are
// unambiguous regardless of the bytes each field contains.
func writeField(b *strings.Builder, s string) {
	fmt.Fprintf(b, "%d:%s", len(s), s)
}
