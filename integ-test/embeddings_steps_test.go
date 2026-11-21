package integtest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	messages "github.com/cucumber/messages/go/v34"
	gocmp "github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore/sqlitestore"
)

const (
	// placeholderHost is an unreachable host used on the fake lane; cassette
	// replay tolerates a changed host, so requests never leave the process.
	placeholderHost = "http://ollama.invalid"
	// defaultLiveHost is the Ollama endpoint used by the live lane when
	// OLLAMA_HOST is unset.
	defaultLiveHost = "http://localhost:11434"
	// liveHostEnv overrides the live Ollama endpoint, matching Ollama's own
	// convention. It is read only by the live lane, never by the library.
	liveHostEnv = "OLLAMA_HOST"
	// embedTimeout bounds a single embedding round-trip on the live lane.
	embedTimeout = 30 * time.Second
	// selfSimTolerance is the absolute float tolerance for a self-similarity
	// that should equal 1.
	selfSimTolerance = 1e-6
	// textMetaKey is the Metadata key carrying each item's source text, so
	// results can be checked for the right payload, not only the right ID.
	textMetaKey = "text"
)

// Sentinel errors returned by step definitions. Wrapping a sentinel keeps the
// dynamic assertion messages while giving callers a stable category to match.
var (
	errEmptyTable     = errors.New("empty data table")
	errBadTableHeader = errors.New("data table needs id and text columns")
	errShortTableRow  = errors.New("data table row has too few cells")
	errEmptyModel     = errors.New("empty model")
	errBatchCount     = errors.New("unexpected embedding count")
	errUnknownID      = errors.New("unknown id")
	errSelfSimilarity = errors.New("self-similarity out of tolerance")
	errNotMoreSimilar = errors.New("related text not more similar than unrelated")
	errBadThreshold   = errors.New("threshold is not a number")
	errResultMismatch = errors.New("result differs from expectation")
	errInputMutated   = errors.New("call under test mutated its input")
)

// embedWorld holds per-scenario state. The same instance is reused across
// scenarios via the initializer, so reset clears it in the Before hook.
type embedWorld struct {
	cassette string
	embedder *ollama.Embedder
	store    *vectorstore.MemoryStore
	vectors  map[string]vectorstore.Vector
	// items holds the embedded items in table order, as passed to the
	// functions under test.
	items []vectorstore.Item
	// expected holds deep copies of items keyed by ID, so expectations share
	// no memory with anything a call under test could mutate.
	expected map[string]vectorstore.Item
	// original holds the same deep copies in table order, to detect a call
	// that reorders or replaces input items.
	original []vectorstore.Item
	// sqlite is the reopened persistent store, closed in the After hook.
	sqlite *sqlitestore.Store
}

// initializeScenario registers the Before hook and step definitions. Steps get
// the scenario context from godog; the few that need the outer *testing.T (for
// pkg/vcrtest cassettes and temp dirs) receive it as a parameter.
func initializeScenario(t *testing.T, sc *godog.ScenarioContext) {
	w := &embedWorld{}

	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		w.reset(scenario)
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
		return ctx, errors.Join(err, w.closeSQLite())
	})

	sc.Step(`^an Ollama embedder for model "([^"]*)"$`, func(model string) error {
		return w.anOllamaEmbedderForModel(t, model)
	})
	sc.Step(`^I embed the following texts:$`, w.iEmbedTheFollowingTexts)
	sc.Step(`^the self-similarity of "([^"]*)" is approximately ([0-9.]+)$`, w.theSelfSimilarityIsApproximately)
	sc.Step(`^"([^"]*)" is more similar to "([^"]*)" than to "([^"]*)"$`, w.isMoreSimilarToThanTo)
	sc.Step(`^finding duplicates at threshold ([0-9.]+) reports exactly these pairs:$`, w.findingDuplicatesReports)
	sc.Step(`^clustering at threshold ([0-9.]+) yields exactly these groups:$`, w.clusteringYieldsGroups)
	sc.Step(`^I save the embeddings to a SQLite store and reopen it$`, func(ctx context.Context) error {
		return w.iSaveToSQLiteAndReopen(ctx, t.TempDir())
	})
	sc.Step(`^the reopened store ranks neighbors of "([^"]*)" exactly like the memory store$`, w.reopenedStoreRanksLikeMemory)
}

// reset clears per-scenario state and derives the cassette name from the
// scenario title so each scenario replays or records its own cassette.
func (w *embedWorld) reset(scenario *godog.Scenario) {
	w.cassette = cassetteName(scenario.Name)
	w.embedder = nil
	w.store = vectorstore.NewMemoryStore()
	w.vectors = make(map[string]vectorstore.Vector)
	w.items = nil
	w.expected = make(map[string]vectorstore.Item)
	w.original = nil
	w.sqlite = nil
}

// liveHost returns the Ollama endpoint for the live lane.
func liveHost() string {
	return cmp.Or(os.Getenv(liveHostEnv), defaultLiveHost)
}

// cassetteName converts a scenario title into a stable cassette filename by
// lowercasing and collapsing every run of non-alphanumeric characters into a
// single underscore.
func cassetteName(scenario string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(scenario) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			underscore = false
			continue
		}
		if !underscore {
			b.WriteByte('_')
			underscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func (w *embedWorld) anOllamaEmbedderForModel(t *testing.T, model string) error {
	if model == "" {
		return errEmptyModel
	}
	host := placeholderHost
	if !isFakeMode {
		host = liveHost()
	}
	w.embedder = ollama.NewEmbedder(host, model,
		ollama.WithHTTPClient(vcrtest.Client(t, w.cassette)),
		ollama.WithHTTPClientTimeout(embedTimeout),
	)
	return nil
}

func (w *embedWorld) iEmbedTheFollowingTexts(ctx context.Context, table *godog.Table) error {
	ids, texts, err := parseTextsTable(table)
	if err != nil {
		return fmt.Errorf("parsing texts table: %w", err)
	}
	vectors, err := w.embedder.EmbedBatch(ctx, texts)
	if err != nil {
		return fmt.Errorf("embedding batch: %w", err)
	}
	if len(vectors) != len(ids) {
		return fmt.Errorf("%w: got %d vectors, want %d", errBatchCount, len(vectors), len(ids))
	}
	for i, id := range ids {
		item := vectorstore.Item{
			ID:       id,
			Vector:   vectorstore.Vector(vectors[i]),
			Metadata: map[string]string{textMetaKey: texts[i]},
		}
		if err := w.store.Add(ctx, item); err != nil {
			return fmt.Errorf("indexing %q: %w", id, err)
		}
		w.vectors[id] = item.Vector
		w.items = append(w.items, item)
		snapshot := vectorstore.Item{
			ID:       id,
			Vector:   slices.Clone(item.Vector),
			Metadata: maps.Clone(item.Metadata),
		}
		w.expected[id] = snapshot
		w.original = append(w.original, snapshot)
	}
	return nil
}

func (w *embedWorld) theSelfSimilarityIsApproximately(ctx context.Context, id, wantStr string) error {
	want, err := strconv.ParseFloat(wantStr, 64)
	if err != nil {
		return fmt.Errorf("parsing want similarity %q: %w", wantStr, err)
	}
	scores, err := w.scoresFor(ctx, id)
	if err != nil {
		return fmt.Errorf("scoring against %q: %w", id, err)
	}
	got, ok := scores[id]
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownID, id)
	}
	if math.Abs(got-want) > selfSimTolerance {
		return fmt.Errorf("%w: self-similarity of %q = %v, want %v (tolerance %v)",
			errSelfSimilarity, id, got, want, selfSimTolerance)
	}
	return nil
}

func (w *embedWorld) isMoreSimilarToThanTo(ctx context.Context, base, related, unrelated string) error {
	scores, err := w.scoresFor(ctx, base)
	if err != nil {
		return fmt.Errorf("scoring against %q: %w", base, err)
	}
	relatedScore, ok := scores[related]
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownID, related)
	}
	unrelatedScore, ok := scores[unrelated]
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownID, unrelated)
	}
	if relatedScore <= unrelatedScore {
		return fmt.Errorf("%w: similarity(%q,%q)=%v not greater than similarity(%q,%q)=%v",
			errNotMoreSimilar, base, related, relatedScore, base, unrelated, unrelatedScore)
	}
	return nil
}

// scoresFor returns the cosine similarity of id's vector against every indexed
// item, keyed by item ID, by querying the vectorstore.
func (w *embedWorld) scoresFor(ctx context.Context, id string) (map[string]float64, error) {
	query, ok := w.vectors[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownID, id)
	}
	neighbors, err := w.store.Nearest(ctx, query, w.store.Len())
	if err != nil {
		return nil, fmt.Errorf("querying nearest for %q: %w", id, err)
	}
	scores := make(map[string]float64, len(neighbors))
	for _, n := range neighbors {
		scores[n.Item.ID] = n.Score
	}
	return scores, nil
}

// parseTextsTable extracts the id and text columns from a Gherkin data table,
// resolving them by header name so column order is not significant.
func parseTextsTable(table *godog.Table) (ids, texts []string, err error) {
	rows, err := parseColumns(table, "id", "text")
	if err != nil {
		return nil, nil, fmt.Errorf("reading id and text columns: %w", err)
	}
	ids = make([]string, 0, len(rows))
	texts = make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row[0])
		texts = append(texts, row[1])
	}
	return ids, texts, nil
}

// sqliteStoreFile is the database file name inside the scenario's temp dir.
const sqliteStoreFile = "embeddings.db"

// scoreTolerance absorbs float rounding between two computations of the same
// cosine similarity over identical vectors.
const scoreTolerance = 1e-12

// scoreField is the cmp path step for Duplicate.Score and Neighbor.Score.
const scoreField = ".Score"

// approxScores compares Score fields within scoreTolerance and every other
// field, vectors included, exactly.
func approxScores() gocmp.Option {
	return gocmp.FilterPath(func(p gocmp.Path) bool { return p.Last().String() == scoreField },
		cmpopts.EquateApprox(0, scoreTolerance))
}

// findingDuplicatesReports runs FindDuplicates over every embedded item and
// requires exactly the tabled (a, b) pairs, each carrying both items' full
// payload and their cosine similarity computed independently from the
// embedding-time copies.
func (w *embedWorld) findingDuplicatesReports(ctx context.Context, thresholdText string, table *godog.Table) error {
	threshold, err := parseThreshold(thresholdText)
	if err != nil {
		return fmt.Errorf("reading duplicate threshold: %w", err)
	}
	rows, err := parseColumns(table, "a", "b")
	if err != nil {
		return fmt.Errorf("reading duplicate pairs: %w", err)
	}
	want := make([]vectorstore.Duplicate, 0, len(rows))
	for _, row := range rows {
		dup, err := w.expectedDuplicate(row[0], row[1])
		if err != nil {
			return fmt.Errorf("building expected pair (%q, %q): %w", row[0], row[1], err)
		}
		want = append(want, dup)
	}

	got, err := vectorstore.FindDuplicates(ctx, w.items, threshold)
	if err != nil {
		return fmt.Errorf("finding duplicates: %w", err)
	}

	if diff := gocmp.Diff(want, got, approxScores()); diff != "" {
		return fmt.Errorf("%w: FindDuplicates(threshold=%v) (-want +got):\n%s", errResultMismatch, threshold, diff)
	}
	if err := w.checkInputUnchanged(); err != nil {
		return fmt.Errorf("after FindDuplicates: %w", err)
	}
	return nil
}

// clusteringYieldsGroups runs ClusterItems over every embedded item and
// requires exactly the tabled groups, in the documented order, with every
// member's full payload.
func (w *embedWorld) clusteringYieldsGroups(thresholdText string, table *godog.Table) error {
	threshold, err := parseThreshold(thresholdText)
	if err != nil {
		return fmt.Errorf("reading cluster threshold: %w", err)
	}
	rows, err := parseColumns(table, "members")
	if err != nil {
		return fmt.Errorf("reading cluster members: %w", err)
	}
	want := make([]vectorstore.Cluster, 0, len(rows))
	for _, row := range rows {
		var members []vectorstore.Item
		for id := range strings.SplitSeq(row[0], ",") {
			item, ok := w.expected[strings.TrimSpace(id)]
			if !ok {
				return fmt.Errorf("%w: %q", errUnknownID, id)
			}
			members = append(members, item)
		}
		want = append(want, vectorstore.Cluster{Items: members})
	}

	got, err := vectorstore.ClusterItems(w.items, threshold)
	if err != nil {
		return fmt.Errorf("clustering: %w", err)
	}

	if diff := gocmp.Diff(want, got); diff != "" {
		return fmt.Errorf("%w: ClusterItems(threshold=%v) (-want +got):\n%s", errResultMismatch, threshold, diff)
	}
	if err := w.checkInputUnchanged(); err != nil {
		return fmt.Errorf("after ClusterItems: %w", err)
	}
	return nil
}

// iSaveToSQLiteAndReopen persists every embedded item to a fresh SQLite
// store in dir, closes it, and reopens it so later steps read only from disk.
func (w *embedWorld) iSaveToSQLiteAndReopen(ctx context.Context, dir string) error {
	path := filepath.Join(dir, sqliteStoreFile)
	store, err := sqlitestore.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("opening sqlite store: %w", err)
	}
	for _, item := range w.items {
		if err := store.Add(ctx, item); err != nil {
			return errors.Join(fmt.Errorf("adding %q: %w", item.ID, err), store.Close())
		}
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("closing sqlite store: %w", err)
	}
	w.sqlite, err = sqlitestore.Open(ctx, path)
	if err != nil {
		return fmt.Errorf("reopening sqlite store: %w", err)
	}
	return nil
}

// reopenedStoreRanksLikeMemory queries both stores with id's vector over all
// items and requires the same neighbors, payloads, and scores.
func (w *embedWorld) reopenedStoreRanksLikeMemory(ctx context.Context, id string) error {
	query, ok := w.vectors[id]
	if !ok {
		return fmt.Errorf("%w: %q", errUnknownID, id)
	}
	limit := len(w.items)
	want, err := w.store.Nearest(ctx, query, limit)
	if err != nil {
		return fmt.Errorf("memory Nearest(%q): %w", id, err)
	}
	got, err := w.sqlite.Nearest(ctx, query, limit)
	if err != nil {
		return fmt.Errorf("sqlite Nearest(%q): %w", id, err)
	}
	if diff := gocmp.Diff(want, got, approxScores()); diff != "" {
		return fmt.Errorf("%w: Nearest(%q) memory vs sqlite (-want +got):\n%s", errResultMismatch, id, diff)
	}
	return nil
}

// closeSQLite closes the reopened store, if a scenario opened one.
func (w *embedWorld) closeSQLite() error {
	if w.sqlite == nil {
		return nil
	}
	err := w.sqlite.Close()
	w.sqlite = nil
	if err != nil {
		return fmt.Errorf("closing reopened sqlite store: %w", err)
	}
	return nil
}

// checkInputUnchanged requires the items passed to the call under test to
// still equal, in order, the deep copies taken at embedding time.
func (w *embedWorld) checkInputUnchanged() error {
	if diff := gocmp.Diff(w.original, w.items); diff != "" {
		return fmt.Errorf("%w (-want +got):\n%s", errInputMutated, diff)
	}
	return nil
}

// expectedDuplicate builds the Duplicate expected for a and b from the
// embedding-time copies: A has the smaller ID, matching the Duplicate
// contract, and Score is their cosine similarity.
func (w *embedWorld) expectedDuplicate(a, b string) (vectorstore.Duplicate, error) {
	low, okA := w.expected[a]
	high, okB := w.expected[b]
	if !okA || !okB {
		return vectorstore.Duplicate{}, fmt.Errorf("%w: pair (%q, %q)", errUnknownID, a, b)
	}
	if high.ID < low.ID {
		low, high = high, low
	}
	score, err := vectorstore.Cosine(low.Vector, high.Vector)
	if err != nil {
		return vectorstore.Duplicate{}, fmt.Errorf("cosine of (%q, %q): %w", low.ID, high.ID, err)
	}
	return vectorstore.Duplicate{A: low, B: high, Score: score}, nil
}

// parseThreshold parses a threshold captured by a step pattern.
func parseThreshold(text string) (float64, error) {
	v, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %w", errBadThreshold, text, err)
	}
	return v, nil
}

// parseColumns returns, for each data row, the cells of the named columns in
// the order given. The header row may list the columns in any order.
func parseColumns(table *godog.Table, names ...string) ([][]string, error) {
	if table == nil || len(table.Rows) < 2 {
		return nil, errEmptyTable
	}
	index := make([]int, len(names))
	for i, name := range names {
		index[i] = slices.IndexFunc(table.Rows[0].Cells, func(c *messages.PickleTableCell) bool { return c.Value == name })
		if index[i] < 0 {
			return nil, fmt.Errorf("%w: missing column %q", errBadTableHeader, name)
		}
	}
	minCells := slices.Max(index) + 1
	rows := make([][]string, 0, len(table.Rows)-1)
	for i, row := range table.Rows[1:] {
		if len(row.Cells) < minCells {
			return nil, fmt.Errorf("%w: row %d has %d cells, need at least %d",
				errShortTableRow, i+1, len(row.Cells), minCells)
		}
		cells := make([]string, len(index))
		for j, col := range index {
			cells[j] = row.Cells[col].Value
		}
		rows = append(rows, cells)
	}
	return rows, nil
}
