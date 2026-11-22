package cost

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"path"

	"gopkg.in/yaml.v3"
)

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
		model, u, err := ParseUsage([]byte(in.Response.Body))
		if errors.Is(err, ErrNoUsage) {
			continue
		}
		if err != nil {
			return fmt.Errorf("parsing usage in %q: %w", name, err)
		}
		if _, err := t.Record(name, model, u); err != nil && !errors.Is(err, ErrUnknownModel) {
			return fmt.Errorf("pricing %q: %w", name, err)
		}
	}
	return nil
}

// percent is the denominator of a percentage.
const percent = 100

// Compare returns ErrCostRegression when current costs more than baseline by
// over maxIncreasePercent percent. A zero baseline allows no cost at all
// unless maxIncreasePercent is negative, which disables the check.
func Compare(baseline, current Report, maxIncreasePercent int) error {
	if maxIncreasePercent < 0 {
		return nil
	}
	// cur > base*(100+p)/100, multiplied out in big integers so neither
	// side can overflow.
	limit := new(big.Int).Mul(
		big.NewInt(int64(baseline.Total.Cost)),
		new(big.Int).Add(big.NewInt(percent), big.NewInt(int64(maxIncreasePercent))),
	)
	scaled := new(big.Int).Mul(big.NewInt(int64(current.Total.Cost)), big.NewInt(percent))
	if scaled.Cmp(limit) > 0 {
		return fmt.Errorf("%w: %s to %s, over %d%%", ErrCostRegression, baseline.Total.Cost, current.Total.Cost, maxIncreasePercent)
	}
	return nil
}
