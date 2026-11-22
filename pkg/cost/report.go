package cost

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"path"

	"gopkg.in/yaml.v3"
)

// maxAmount is the largest Amount.
const maxAmount = Amount(math.MaxInt64)

// cassetteFile is the part of a go-vcr cassette the report reads.
type cassetteFile struct {
	Interactions []struct {
		Response struct {
			Body string `yaml:"body"`
		} `yaml:"response"`
	} `yaml:"interactions"`
}

// ReportFromCassettes prices every recorded response in the .yaml and .yml
// cassettes under fsys, without contacting a provider. Responses that report
// no usage are skipped.
func ReportFromCassettes(fsys fs.FS, prices *PriceTable) (Report, error) {
	t := NewTracker(prices)
	err := fs.WalkDir(fsys, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (path.Ext(name) != ".yaml" && path.Ext(name) != ".yml") {
			return nil
		}
		return recordCassette(fsys, name, t)
	})
	if err != nil {
		return Report{}, fmt.Errorf("reading cassettes: %w", err)
	}
	return t.Report(), nil
}

func recordCassette(fsys fs.FS, name string, t *Tracker) error {
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return fmt.Errorf("reading %q: %w", name, err)
	}
	var c cassetteFile
	if err := yaml.NewDecoder(bytes.NewReader(src)).Decode(&c); err != nil {
		return fmt.Errorf("decoding %q: %w", name, err)
	}
	for _, in := range c.Interactions {
		model, u, ok := ParseUsage([]byte(in.Response.Body))
		if !ok {
			continue
		}
		if _, err := t.Record(name, model, u); err != nil && !errors.Is(err, ErrUnknownModel) {
			return fmt.Errorf("pricing %q: %w", name, err)
		}
	}
	return nil
}

// Compare returns ErrCostRegression when current costs more than baseline by
// over maxIncreasePercent percent. A zero baseline allows no cost at all
// unless maxIncreasePercent is negative, which disables the check.
func Compare(baseline, current Report, maxIncreasePercent int) error {
	if maxIncreasePercent < 0 {
		return nil
	}
	base, cur := int64(baseline.Total.Cost), int64(current.Total.Cost)
	// cur > base*(100+p)/100, multiplied out to stay in integers.
	limit, ok := mulInt64(base, int64(100+maxIncreasePercent))
	if !ok {
		// The allowed cost exceeds any Amount, so nothing can regress.
		return nil
	}
	if cur > math.MaxInt64/100 || cur*100 > limit {
		return fmt.Errorf("%w: %s to %s, over %d%%", ErrCostRegression, baseline.Total.Cost, current.Total.Cost, maxIncreasePercent)
	}
	return nil
}
