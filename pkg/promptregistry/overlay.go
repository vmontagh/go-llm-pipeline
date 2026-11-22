package promptregistry

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sentinel errors for rule overlays, wrapped with %w like the other package
// errors.
var (
	// ErrInvalidOverlay is returned by NewOverlay and LoadOverlay when an
	// overlay is malformed.
	ErrInvalidOverlay = errors.New("invalid overlay")
	// ErrUnusedRules is returned by Render when a rule of the overlay does not
	// appear verbatim in the rendered prompt, so a learned rule cannot be
	// silently dropped.
	ErrUnusedRules = errors.New("unused overlay rules")
	// ErrOverlayTooLarge is returned by Render when the overlay's rules exceed
	// the limit set with WithOverlayLimit. Rules are never truncated.
	ErrOverlayTooLarge = errors.New("overlay too large")
)

// idHashLen is the number of hex digits of the content hash shown in an ID.
const idHashLen = 12

var (
	// overlayLabel restricts overlay names and versions so a version ID such
	// as "judge/v3+rules@7#3f2a9c1b0d4e" parses unambiguously.
	overlayLabel = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// sectionName restricts section names to identifiers, so every section is
	// reachable as a template field such as {{.Rules.guidelines}}.
	sectionName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// OverlaySpec describes a rule overlay to build with NewOverlay. Its YAML form,
// read by LoadOverlay, is:
//
//	name: photo-rules
//	version: "7"
//	sections:
//	  guidelines:
//	    - Prefer the sharpest frame of a burst.
//	  rejection: []
type OverlaySpec struct {
	// Name identifies the overlay, for example "photo-rules".
	Name string `yaml:"name"`
	// Version identifies this revision of the overlay, for example "7".
	Version string `yaml:"version"`
	// Sections maps a section name to its rules in order. A section may be
	// empty, so a template can reference it before any rule is learned.
	Sections map[string][]string `yaml:"sections"`
}

// Overlay is a named, versioned set of rules rendered on top of a template
// without editing it. It is immutable and safe for concurrent use. Construct
// one with NewOverlay or LoadOverlay.
type Overlay struct {
	name     string
	version  string
	sections map[string][]string
	hash     string
	size     int
}

// NewOverlay validates spec and returns the overlay it describes. Name and
// version must be non-empty and use only letters, digits, ".", "_" and "-".
// Section names must be identifiers. Each rule must be non-blank and unique
// within its section. A violation returns ErrInvalidOverlay.
func NewOverlay(spec OverlaySpec) (*Overlay, error) {
	var errs []error
	if !overlayLabel.MatchString(spec.Name) {
		errs = append(errs, fmt.Errorf("%w: name %q must match %s", ErrInvalidOverlay, spec.Name, overlayLabel))
	}
	if !overlayLabel.MatchString(spec.Version) {
		errs = append(errs, fmt.Errorf("%w: version %q must match %s", ErrInvalidOverlay, spec.Version, overlayLabel))
	}
	sections := make(map[string][]string, len(spec.Sections))
	size := 0
	for _, name := range slices.Sorted(maps.Keys(spec.Sections)) {
		if !sectionName.MatchString(name) {
			errs = append(errs, fmt.Errorf("%w: section %q must match %s", ErrInvalidOverlay, name, sectionName))
		}
		rules := spec.Sections[name]
		seen := make(map[string]bool, len(rules))
		for i, rule := range rules {
			switch {
			case strings.TrimSpace(rule) == "":
				errs = append(errs, fmt.Errorf("%w: section %q rule %d is blank", ErrInvalidOverlay, name, i))
			case seen[rule]:
				errs = append(errs, fmt.Errorf("%w: section %q rule %d duplicates %q", ErrInvalidOverlay, name, i, rule))
			}
			seen[rule] = true
			size += len(rule)
		}
		sections[name] = slices.Clone(rules)
	}
	// Each joined error already wraps ErrInvalidOverlay.
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return &Overlay{
		name:     spec.Name,
		version:  spec.Version,
		sections: sections,
		hash:     contentHash(sections),
		size:     size,
	}, nil
}

// LoadOverlay reads the YAML overlay at name in fsys and validates it as
// NewOverlay does. Unknown fields and multiple YAML documents are rejected
// with ErrInvalidOverlay.
func LoadOverlay(fsys fs.FS, name string) (*Overlay, error) {
	if fsys == nil {
		return nil, fmt.Errorf("%w: reading %q: nil filesystem", ErrInvalidOverlay, name)
	}
	src, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %q: %w", ErrInvalidOverlay, name, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	var spec OverlaySpec
	if err := dec.Decode(&spec); err != nil {
		return nil, fmt.Errorf("%w: decoding %q: %w", ErrInvalidOverlay, name, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %q must hold exactly one YAML document", ErrInvalidOverlay, name)
	}
	o, err := NewOverlay(spec)
	if err != nil {
		return nil, fmt.Errorf("loading %q: %w", name, err)
	}
	return o, nil
}

// contentHash returns the hex SHA-256 of an unambiguous encoding of sections:
// section names in sorted order, each followed by its rules in order, every
// string and count length-prefixed.
func contentHash(sections map[string][]string) string {
	var buf []byte
	appendString := func(s string) {
		buf = binary.AppendUvarint(buf, uint64(len(s)))
		buf = append(buf, s...)
	}
	for _, name := range slices.Sorted(maps.Keys(sections)) {
		appendString(name)
		buf = binary.AppendUvarint(buf, uint64(len(sections[name])))
		for _, rule := range sections[name] {
			appendString(rule)
		}
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

// Name returns the overlay name.
func (o *Overlay) Name() string { return o.name }

// Version returns the overlay version.
func (o *Overlay) Version() string { return o.version }

// Hash returns the hex SHA-256 of the overlay's sections and rules. It changes
// whenever a rule changes, even if the version does not.
func (o *Overlay) Hash() string { return o.hash }

// Sections returns a copy of the overlay's sections.
func (o *Overlay) Sections() map[string][]string { return cloneSections(o.sections) }

// ID returns "<name>@<version>#<hash prefix>", for example
// "photo-rules@7#3f2a9c1b0d4e".
func (o *Overlay) ID() string {
	return o.name + "@" + o.version + "#" + o.hash[:idHashLen]
}

// refEscaper escapes "+" in template references, and "%" so the escaping is
// reversible, so a VersionID's first "+" always starts the overlay ID.
var refEscaper = strings.NewReplacer("%", "%25", "+", "%2B")

// VersionID returns a stable identifier for the prompt produced by rendering
// ref with overlay o: "judge/v3" without an overlay, or
// "judge/v3+photo-rules@7#3f2a9c1b0d4e" with one. Store it with each output to
// know exactly which template and rules produced it. "%" and "+" in ref are
// percent-encoded, so distinct template and overlay pairs never share an ID.
func VersionID(ref Ref, o *Overlay) string {
	id := refEscaper.Replace(ref.Name + "/" + ref.Version)
	if o == nil {
		return id
	}
	return id + "+" + o.ID()
}

func cloneSections(sections map[string][]string) map[string][]string {
	out := make(map[string][]string, len(sections))
	for name, rules := range sections {
		out[name] = slices.Clone(rules)
	}
	return out
}

// RenderOption configures a single Render call.
type RenderOption func(*renderConfig)

type renderConfig struct {
	overlay *Overlay
	limit   int
}

// WithOverlay renders the template with overlay o. The template then sees the
// caller's data as {{.Data}} and the overlay's sections as {{.Rules}}, for
// example {{range .Rules.guidelines}}- {{.}}{{end}}. A template written for
// an overlay therefore fails with ErrMissingField when rendered without one,
// and the other way round. A nil o renders without an overlay.
func WithOverlay(o *Overlay) RenderOption {
	return func(c *renderConfig) { c.overlay = o }
}

// WithOverlayLimit makes Render fail with ErrOverlayTooLarge when the
// overlay's rules total more than maxBytes bytes. A maxBytes of zero or less
// means no limit.
func WithOverlayLimit(maxBytes int) RenderOption {
	return func(c *renderConfig) { c.limit = maxBytes }
}

// overlayData is the data a template rendered with an overlay receives.
type overlayData struct {
	Data  any
	Rules map[string][]string
}

// checkLimit returns ErrOverlayTooLarge when the overlay's rules exceed the
// configured limit.
func (c *renderConfig) checkLimit() error {
	o := c.overlay
	if c.limit > 0 && o.size > c.limit {
		return fmt.Errorf("%w: %s has %d bytes of rules, limit %d", ErrOverlayTooLarge, o.ID(), o.size, c.limit)
	}
	return nil
}

// unrenderedSections returns, sorted, the sections with a rule that does not
// appear verbatim in out. Checking the output rather than the template source
// holds however the template reaches the rules: through dot, $, a variable,
// or a nested template. Each rule claims its own occurrence, longest first, so
// a rule present only inside a longer rule, or a repeated rule rendered once,
// is still reported.
func unrenderedSections(sections map[string][]string, out string) []string {
	type rule struct{ section, text string }
	var rules []rule
	for name, texts := range sections {
		for _, text := range texts {
			rules = append(rules, rule{section: name, text: text})
		}
	}
	slices.SortFunc(rules, func(a, b rule) int {
		return cmp.Or(cmp.Compare(len(b.text), len(a.text)), strings.Compare(a.text, b.text), strings.Compare(a.section, b.section))
	})
	var claimed []span
	missing := make(map[string]bool)
	for _, r := range rules {
		s, ok := claimOccurrence(out, r.text, claimed)
		if !ok {
			missing[r.section] = true
			continue
		}
		claimed = append(claimed, s)
	}
	return slices.Sorted(maps.Keys(missing))
}

// span is a half-open byte range [start, end) of the rendered output.
type span struct{ start, end int }

// claimOccurrence returns the first occurrence of text in out that overlaps
// no claimed span.
func claimOccurrence(out, text string, claimed []span) (span, bool) {
	for from := 0; from <= len(out); {
		i := strings.Index(out[from:], text)
		if i < 0 {
			return span{}, false
		}
		s := span{start: from + i, end: from + i + len(text)}
		overlaps := slices.ContainsFunc(claimed, func(c span) bool { return s.start < c.end && c.start < s.end })
		if !overlaps {
			return s, true
		}
		from = s.start + 1
	}
	return span{}, false
}
