package analyzer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

// Default extraction parameters.
const defaultMaxAttempts = 3

// Extraction sentinels, matchable with errors.Is.
var (
	// ErrJSONMalformed is returned (and fed back to the model) when a response
	// contains no syntactically valid JSON object for the target type.
	ErrJSONMalformed = errors.New("analyzer: response is not valid JSON")
	// ErrDecode is returned when a response is valid, schema-conforming JSON that
	// nonetheless cannot be decoded into the target type T (for example an
	// unknown field under strict decoding).
	ErrDecode = errors.New("analyzer: response did not map to target type")
	// ErrMaxAttemptsExceeded is returned when the bounded retry budget is
	// exhausted without a valid, schema-conforming result.
	ErrMaxAttemptsExceeded = errors.New("analyzer: extraction exceeded max attempts")
	// ErrNilAnalyzer is returned when Extract is called with a nil TextAnalyzer.
	ErrNilAnalyzer = errors.New("analyzer: nil TextAnalyzer passed to Extract")
)

// PublishFn is the event callback shape shared by Analyze and ExtractRequest.
type PublishFn = func(eventType, itemID, message string, isError bool)

// TextAnalyzer is the raw-text provider surface Extract depends on. It is
// satisfied by *Template[C, string] (see NewTextTemplate); accepting the
// interface keeps Extract decoupled from provider transport internals.
type TextAnalyzer interface {
	Analyze(ctx context.Context, prompt string, imageData []byte, jobID string, publish PublishFn) (string, error)
}

// ExtractRequest carries the per-call inputs for Extract.
type ExtractRequest struct {
	// Prompt is the base instruction; the schema and any correction feedback are
	// appended to it on each attempt.
	Prompt string
	// ImageData is optional binary payload forwarded to the provider unchanged.
	ImageData []byte
	// JobID correlates published events and log entries.
	JobID string
	// Publish optionally receives progress events; it may be nil.
	Publish PublishFn
}

// FeedbackInput is passed to a feedback formatter to build the corrective prompt
// appended on the next attempt after a failure.
type FeedbackInput struct {
	// Schema is the JSON Schema the response must conform to.
	Schema map[string]any
	// Raw is the previous raw model response.
	Raw string
	// ParseError is set when the previous response could not be turned into a T:
	// it wraps ErrJSONMalformed when the response was not decodable JSON, or
	// ErrDecode when the response was syntactically valid, schema-conforming JSON
	// that still could not map onto T (for example a strict unknown field).
	// Formatters can distinguish the two with errors.Is.
	ParseError error
	// Fields lists the field-level violations when schema validation failed.
	Fields []*FieldError
}

type extractConfig struct {
	maxAttempts int
	schema      map[string]any
	schemaSet   bool
	strict      bool
	formatter   func(FeedbackInput) string
}

// ExtractOption configures Extract.
type ExtractOption interface {
	applyExtract(*extractConfig)
}

type extractOptionFunc func(*extractConfig)

func (f extractOptionFunc) applyExtract(c *extractConfig) { f(c) }

// WithMaxAttempts bounds how many provider round-trips Extract performs. Values
// below 1 are ignored, leaving the default of 3.
func WithMaxAttempts(n int) ExtractOption {
	return extractOptionFunc(func(c *extractConfig) {
		if n >= 1 {
			c.maxAttempts = n
		}
	})
}

// WithSchema overrides the schema derived from T with a caller-supplied JSON
// Schema map. The map must be non-nil and must describe an object root: a single
// "object" type, a union that includes "object", or a typeless object-shaped
// schema. A nil override or a non-object root returns ErrSchemaDerivation before
// any provider call, matching Extract's object-only extraction contract.
func WithSchema(schema map[string]any) ExtractOption {
	return extractOptionFunc(func(c *extractConfig) {
		c.schema = schema
		c.schemaSet = true
	})
}

// WithStrictDecoding rejects responses whose JSON contains fields absent from T
// (json.Decoder.DisallowUnknownFields).
func WithStrictDecoding(strict bool) ExtractOption {
	return extractOptionFunc(func(c *extractConfig) { c.strict = strict })
}

// WithFeedbackFormatter overrides how validation and JSON errors are rendered
// into the corrective prompt appended on the next attempt.
func WithFeedbackFormatter(fn func(FeedbackInput) string) ExtractOption {
	return extractOptionFunc(func(c *extractConfig) {
		if fn != nil {
			c.formatter = fn
		}
	})
}

// Extract calls a to obtain a response, decodes it into T, validates it against
// a schema (derived from T unless WithSchema is set), and — on JSON or
// field-level validation failure — feeds the specific errors back to the model
// and retries, bounded by WithMaxAttempts. It returns the decoded T on success,
// or the zero T wrapped with ErrMaxAttemptsExceeded once the budget is spent.
// The final error also wraps the last underlying cause (ErrJSONMalformed,
// ErrSchemaValidation, or ErrDecode) for errors.Is matching.
//
// Extract populates an object root only. A WithSchema override whose root is not
// an object (a non-object type, an object-excluding union, or a typeless schema
// carrying non-object constraints) returns ErrSchemaDerivation before any
// provider call.
func Extract[T any](ctx context.Context, a TextAnalyzer, req ExtractRequest, opts ...ExtractOption) (T, error) {
	var zero T
	if a == nil {
		return zero, ErrNilAnalyzer
	}

	cfg := extractConfig{
		maxAttempts: defaultMaxAttempts,
		formatter:   defaultFeedback,
	}
	for _, o := range opts {
		if o != nil {
			o.applyExtract(&cfg)
		}
	}

	schema := cfg.schema
	if cfg.schemaSet {
		if schema == nil {
			return zero, fmt.Errorf("%w: WithSchema requires a non-nil schema", ErrSchemaDerivation)
		}
	} else {
		derived, err := SchemaOf(zero)
		if err != nil {
			return zero, fmt.Errorf("deriving schema: %w", err)
		}
		schema = derived
	}

	// Extract can only populate an object root (SchemaOf is struct-only and the
	// response parser finds JSON objects), so reject any WithSchema whose root is
	// not an object before contacting the provider, rather than surfacing the
	// limitation later as a misleading ErrJSONMalformed. A typeless object-shaped
	// or empty schema is treated as an object, matching SchemaOf's contract.
	if reason, objectRoot := rootObjectContract(schema); !objectRoot {
		return zero, fmt.Errorf("%w: Extract requires an object root schema, got %s", ErrSchemaDerivation, reason)
	}

	// Marshal the schema once, before any provider call, so a schema that cannot
	// be rendered (for example a WithSchema map holding a non-finite float) fails
	// fast instead of silently sending the model no constraints.
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return zero, fmt.Errorf("marshaling schema: %w", err)
	}

	var (
		lastErr      error
		lastFeedback *FeedbackInput
	)
	for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}

		prompt := buildPrompt(req.Prompt, string(schemaJSON), lastFeedback, cfg.formatter)
		raw, err := a.Analyze(ctx, prompt, req.ImageData, req.JobID, req.Publish)
		if err != nil {
			return zero, fmt.Errorf("analyzing (attempt %d): %w", attempt, err)
		}

		value, feedback, err := decodeAndValidate[T](schema, raw, cfg.strict)
		if err == nil {
			return value, nil
		}
		lastErr = err
		lastFeedback = &feedback
	}

	return zero, fmt.Errorf("%w: %d attempts: %w", ErrMaxAttemptsExceeded, cfg.maxAttempts, lastErr)
}

// rootObjectContract reports whether schema describes an object root, the only
// shape Extract can populate. A type keyword that includes "object" (single or
// union) is an object root; a malformed type keyword or a type that excludes
// object is not. A typeless schema is an object root when it carries object
// keywords (properties/required/additionalProperties) or no structural keywords
// at all, but a typeless schema carrying clearly non-object keywords (array,
// string, or number constraints) is rejected so the object-only limitation fails
// fast instead of surfacing later as ErrJSONMalformed. reason names the
// offending shape for the error message.
func rootObjectContract(schema map[string]any) (reason string, objectRoot bool) {
	types, present, malformed := schemaTypes(schema)
	if present {
		if malformed {
			return fmt.Sprintf("malformed type %v", schema[PropType]), false
		}
		for _, t := range types {
			if t == TypeObject {
				return "", true
			}
		}
		return fmt.Sprintf("type %q", strings.Join(types, ",")), false
	}
	if hasAnyKey(schema, PropProperties, PropRequired, PropAdditional) {
		return "", true
	}
	if key, ok := firstPresentKey(schema, PropItems, PropMinItems, PropMaxItems, PropMinLength, PropMaxLength, PropPattern, PropMinimum, PropMaximum); ok {
		return fmt.Sprintf("typeless %s constraint", key), false
	}
	return "", true
}

// hasAnyKey reports whether schema contains any of the named keys.
func hasAnyKey(schema map[string]any, keys ...string) bool {
	for _, k := range keys {
		if _, ok := schema[k]; ok {
			return true
		}
	}
	return false
}

// firstPresentKey returns the first of keys present in schema, in the given order.
func firstPresentKey(schema map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if _, ok := schema[k]; ok {
			return k, true
		}
	}
	return "", false
}

// decodeAndValidate finds the first JSON object in raw, validates it against
// schema, and only then decodes it into T. Validating before decoding means a
// schema-conforming-shaped but type-wrong response (for example a string where T
// expects an int) yields field-level feedback wrapping ErrSchemaValidation
// rather than being mislabeled malformed JSON. On failure it returns the
// FeedbackInput to feed back to the model plus an error wrapping ErrJSONMalformed
// (no/invalid JSON), the joined ErrSchemaValidation field errors, or ErrDecode
// (valid, schema-conforming JSON that cannot map to T).
func decodeAndValidate[T any](schema map[string]any, raw string, strict bool) (T, FeedbackInput, error) {
	var zero T
	objects := ExtractAllJSON(raw)
	if len(objects) == 0 {
		err := fmt.Errorf("%w: no JSON object found", ErrJSONMalformed)
		return zero, FeedbackInput{Schema: schema, Raw: raw, ParseError: err}, err
	}

	objBytes := []byte(objects[0])
	if !json.Valid(objBytes) {
		err := fmt.Errorf("%w: response is not syntactically valid JSON", ErrJSONMalformed)
		return zero, FeedbackInput{Schema: schema, Raw: raw, ParseError: err}, err
	}

	if err := Validate(schema, objBytes); err != nil {
		return zero, FeedbackInput{Schema: schema, Raw: raw, Fields: fieldErrors(err)}, err
	}

	dec := json.NewDecoder(bytes.NewReader(objBytes))
	if strict {
		dec.DisallowUnknownFields()
	}
	var value T
	if err := dec.Decode(&value); err != nil {
		wrapped := fmt.Errorf("%w: %w", ErrDecode, err)
		return zero, FeedbackInput{Schema: schema, Raw: raw, ParseError: wrapped}, wrapped
	}
	return value, FeedbackInput{}, nil
}

// buildPrompt appends the schema instruction and, when the previous attempt
// failed, the correction feedback to the base prompt. schemaJSON is the
// pre-marshaled schema. Feedback is replaced each attempt rather than compounded.
func buildPrompt(base, schemaJSON string, feedback *FeedbackInput, formatter func(FeedbackInput) string) string {
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\nRespond with ONLY a single JSON object conforming to this JSON Schema:\n")
	b.WriteString(schemaJSON)
	if feedback != nil {
		b.WriteString("\n\n")
		b.WriteString(formatter(*feedback))
	}
	return b.String()
}

// defaultFeedback renders a deterministic correction message from a failed
// attempt for the next prompt. It distinguishes malformed JSON from a
// syntactically valid response that did not match the required structure
// (ErrDecode) so the model gets actionable guidance.
func defaultFeedback(in FeedbackInput) string {
	var b strings.Builder
	b.WriteString("Your previous response was invalid and must be corrected.")
	if in.ParseError != nil {
		if errors.Is(in.ParseError, ErrDecode) {
			b.WriteString("\nThe response was valid JSON but did not match the required structure: ")
		} else {
			b.WriteString("\nThe response did not contain a valid JSON object: ")
		}
		b.WriteString(in.ParseError.Error())
	}
	if len(in.Fields) > 0 {
		b.WriteString("\nFix the following field errors:")
		for _, fe := range in.Fields {
			b.WriteString("\n- ")
			b.WriteString(fe.Error())
		}
	}
	b.WriteString("\nReturn only the corrected JSON object, with no surrounding text.")
	return b.String()
}

// fieldErrors extracts the *FieldError values from a (possibly joined)
// validation error.
func fieldErrors(err error) []*FieldError {
	var out []*FieldError
	var collect func(error)
	collect = func(e error) {
		if e == nil {
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, sub := range joined.Unwrap() {
				collect(sub)
			}
			return
		}
		var fe *FieldError
		if errors.As(e, &fe) {
			out = append(out, fe)
		}
	}
	collect(err)
	return out
}

// NewTextTemplate wires a Template that returns the provider's raw text
// unchanged, ready to satisfy TextAnalyzer for Extract.
func NewTextTemplate[C any](p provider.Provider[C], opts ...TemplateConfigurator) (*Template[C, string], error) {
	return NewTemplate[C, string](p, rawTextParser, opts...)
}

func rawTextParser(s string) (string, error) { return s, nil }
