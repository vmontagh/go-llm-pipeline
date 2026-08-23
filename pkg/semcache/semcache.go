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
// different models or parameter sets never collide. The cache never computes
// embeddings itself: callers own the embedding model and inject it through the
// [Embedder] seam. The cache is safe for concurrent use.
package semcache

import (
	"container/list"
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
	// ErrInvalidEviction is returned when an unknown eviction policy is
	// configured.
	ErrInvalidEviction = errors.New("invalid eviction policy")
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

// Metadata keys carried on each indexed vectorstore.Item.
const (
	metaHash     = "hash"
	metaModel    = "model"
	metaResponse = "response"
)

// semanticNeighborFanout is how many nearest neighbors getSemantic requests so
// it can skip stale vectors whose entries were already evicted and still find a
// live neighbor beneath them.
const semanticNeighborFanout = 8

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

// Eviction selects which entry is discarded when the cache is full. The zero
// value is [EvictInvalid] and is rejected by [New]; omitting [WithEviction]
// defaults to [EvictLRU].
type Eviction int

const (
	// EvictInvalid is the zero value and is not a usable policy, so an
	// unset Eviction is rejected rather than silently treated as valid.
	EvictInvalid Eviction = iota
	// EvictLRU discards the least recently used entry, accounting for reads.
	EvictLRU
	// EvictFIFO discards the oldest inserted entry regardless of reads.
	EvictFIFO
)

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

// entry is one cached response together with its normalized key hash and the
// semantic partition it belongs to. The hash doubles as the ID under which the
// entry is indexed in its partition's semantic store.
type entry struct {
	hash      string
	partition string
	response  string
}

// entryOf recovers the *entry stored in a list element. The list holds only
// *entry values, so a failed assertion is an unreachable invariant violation.
func entryOf(el *list.Element) *entry {
	ent, ok := el.Value.(*entry)
	if !ok {
		panic("semcache: list element does not hold *entry")
	}
	return ent
}

// config accumulates option values before New validates them.
type config struct {
	embedder   Embedder
	threshold  float64
	semantic   bool
	maxEntries int
	eviction   Eviction
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

// WithMaxEntries bounds the cache to at most n entries, evicting per the
// configured [Eviction] policy once full. n must not be negative; zero leaves
// the cache unbounded, as does omitting this option.
func WithMaxEntries(n int) Option {
	return func(c *config) { c.maxEntries = n }
}

// WithEviction selects the policy used to choose a victim when the cache is
// full. It has no effect unless [WithMaxEntries] is also set.
func WithEviction(policy Eviction) Option {
	return func(c *config) { c.eviction = policy }
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
	eviction   Eviction
	metrics    MetricsRecorder
	logger     *slog.Logger

	mu        sync.Mutex
	byKey     map[string]*list.Element            // hash -> element holding *entry
	order     *list.List                          // front = most recently used, back = eviction victim
	stores    map[string]*vectorstore.MemoryStore // partitionKey -> semantic index; nil unless semantic is enabled
	partCount map[string]int                      // partitionKey -> live entry count; nil unless semantic is enabled

	stats Stats
}

// New returns a Cache configured by opts. By default the cache is
// exact-match-only and unbounded. It returns an error when semantic mode is
// requested without an embedder or with an out-of-range threshold, or when an
// invalid max-entries or eviction policy is supplied.
func New(opts ...Option) (*Cache, error) {
	cfg := config{eviction: EvictLRU}
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
	if cfg.eviction != EvictLRU && cfg.eviction != EvictFIFO {
		return nil, fmt.Errorf("configuring cache: %w: %d", ErrInvalidEviction, cfg.eviction)
	}

	c := &Cache{
		embedder:   cfg.embedder,
		threshold:  cfg.threshold,
		semantic:   cfg.semantic,
		maxEntries: cfg.maxEntries,
		eviction:   cfg.eviction,
		metrics:    cfg.metrics,
		logger:     cfg.logger,
		byKey:      make(map[string]*list.Element),
		order:      list.New(),
	}
	if cfg.semantic {
		c.stores = make(map[string]*vectorstore.MemoryStore)
		c.partCount = make(map[string]int)
	}
	return c, nil
}

// Get returns a cached response for key. It first tries the exact-match fast
// path, then, when semantic mode is enabled, the embedding nearest-neighbor
// fallback. The boolean reports whether a usable response was found; a miss is
// not an error. An error is returned only for an invalid key or an embedding
// failure on the semantic path.
func (c *Cache) Get(ctx context.Context, key Key) (response string, hit bool, err error) {
	hash, err := hashKey(key)
	if err != nil {
		return "", false, fmt.Errorf("cache get: %w", err)
	}

	if response, ok := c.getExact(hash); ok {
		c.record(true, MetricExactHits)
		return response, true, nil
	}

	if !c.semantic {
		c.record(false, "")
		return "", false, nil
	}

	response, ok, err := c.getSemantic(ctx, key)
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

// getExact returns the response stored under hash, promoting it on the LRU
// ordering when found.
func (c *Cache) getExact(hash string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[hash]
	if !ok {
		return "", false
	}
	if c.eviction == EvictLRU {
		c.order.MoveToFront(el)
	}
	return entryOf(el).response, true
}

// getSemantic embeds key.Prompt and returns the nearest stored response within
// the key's model+params partition whose cosine similarity meets the threshold.
// Partitioning by model and params keeps semantic matches from crossing model
// or parameter boundaries, so a match never returns another model's response.
// It requests several neighbors so a stale vector whose entry was already
// evicted is skipped in favor of the next live neighbor above the threshold.
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

	neighbors, err := store.Nearest(ctx, vector, semanticNeighborFanout)
	if err != nil {
		if errors.Is(err, vectorstore.ErrDimensionMismatch) {
			// A different embedding dimension than the indexed vectors means no
			// comparable neighbor exists, so treat it as a miss. Every other
			// error, including context cancellation, propagates.
			return "", false, nil
		}
		return "", false, fmt.Errorf("querying semantic index: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range neighbors {
		n := neighbors[i]
		if n.Score < c.threshold {
			// Neighbors are sorted by descending score, so once one falls below
			// the threshold no later neighbor can qualify.
			break
		}
		el, ok := c.byKey[n.Item.ID]
		if !ok {
			// A stale vector whose entry was already evicted: skip it and keep
			// looking for a live neighbor rather than reporting a false miss.
			continue
		}
		if c.eviction == EvictLRU {
			c.order.MoveToFront(el)
		}
		if c.logger != nil {
			c.logger.Debug("semcache semantic hit",
				slog.String("model", key.Model),
				slog.String("match_hash", n.Item.ID),
				slog.Float64("score", n.Score))
		}
		return entryOf(el).response, true, nil
	}
	return "", false, nil
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
// past the configured maximum evicts entries per the eviction policy.
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
		// hold c.mu. The index add and the map/list commit then happen together
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

	evictions, err := c.commit(ctx, hash, partition, item, response)
	if err != nil {
		return fmt.Errorf("cache put: %w", err)
	}
	// Report evictions to the external recorder only after releasing the lock,
	// so a blocking or re-entrant recorder cannot deadlock the cache.
	c.recordEvictions(evictions)
	return nil
}

// commit applies one Put atomically: it indexes the semantic vector (when
// enabled) and updates the key map and ordering under a single c.mu hold, then
// evicts down to the size bound. It returns the number of entries evicted so
// the caller can report them outside the lock. The vectorstore methods it calls
// while holding c.mu take only their own internal locks and never call back
// into the cache, so the fixed c.mu-then-store lock order rules out inversion.
func (c *Cache) commit(ctx context.Context, hash, partition string, item vectorstore.Item, response string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.byKey[hash]; ok {
		// Same key: the hash pins the prompt, so the indexed vector is already
		// identical and only the response needs replacing.
		entryOf(el).response = response
		if c.eviction == EvictLRU {
			c.order.MoveToFront(el)
		}
		return 0, nil
	}

	if c.semantic {
		store := c.stores[partition]
		created := store == nil
		if created {
			store = vectorstore.NewMemoryStore()
		}
		if err := store.Add(ctx, item); err != nil {
			return 0, fmt.Errorf("indexing embedding: %w", err)
		}
		if created {
			c.stores[partition] = store
		}
		c.partCount[partition]++
	}

	c.byKey[hash] = c.order.PushFront(&entry{hash: hash, partition: partition, response: response})
	return c.evictLocked(ctx), nil
}

// evictLocked removes entries from the back of the ordering until the cache is
// within its size bound, returning the number evicted. The caller must hold
// c.mu and is responsible for reporting the count to the external recorder
// after releasing the lock.
func (c *Cache) evictLocked(ctx context.Context) int {
	if c.maxEntries <= 0 {
		return 0
	}
	evicted := 0
	for c.order.Len() > c.maxEntries {
		victim := c.order.Back()
		if victim == nil {
			break
		}
		ent := entryOf(victim)
		c.order.Remove(victim)
		delete(c.byKey, ent.hash)
		if c.semantic {
			// Detach from the caller's context so a cancelled Get or Put cannot
			// abort the internal delete and leave a stale vector that could
			// otherwise rank ahead of a live neighbor.
			delCtx := context.WithoutCancel(ctx)
			if store := c.stores[ent.partition]; store != nil {
				if _, err := store.Delete(delCtx, ent.hash); err != nil && c.logger != nil {
					c.logger.Debug("semcache evict: semantic delete failed",
						slog.String("hash", ent.hash), slog.Any("error", err))
				}
			}
			c.partCount[ent.partition]--
			if c.partCount[ent.partition] <= 0 {
				// The partition is empty: drop its store and counter so a bounded
				// cache does not accumulate one empty store per distinct key.
				delete(c.partCount, ent.partition)
				delete(c.stores, ent.partition)
			}
		}
		c.stats.Evictions++
		evicted++
	}
	return evicted
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
	return c.order.Len()
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
