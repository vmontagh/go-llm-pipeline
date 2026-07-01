// Package scrub provides text scrubbing utilities for redacting sensitive patterns from strings.
// A Scrubber holds an ordered list of regexp-based Rules and applies them in sequence, making it
// suitable for removing PII from debug log messages, VCR cassette bodies, or any text before it
// is persisted or emitted.
package scrub

import "regexp"

// Rule replaces all matches of a compiled regular expression with a fixed replacement string.
type Rule struct {
	pattern     *regexp.Regexp
	replacement string
}

// RegexpRule compiles pattern and returns a Rule that replaces all matches with replacement.
func RegexpRule(pattern, replacement string) (Rule, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return Rule{}, err
	}
	return Rule{pattern: re, replacement: replacement}, nil
}

// MustRegexpRule is like RegexpRule but panics if pattern fails to compile.
// Safe to use at package-level var initialisation.
func MustRegexpRule(pattern, replacement string) Rule {
	r, err := RegexpRule(pattern, replacement)
	if err != nil {
		panic("scrub.MustRegexpRule: " + err.Error())
	}
	return r
}

// Scrubber applies an ordered list of Rules to text, replacing each matched pattern in turn.
// A zero-value Scrubber is valid and passes text through unchanged.
type Scrubber struct {
	rules []Rule
}

// NewScrubber returns a Scrubber that applies rules in order.
func NewScrubber(rules ...Rule) *Scrubber {
	return &Scrubber{rules: rules}
}

// Scrub applies all rules to text and returns the scrubbed result.
func (s *Scrubber) Scrub(text string) string {
	for _, r := range s.rules {
		text = r.pattern.ReplaceAllString(text, r.replacement)
	}
	return text
}
