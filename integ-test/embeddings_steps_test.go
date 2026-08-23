package integtest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
	"github.com/vmontagh/go-llm-pipeline/pkg/vectorstore"
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
)

// Sentinel errors returned by step definitions. Wrapping a sentinel keeps the
// dynamic assertion messages while giving callers a stable category to match.
var (
	errEmptyTable     = errors.New("empty data table")
	errBadTableHeader = errors.New("data table needs id and text columns")
	errEmptyModel     = errors.New("empty model")
	errBatchCount     = errors.New("unexpected embedding count")
	errUnknownID      = errors.New("unknown id")
	errSelfSimilarity = errors.New("self-similarity out of tolerance")
	errNotMoreSimilar = errors.New("related text not more similar than unrelated")
)

// embedWorld holds per-scenario state. The same instance is reused across
// scenarios via the initializer, so reset clears it in the Before hook.
type embedWorld struct {
	t        *testing.T
	cassette string
	embedder *ollama.Embedder
	store    *vectorstore.MemoryStore
	vectors  map[string]vectorstore.Vector
}

// initializeScenario registers the Before hook and step definitions. It takes
// the outer *testing.T so steps can build cassette-backed HTTP clients with
// pkg/vcrtest, which is testing.T-aware.
func initializeScenario(t *testing.T, sc *godog.ScenarioContext) {
	w := &embedWorld{t: t}

	sc.Before(func(ctx context.Context, scenario *godog.Scenario) (context.Context, error) {
		w.reset(scenario)
		return ctx, nil
	})

	sc.Step(`^an Ollama embedder for model "([^"]*)"$`, w.anOllamaEmbedderForModel)
	sc.Step(`^I embed the following texts:$`, w.iEmbedTheFollowingTexts)
	sc.Step(`^the self-similarity of "([^"]*)" is approximately ([0-9.]+)$`, w.theSelfSimilarityIsApproximately)
	sc.Step(`^"([^"]*)" is more similar to "([^"]*)" than to "([^"]*)"$`, w.isMoreSimilarToThanTo)
}

// reset clears per-scenario state and derives the cassette name from the
// scenario title so each scenario replays or records its own cassette.
func (w *embedWorld) reset(scenario *godog.Scenario) {
	w.cassette = cassetteName(scenario.Name)
	w.embedder = nil
	w.store = vectorstore.NewMemoryStore()
	w.vectors = make(map[string]vectorstore.Vector)
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

func (w *embedWorld) anOllamaEmbedderForModel(model string) error {
	if model == "" {
		return errEmptyModel
	}
	host := placeholderHost
	if !isFakeMode {
		host = liveHost()
	}
	w.embedder = ollama.NewEmbedder(host, model,
		ollama.WithHTTPClient(vcrtest.Client(w.t, w.cassette)),
		ollama.WithHTTPClientTimeout(embedTimeout),
	)
	return nil
}

func (w *embedWorld) iEmbedTheFollowingTexts(table *godog.Table) error {
	ids, texts, err := parseTextsTable(table)
	if err != nil {
		return err
	}
	vectors, err := w.embedder.EmbedBatch(w.t.Context(), texts)
	if err != nil {
		return fmt.Errorf("embedding batch: %w", err)
	}
	if len(vectors) != len(ids) {
		return fmt.Errorf("%w: got %d vectors, want %d", errBatchCount, len(vectors), len(ids))
	}
	for i, id := range ids {
		vec := vectorstore.Vector(vectors[i])
		if err := w.store.Add(w.t.Context(), vectorstore.Item{ID: id, Vector: vec}); err != nil {
			return fmt.Errorf("indexing %q: %w", id, err)
		}
		w.vectors[id] = vec
	}
	return nil
}

func (w *embedWorld) theSelfSimilarityIsApproximately(id, wantStr string) error {
	want, err := strconv.ParseFloat(wantStr, 64)
	if err != nil {
		return fmt.Errorf("parsing want similarity %q: %w", wantStr, err)
	}
	scores, err := w.scoresFor(id)
	if err != nil {
		return err
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

func (w *embedWorld) isMoreSimilarToThanTo(base, related, unrelated string) error {
	scores, err := w.scoresFor(base)
	if err != nil {
		return err
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
func (w *embedWorld) scoresFor(id string) (map[string]float64, error) {
	query, ok := w.vectors[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", errUnknownID, id)
	}
	neighbors, err := w.store.Nearest(w.t.Context(), query, w.store.Len())
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
	if table == nil || len(table.Rows) < 2 {
		return nil, nil, errEmptyTable
	}
	idCol, textCol := -1, -1
	for i, cell := range table.Rows[0].Cells {
		switch cell.Value {
		case "id":
			idCol = i
		case "text":
			textCol = i
		}
	}
	if idCol < 0 || textCol < 0 {
		return nil, nil, errBadTableHeader
	}
	rows := table.Rows[1:]
	ids = make([]string, 0, len(rows))
	texts = make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Cells[idCol].Value)
		texts = append(texts, row.Cells[textCol].Value)
	}
	return ids, texts, nil
}
