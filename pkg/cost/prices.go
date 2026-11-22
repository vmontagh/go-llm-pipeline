// Package cost counts LLM tokens and turns them into cost estimates.
//
// Usage comes from provider responses when they report it ([ParseUsage]
// understands OpenAI, Gemini, and Ollama bodies) and from a [Counter]
// otherwise. A caller-supplied [PriceTable] converts usage to an exact
// [Amount]; the package ships no prices because vendor prices change. A
// [Tracker] aggregates usage and cost per asset and per model for a batch, and
// [ReportFromCassettes] computes what recorded cassettes would cost so CI can
// catch cost regressions offline with [Compare].
package cost

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sentinel errors returned by this package, wrapped with %w.
var (
	// ErrInvalidPrices is returned when a price table is malformed.
	ErrInvalidPrices = errors.New("invalid price table")
	// ErrUnknownModel is returned when a model has no price.
	ErrUnknownModel = errors.New("unknown model")
	// ErrUnpriced is returned by Report.CheckPriced when calls had no price.
	ErrUnpriced = errors.New("unpriced calls")
	// ErrInvalidUsage is returned for negative token counts.
	ErrInvalidUsage = errors.New("invalid usage")
	// ErrOverflow is returned when a cost does not fit in an Amount.
	ErrOverflow = errors.New("cost overflow")
	// ErrCostRegression is returned by Compare when cost grew past the
	// allowed increase.
	ErrCostRegression = errors.New("cost regression")
)

const (
	// picosPerMicro converts micro-dollars to picodollars.
	picosPerMicro = 1_000_000
	// microsPerUSD converts dollars to micro-dollars.
	microsPerUSD = 1_000_000
	// rateDecimals is the precision of a rate in USD per million tokens.
	rateDecimals = 6
	// prefixWildcard ends a price-table key that matches by prefix.
	prefixWildcard = "*"
)

// Amount is an exact amount of US dollars in picodollars (1e-12 USD), so
// summing many small calls never loses precision.
type Amount int64

// String formats a as dollars rounded to six decimal places, for example
// "$0.001250".
func (a Amount) String() string {
	sign := ""
	if a < 0 {
		sign, a = "-", -a
	}
	micros := (int64(a) + picosPerMicro/2) / picosPerMicro
	return fmt.Sprintf("%s$%d.%06d", sign, micros/microsPerUSD, micros%microsPerUSD)
}

// Rate is the price of a model in micro-dollars per million tokens, so
// "2.50" USD per million input tokens is an Input of 2_500_000.
type Rate struct {
	Input  int64
	Output int64
}

// PriceTable maps model names to rates. A key ending in "*" matches every
// model with that prefix; an exact key wins, then the longest prefix.
type PriceTable struct {
	exact    map[string]Rate
	prefixes []string // longest first
	byPrefix map[string]Rate
}

// NewPriceTable builds a table from rates, rejecting empty model names,
// misplaced wildcards, and negative rates with ErrInvalidPrices.
func NewPriceTable(rates map[string]Rate) (*PriceTable, error) {
	t := &PriceTable{exact: make(map[string]Rate), byPrefix: make(map[string]Rate)}
	var errs []error
	for _, model := range slices.Sorted(maps.Keys(rates)) {
		r := rates[model]
		prefix, wildcard := strings.CutSuffix(model, prefixWildcard)
		switch {
		case prefix == "":
			errs = append(errs, fmt.Errorf("%w: model %q is empty", ErrInvalidPrices, model))
		case strings.Contains(prefix, prefixWildcard):
			errs = append(errs, fmt.Errorf("%w: model %q may only end in %q", ErrInvalidPrices, model, prefixWildcard))
		case r.Input < 0 || r.Output < 0:
			errs = append(errs, fmt.Errorf("%w: model %q has a negative rate", ErrInvalidPrices, model))
		case wildcard:
			t.byPrefix[prefix] = r
			t.prefixes = append(t.prefixes, prefix)
		default:
			t.exact[model] = r
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	slices.SortFunc(t.prefixes, func(a, b string) int { return len(b) - len(a) })
	return t, nil
}

// Rate returns the rate for model, or false if the table has none.
func (t *PriceTable) Rate(model string) (Rate, bool) {
	if r, ok := t.exact[model]; ok {
		return r, true
	}
	for _, p := range t.prefixes {
		if strings.HasPrefix(model, p) {
			return t.byPrefix[p], true
		}
	}
	return Rate{}, false
}

// Cost returns the cost of u on model, or ErrUnknownModel.
func (t *PriceTable) Cost(model string, u Usage) (Amount, error) {
	if u.Input < 0 || u.Output < 0 {
		return 0, fmt.Errorf("%w: negative tokens for %q", ErrInvalidUsage, model)
	}
	r, ok := t.Rate(model)
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownModel, model)
	}
	// micro-dollars per million tokens times tokens is picodollars.
	in, inOK := mulInt64(int64(u.Input), r.Input)
	out, outOK := mulInt64(int64(u.Output), r.Output)
	total, sumOK := addInt64(in, out)
	if !inOK || !outOK || !sumOK {
		return 0, fmt.Errorf("%w: %q", ErrOverflow, model)
	}
	return Amount(total), nil
}

// priceFile is the YAML form read by LoadPrices.
type priceFile struct {
	Models map[string]struct {
		Input  string `yaml:"input"`
		Output string `yaml:"output"`
	} `yaml:"models"`
}

// LoadPrices reads a YAML price table from name in fsys. Rates are USD per
// million tokens, with up to six decimal places:
//
//	models:
//	  gpt-4o-mini: {input: 0.15, output: 0.60}
//	  "gemini-2.5-flash*": {input: 0.30, output: 2.50}
//	  "llama*": {input: 0, output: 0}
//
// Unknown fields, multiple documents, and malformed rates return
// ErrInvalidPrices.
func LoadPrices(fsys fs.FS, name string) (*PriceTable, error) {
	if fsys == nil {
		return nil, fmt.Errorf("%w: reading %q: nil filesystem", ErrInvalidPrices, name)
	}
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %q: %w", ErrInvalidPrices, name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	var f priceFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: decoding %q: %w", ErrInvalidPrices, name, err)
	}
	var extra any
	if dec.Decode(&extra) == nil {
		return nil, fmt.Errorf("%w: %q must hold exactly one YAML document", ErrInvalidPrices, name)
	}
	rates := make(map[string]Rate, len(f.Models))
	var errs []error
	for model, m := range f.Models {
		in, inErr := parseRate(m.Input)
		out, outErr := parseRate(m.Output)
		if err := errors.Join(inErr, outErr); err != nil {
			errs = append(errs, fmt.Errorf("model %q: %w", model, err))
			continue
		}
		rates[model] = Rate{Input: in, Output: out}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("%w: %q: %w", ErrInvalidPrices, name, err)
	}
	t, err := NewPriceTable(rates)
	if err != nil {
		return nil, fmt.Errorf("loading %q: %w", name, err)
	}
	return t, nil
}

// parseRate parses a non-negative decimal USD amount with at most six
// decimal places into micro-dollars, exactly and without floating point.
func parseRate(s string) (int64, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" || len(frac) > rateDecimals {
		return 0, fmt.Errorf("%w: rate %q must be a decimal with at most %d places", ErrInvalidPrices, s, rateDecimals)
	}
	frac += strings.Repeat("0", rateDecimals-len(frac))
	w, err := strconv.ParseUint(whole, 10, 63)
	if err != nil {
		return 0, fmt.Errorf("%w: rate %q: %w", ErrInvalidPrices, s, err)
	}
	f, err := strconv.ParseUint(frac, 10, 63)
	if err != nil {
		return 0, fmt.Errorf("%w: rate %q has an invalid fraction", ErrInvalidPrices, s)
	}
	if w > (math.MaxInt64-f)/microsPerUSD {
		return 0, fmt.Errorf("%w: rate %q is too large", ErrInvalidPrices, s)
	}
	return int64(w)*microsPerUSD + int64(f), nil
}

// mulInt64 returns a*b for non-negative a and b, or false on overflow.
func mulInt64(a, b int64) (int64, bool) {
	if a != 0 && b > math.MaxInt64/a {
		return 0, false
	}
	return a * b, true
}

// addInt64 returns a+b for non-negative a and b, or false on overflow.
func addInt64(a, b int64) (int64, bool) {
	if a > math.MaxInt64-b {
		return 0, false
	}
	return a + b, true
}
