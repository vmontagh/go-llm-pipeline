package analyzer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ErrSchemaValidation is the sentinel wrapped by every validation failure so
// callers can match it with errors.Is.
var ErrSchemaValidation = errors.New("analyzer: schema validation failed")

// FieldError describes a single schema violation at a specific location.
type FieldError struct {
	// Path is the dotted/bracketed JSON location, for example "items[0].name".
	Path string
	// Rule is the JSON-Schema keyword that failed, for example "required".
	Rule string
	// Detail is a human-readable explanation suitable for model feedback.
	Detail string
}

// Error implements the error interface. It always wraps ErrSchemaValidation so
// errors.Is(fieldErr, ErrSchemaValidation) holds.
func (e *FieldError) Error() string {
	loc := e.Path
	if loc == "" {
		loc = "(root)"
	}
	return fmt.Sprintf("%s: %s: %s", loc, e.Rule, e.Detail)
}

// Unwrap reports ErrSchemaValidation so a FieldError satisfies errors.Is.
func (e *FieldError) Unwrap() error { return ErrSchemaValidation }

// ruleJSON labels FieldErrors that reject a document at the JSON-syntax level,
// before any schema keyword is applied.
const ruleJSON = "json"

// Numeric-magnitude caps bound how large a JSON number literal may be before it
// is rejected. big.Rat.SetString expands decimal exponents, so an unbounded
// value like 1e100000000 could exhaust memory; these limits keep conversion cheap
// while still admitting any realistic value.
const (
	maxNumberMantissaDigits = 4096
	maxNumberExponent       = 4096
)

// Validate checks data (a JSON document) against schema, a JSON Schema map as
// produced by SchemaOf or supplied via WithSchema. It returns nil when data
// conforms, or an errors.Join of *FieldError values (each wrapping
// ErrSchemaValidation) describing every violation. Numbers are decoded with
// json.Number and compared as exact rationals (math/big.Rat), so integer bounds
// and enum members hold beyond float64's 2^53 precision. The schema is first
// canonicalized through a JSON round-trip, so caller-supplied keyword values of
// any Go type (for example enum as []int or minimum as int64) are enforced
// consistently with the schema shown to the model. A schema that cannot be
// marshaled to JSON yields a wrapped error rather than a panic.
//
// Validation fails closed: every recognized keyword is either enforced or, when
// its schema value is malformed, reported as a FieldError, so a keyword is never
// silently dropped. A keyword the validator does not recognize is rejected rather
// than ignored, so a misspelled or unsupported keyword cannot mask a missing
// constraint.
func Validate(schema map[string]any, data []byte) error {
	canonical, err := canonicalizeSchema(schema)
	if err != nil {
		return fmt.Errorf("canonicalizing schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return &FieldError{Rule: ruleJSON, Detail: fmt.Sprintf("invalid JSON: %v", err)}
	}
	// Decoder.More is only meaningful inside an array or object; at the top level
	// it reports false for a stray "]" or "}". Require the stream to reach EOF so
	// "{} }", "{} ]", and "{} {}" are all rejected.
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return &FieldError{Rule: ruleJSON, Detail: "unexpected trailing content after JSON value"}
	}
	// Reject duplicate object keys before validating. Decoding into value above
	// keeps only the last value for a repeated key, so validateValue can never see
	// a shadowed first occurrence; a strict decode into the target type, however,
	// processes every occurrence and may fail on the shadowed one. Failing closed
	// here keeps validation and decoding consistent on an ambiguous document.
	if dup := duplicateKeyError(data); dup != nil {
		return dup
	}
	var errs []error
	// Reject unrecognized keywords across the whole schema structure first, so an
	// unknown keyword on a property or items schema fails closed even when the
	// instance omits that property or leaves an array empty.
	reportUnknownKeywordsTree(canonical, "", &errs)
	validateValue(canonical, value, "", &errs)
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// duplicateKeyError scans data for an object that declares the same key twice and
// returns a FieldError for the first such key, or nil when every object has unique
// keys. It streams tokens rather than decoding into a value, so a repeated key is
// observed instead of collapsed. data is assumed to be a single syntactically
// valid JSON document (Validate has already decoded it), so a token error is
// treated as no duplicate and left for the earlier syntax checks to report.
func duplicateKeyError(data []byte) *FieldError {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	type frame struct {
		object    bool
		expectKey bool
		seen      map[string]struct{}
	}
	var stack []frame
	// afterValue records that a value completed in the enclosing frame, so the
	// next token in an object is a key again.
	afterValue := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	// next reads the following token, reporting ok=false at end of input. The
	// document is the single valid JSON value Validate already confirmed, so a
	// read error only signals the stream is exhausted; confining it here keeps the
	// scan free of an error-to-nil return.
	next := func() (json.Token, bool) {
		tok, err := dec.Token()
		return tok, err == nil
	}
	for {
		tok, ok := next()
		if !ok {
			break
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{':
				stack = append(stack, frame{object: true, expectKey: true, seen: map[string]struct{}{}})
			case '[':
				stack = append(stack, frame{})
			case '}', ']':
				stack = stack[:len(stack)-1]
				afterValue()
			}
		case string:
			top := len(stack) - 1
			if top >= 0 && stack[top].object && stack[top].expectKey {
				if _, dup := stack[top].seen[t]; dup {
					return &FieldError{Rule: ruleJSON, Detail: fmt.Sprintf("duplicate object key %q", t)}
				}
				stack[top].seen[t] = struct{}{}
				stack[top].expectKey = false
				continue
			}
			afterValue()
		default:
			afterValue()
		}
	}
	return nil
}

// canonicalizeSchema round-trips a schema through JSON so every value takes a
// canonical decoded form (map[string]any, []any, json.Number, string, bool, or
// nil). Downstream validation then reads uniform shapes instead of special-casing
// arbitrary Go types such as []int enum members or int64 bounds.
func canonicalizeSchema(schema map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var canonical map[string]any
	if err := dec.Decode(&canonical); err != nil {
		return nil, err
	}
	return canonical, nil
}

// instanceKind classifies a decoded JSON value so a keyword rule can declare the
// single instance kind it constrains.
type instanceKind int

const (
	kindNull instanceKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

// kindOf classifies a decoded JSON value (as produced by a json.Decoder with
// UseNumber) into its instanceKind. A value that is not one of the JSON kinds is
// treated as null, which carries no keyword rules.
func kindOf(value any) instanceKind {
	switch value.(type) {
	case map[string]any:
		return kindObject
	case []any:
		return kindArray
	case string:
		return kindString
	case json.Number:
		return kindNumber
	case bool:
		return kindBool
	default:
		return kindNull
	}
}

// keywordRule binds a schema keyword to the single instance kind it constrains
// and the check that enforces it. validateValue applies a rule only when its
// keyword is present in the schema and the instance is of the rule's kind, so a
// present keyword is either enforced (matching kind) or inapplicable to the
// instance's kind — it is never silently dropped. This registry is also the
// source of truth for which keywords are recognized: reportUnknownKeywords fails
// closed on any keyword absent from it (plus the specially handled type, enum,
// and description).
type keywordRule struct {
	keyword string
	kind    instanceKind
	apply   func(schema map[string]any, value any, path string, errs *[]error)
}

// keywordRules is populated by init. It is not a package-level initializer
// expression because its apply functions transitively reference the registry
// (validateValue recurses through applyKeywordRules), which the compiler would
// otherwise reject as an initialization cycle.
var keywordRules []keywordRule

// recognizedKeywords is every schema keyword this validator understands: each
// keyword-driven rule plus type (handled by enforceType), enum (handled by
// validateEnum), and the description annotation (metadata, not validated). Any
// other keyword is rejected by reportUnknownKeywords, so the set of accepted
// keywords stays closed and a misspelled or unsupported keyword cannot be
// silently ignored. It is derived from keywordRules so a new rule is recognized
// automatically.
var recognizedKeywords map[string]struct{}

func init() {
	keywordRules = []keywordRule{
		{PropRequired, kindObject, applyRequired},
		{PropAdditional, kindObject, applyAdditional},
		{PropProperties, kindObject, applyProperties},
		{PropMinItems, kindArray, applyMinItems},
		{PropMaxItems, kindArray, applyMaxItems},
		{PropItems, kindArray, applyItems},
		{PropMinLength, kindString, applyMinLength},
		{PropMaxLength, kindString, applyMaxLength},
		{PropPattern, kindString, applyPattern},
		{PropStringEncodedKind, kindString, applyStringEncoded},
		{PropMinimum, kindNumber, applyMinimum},
		{PropMaximum, kindNumber, applyMaximum},
	}
	recognizedKeywords = map[string]struct{}{
		PropType:        {},
		PropEnum:        {},
		PropDescription: {},
	}
	for _, rule := range keywordRules {
		recognizedKeywords[rule.keyword] = struct{}{}
	}
}

// validateValue checks value against schema, appending a *FieldError for every
// violation. It enforces the type keyword, applies each present keyword rule whose
// instance kind matches value, and always checks enum — which constrains every
// instance kind, so it runs even after a type mismatch. Unrecognized keywords are
// not reported here: reportUnknownKeywordsTree already rejects them once across the
// whole schema structure before validateValue runs, independent of the instance.
func validateValue(schema map[string]any, value any, path string, errs *[]error) {
	if ok, integerRequired := enforceType(schema, value, path, errs); ok {
		applyKeywordRules(schema, value, integerRequired, path, errs)
	}
	// enum applies to every instance type, including arrays and objects, so it is
	// checked once here rather than inside each keyword rule.
	validateEnum(schema, value, path, errs)
}

// reportUnknownKeywordsTree walks the schema and every nested schema object it can
// reach — each declared property schema, the items schema, and a schema-valued
// additionalProperties — reporting unrecognized keywords at every node. It runs
// once over the schema structure, independent of the instance, so an unknown
// keyword on a property or items schema is rejected even when the instance omits
// that property or the array is empty. A property or items value that is not a
// schema object is skipped here; validateValue reports that malformation as an
// instance constraint.
func reportUnknownKeywordsTree(schema map[string]any, path string, errs *[]error) {
	reportUnknownKeywords(schema, path, errs)
	if props, ok := propertiesMap(schema); ok {
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if child, ok := props[name].(map[string]any); ok {
				reportUnknownKeywordsTree(child, childPath(path, name), errs)
			}
		}
	}
	if items, ok := schema[PropItems].(map[string]any); ok {
		reportUnknownKeywordsTree(items, path+"[]", errs)
	}
	if additional, ok := schema[PropAdditional].(map[string]any); ok {
		reportUnknownKeywordsTree(additional, childPath(path, "*"), errs)
	}
}

// reportUnknownKeywords appends a FieldError for every schema keyword this
// validator does not recognize, so a caller schema carrying an unsupported or
// misspelled keyword fails closed instead of having the keyword silently ignored.
// reportUnknownKeywordsTree calls it at every node of the schema structure.
func reportUnknownKeywords(schema map[string]any, path string, errs *[]error) {
	var unknown []string
	for key := range schema {
		if _, ok := recognizedKeywords[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		*errs = append(*errs, &FieldError{Path: path, Rule: key, Detail: "unrecognized schema keyword"})
	}
}

// applyKeywordRules applies every present keyword rule whose instance kind matches
// value. A number is first validated as a number (magnitude, exact-rational
// conversion, and integer form when integerRequired); a magnitude or conversion
// failure skips the numeric bound rules, mirroring encoding/json's inability to
// decode such a literal into a numeric field.
func applyKeywordRules(schema map[string]any, value any, integerRequired bool, path string, errs *[]error) {
	kind := kindOf(value)
	if kind == kindNumber {
		num, ok := value.(json.Number)
		if !ok || !checkNumberInstance(num, integerRequired, path, errs) {
			return
		}
	}
	for _, rule := range keywordRules {
		if rule.kind != kind {
			continue
		}
		if _, present := schema[rule.keyword]; !present {
			continue
		}
		rule.apply(schema, value, path, errs)
	}
}

// enforceType applies the type keyword and reports whether value may proceed to
// its keyword rules. It returns ok=false — skipping the keyword rules, though enum
// still runs — when the type keyword is malformed, names an unsupported type, or
// does not match value. For a matched number it reports integerRequired so the
// numeric check can enforce the integer literal form. A typeless schema matches
// every instance (ok=true) and never requires an integer form, so its keyword
// rules are still applied by instance kind.
func enforceType(schema map[string]any, value any, path string, errs *[]error) (ok, integerRequired bool) {
	types, present, malformed := schemaTypes(schema)
	switch {
	case malformed:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("invalid schema type: %v", schema[PropType])})
		return false, false
	case !present:
		return true, false
	}
	for _, typ := range types {
		if !isKnownType(typ) {
			*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("unsupported schema type %q", typ)})
			return false, false
		}
	}
	if len(types) == 1 {
		return enforceSingleType(types[0], value, path, errs)
	}
	for _, typ := range types {
		if valueMatchesType(typ, value) {
			return true, typ == TypeInteger
		}
	}
	*errs = append(*errs, &FieldError{
		Path:   path,
		Rule:   PropType,
		Detail: fmt.Sprintf("expected one of [%s], got %s", strings.Join(types, ", "), jsonKind(value)),
	})
	return false, false
}

// enforceSingleType checks value against a single declared type, reproducing the
// type-mismatch messages of the former per-kind validators. A number matches
// TypeNumber and TypeInteger regardless of literal form: the integer-form check is
// a separate, non-fatal numeric check, so type:integer reports a fractional
// literal as "not an integer" rather than as a type mismatch.
func enforceSingleType(typ string, value any, path string, errs *[]error) (ok, integerRequired bool) {
	switch typ {
	case TypeObject:
		if _, isObj := value.(map[string]any); !isObj {
			addTypeError(errs, path, TypeObject, value)
			return false, false
		}
	case TypeArray:
		if _, isArr := value.([]any); !isArr {
			addTypeError(errs, path, TypeArray, value)
			return false, false
		}
	case TypeString:
		if _, isStr := value.(string); !isStr {
			addTypeError(errs, path, TypeString, value)
			return false, false
		}
	case TypeBoolean:
		if _, isBool := value.(bool); !isBool {
			addTypeError(errs, path, TypeBoolean, value)
			return false, false
		}
	case TypeNull:
		if value != nil {
			addTypeError(errs, path, TypeNull, value)
			return false, false
		}
	case TypeInteger, TypeNumber:
		if _, isNum := value.(json.Number); !isNum {
			addTypeError(errs, path, typ, value)
			return false, false
		}
		return true, typ == TypeInteger
	}
	return true, false
}

// checkNumberInstance validates a number as a number before its bound rules run.
// It rejects an over-magnitude literal (which big.Rat conversion could not handle
// cheaply) and a literal that is not a usable number, returning false so the bound
// rules are skipped. When integerRequired, a non-integer literal is reported but
// does not stop the bound checks, matching encoding/json, which decodes 1e0 into a
// float field but not an integer one.
func checkNumberInstance(num json.Number, integerRequired bool, path string, errs *[]error) bool {
	s := num.String()
	// Guard before any big.Rat conversion: big.Rat.SetString expands decimal
	// exponents, so an untrusted literal like 1e100000000 could exhaust memory.
	if numberMagnitudeExceeded(s) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: "number magnitude exceeds supported range"})
		return false
	}
	if _, ok := toBigRat(num); !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %s is not a number", s)})
		return false
	}
	if integerRequired && !jsonIntegerLiteral(num) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %s is not an integer", s)})
	}
	return true
}

// schemaTypes reads the type keyword as a single string or a JSON Schema union
// (a []any of strings, or a native []string in a not-yet-canonicalized caller
// schema). It reports present=false when type is absent (leaving the instance
// unconstrained by type) and malformed=true when type is present but not a usable
// shape — an empty union, a union with a non-string member, or a
// non-string/non-array value — so the caller fails closed with a schema error
// rather than silently skipping type checks.
func schemaTypes(schema map[string]any) (types []string, present, malformed bool) {
	raw, ok := schema[PropType]
	if !ok {
		return nil, false, false
	}
	switch t := raw.(type) {
	case string:
		return []string{t}, true, false
	case []any:
		if len(t) == 0 {
			return nil, true, true
		}
		out := make([]string, 0, len(t))
		for _, v := range t {
			s, ok := v.(string)
			if !ok {
				return nil, true, true
			}
			out = append(out, s)
		}
		return out, true, false
	case []string:
		// A caller-supplied schema (checked before canonicalization) may hold a
		// native []string union; canonicalized schemas only ever carry []any.
		if len(t) == 0 {
			return nil, true, true
		}
		return append([]string(nil), t...), true, false
	default:
		return nil, true, true
	}
}

// isKnownType reports whether typ is a JSON Schema primitive this validator
// enforces.
func isKnownType(typ string) bool {
	switch typ {
	case TypeObject, TypeArray, TypeString, TypeInteger, TypeNumber, TypeBoolean, TypeNull:
		return true
	default:
		return false
	}
}

// valueMatchesType reports whether a decoded JSON value is of the given schema
// type, used to select the matching branch of a union. An integer matches only a
// json.Number written in integer form (see jsonIntegerLiteral).
func valueMatchesType(typ string, value any) bool {
	switch typ {
	case TypeNull:
		return value == nil
	case TypeBoolean:
		_, ok := value.(bool)
		return ok
	case TypeString:
		_, ok := value.(string)
		return ok
	case TypeNumber:
		_, ok := value.(json.Number)
		return ok
	case TypeInteger:
		n, ok := value.(json.Number)
		return ok && jsonIntegerLiteral(n)
	case TypeObject:
		_, ok := value.(map[string]any)
		return ok
	case TypeArray:
		_, ok := value.([]any)
		return ok
	default:
		return false
	}
}

// jsonNegativeZero is the sole exponent-free integer literal that decodes into a
// signed integer but not an unsigned one (strconv.ParseUint rejects the leading
// minus); jsonIntegerLiteral treats it as a non-integer for that reason.
const jsonNegativeZero = "-0"

// jsonIntegerLiteral reports whether n is written as a JSON integer, that is with
// no fractional part and no exponent. encoding/json decodes a number into a Go
// integer field only in this form: it rejects 1.0, 1e0, and 1000E0 even though
// they are mathematically integral. Validate treats exactly these literals as
// integers so schema-validity and decodability stay identical; a value like 1e0
// is reported as a non-integer rather than validating and then failing to decode.
// The decoder guarantees n is already a syntactically valid JSON number, so the
// absence of '.', 'e', and 'E' is sufficient to identify the integer grammar.
//
// Negative zero (jsonNegativeZero) is the one exception: it is exponent-free and
// satisfies the minimum:0 emitted for unsigned fields, but encoding/json decodes
// unsigned fields with strconv.ParseUint, which rejects the leading minus.
// Reporting it as a non-integer keeps Validate from accepting a value the decoder
// would reject. Canonical encodings never emit "-0", so this does not narrow
// canonical acceptance for signed fields.
func jsonIntegerLiteral(n json.Number) bool {
	s := string(n)
	return !strings.ContainsAny(s, ".eE") && s != jsonNegativeZero
}

// applyRequired enforces the required keyword: every listed property name must be
// present in the object. A required value that is not an array of strings fails
// closed with a schema error rather than silently dropping the constraint.
func applyRequired(schema map[string]any, value any, path string, errs *[]error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	names, malformed := requiredNames(schema)
	if malformed {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropRequired, Detail: "schema required must be an array of strings"})
		return
	}
	for _, name := range names {
		if _, present := obj[name]; !present {
			*errs = append(*errs, &FieldError{
				Path:   childPath(path, name),
				Rule:   PropRequired,
				Detail: "missing required property",
			})
		}
	}
}

// applyProperties validates each declared property against its child schema and
// fails closed when properties, or any child schema, is not a schema object map.
func applyProperties(schema map[string]any, value any, path string, errs *[]error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	props, ok := propertiesMap(schema)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropProperties, Detail: "schema properties must be a schema object map"})
		return
	}
	childNames := make([]string, 0, len(props))
	for name := range props {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)
	for _, name := range childNames {
		child, ok := props[name].(map[string]any)
		if !ok {
			*errs = append(*errs, &FieldError{
				Path:   childPath(path, name),
				Rule:   PropProperties,
				Detail: "property schema must be a schema object",
			})
			continue
		}
		if v, present := obj[name]; present {
			validateValue(child, v, childPath(path, name), errs)
		}
	}
}

// applyAdditional enforces the additionalProperties keyword. When properties is
// itself malformed it does nothing: the properties rule already reports that, and
// treating a misshapen properties as "no declared properties" would amplify a
// single schema error into a spurious "unexpected property" for every key.
func applyAdditional(schema map[string]any, value any, path string, errs *[]error) {
	obj, ok := value.(map[string]any)
	if !ok {
		return
	}
	props, ok := propertiesMap(schema)
	if !ok {
		return
	}
	validateAdditional(schema, obj, props, path, errs)
}

// propertiesMap reads the properties keyword. It reports ok=false only when the
// keyword is present but not an object map, so a caller schema that misshapes
// properties fails closed; an absent keyword yields a nil map and ok=true.
func propertiesMap(schema map[string]any) (map[string]any, bool) {
	raw, present := schema[PropProperties]
	if !present {
		return nil, true
	}
	m, ok := raw.(map[string]any)
	return m, ok
}

// validateAdditional enforces the additionalProperties keyword. A bool false
// rejects any property not declared in properties; a bool true (or absence)
// allows them; a schema map validates each extra property against that schema,
// which is how map[string]T fields are checked.
func validateAdditional(schema, obj, props map[string]any, path string, errs *[]error) {
	raw, present := schema[PropAdditional]
	if !present {
		return
	}
	switch additional := raw.(type) {
	case bool:
		if additional {
			return
		}
		extras := extraNames(obj, props)
		for _, name := range extras {
			*errs = append(*errs, &FieldError{
				Path:   childPath(path, name),
				Rule:   PropAdditional,
				Detail: "unexpected property",
			})
		}
	case map[string]any:
		for _, name := range extraNames(obj, props) {
			validateValue(additional, obj[name], childPath(path, name), errs)
		}
	default:
		*errs = append(*errs, &FieldError{
			Path:   path,
			Rule:   PropAdditional,
			Detail: "schema additionalProperties must be a boolean or schema object",
		})
	}
}

// extraNames returns the sorted object keys not declared in props.
func extraNames(obj, props map[string]any) []string {
	var extras []string
	for name := range obj {
		if _, declared := props[name]; !declared {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	return extras
}

// applyMinItems enforces the minItems keyword on an array instance.
func applyMinItems(schema map[string]any, value any, path string, errs *[]error) {
	arr, ok := value.([]any)
	if !ok {
		return
	}
	minItems, valid := intKeyword(schema, PropMinItems)
	switch {
	case !valid:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinItems, Detail: "schema minItems is not a usable non-negative integer"})
	case int64(len(arr)) < minItems:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinItems, Detail: fmt.Sprintf("array length %d is below minimum %d", len(arr), minItems)})
	}
}

// applyMaxItems enforces the maxItems keyword on an array instance.
func applyMaxItems(schema map[string]any, value any, path string, errs *[]error) {
	arr, ok := value.([]any)
	if !ok {
		return
	}
	maxItems, valid := intKeyword(schema, PropMaxItems)
	switch {
	case !valid:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxItems, Detail: "schema maxItems is not a usable non-negative integer"})
	case int64(len(arr)) > maxItems:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxItems, Detail: fmt.Sprintf("array length %d exceeds maximum %d", len(arr), maxItems)})
	}
}

// applyItems validates every array element against the items schema, failing
// closed when items is present but not a schema object.
func applyItems(schema map[string]any, value any, path string, errs *[]error) {
	arr, ok := value.([]any)
	if !ok {
		return
	}
	items, ok := schema[PropItems].(map[string]any)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropItems, Detail: "schema items must be a schema object"})
		return
	}
	for i, elem := range arr {
		validateValue(items, elem, fmt.Sprintf("%s[%d]", path, i), errs)
	}
}

// applyMinLength enforces the minLength keyword, counting runes so multi-byte
// characters are one unit each, consistent with JSON Schema.
func applyMinLength(schema map[string]any, value any, path string, errs *[]error) {
	s, ok := value.(string)
	if !ok {
		return
	}
	runes := int64(utf8.RuneCountInString(s))
	minLen, valid := intKeyword(schema, PropMinLength)
	switch {
	case !valid:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinLength, Detail: "schema minLength is not a usable non-negative integer"})
	case runes < minLen:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinLength, Detail: fmt.Sprintf("length %d is below minimum %d", runes, minLen)})
	}
}

// applyMaxLength enforces the maxLength keyword, counting runes.
func applyMaxLength(schema map[string]any, value any, path string, errs *[]error) {
	s, ok := value.(string)
	if !ok {
		return
	}
	runes := int64(utf8.RuneCountInString(s))
	maxLen, valid := intKeyword(schema, PropMaxLength)
	switch {
	case !valid:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxLength, Detail: "schema maxLength is not a usable non-negative integer"})
	case runes > maxLen:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxLength, Detail: fmt.Sprintf("length %d exceeds maximum %d", runes, maxLen)})
	}
}

// applyPattern enforces the pattern keyword. An empty pattern matches every string
// and is treated as no constraint; a non-string or uncompilable pattern fails
// closed with a schema error.
func applyPattern(schema map[string]any, value any, path string, errs *[]error) {
	s, ok := value.(string)
	if !ok {
		return
	}
	pattern, ok := schema[PropPattern].(string)
	switch {
	case !ok:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropPattern, Detail: "schema pattern must be a string"})
	case pattern == "":
		// An empty pattern matches every string; treat it as no constraint.
	default:
		re, err := regexp.Compile(pattern)
		switch {
		case err != nil:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropPattern, Detail: fmt.Sprintf("schema pattern %q is invalid: %v", pattern, err)})
		case !re.MatchString(s):
			*errs = append(*errs, &FieldError{Path: path, Rule: PropPattern, Detail: fmt.Sprintf("value %q does not match pattern %q", s, pattern)})
		}
	}
}

// applyStringEncoded enforces the internal x-goStringKind keyword emitted for
// json:",string" fields: the quoted string must decode into the recorded Go kind
// exactly as encoding/json would, so a schema-valid string is always decodable. A
// present-but-unusable keyword (non-string, or an unknown kind name) fails closed
// with a FieldError rather than silently skipping the check.
func applyStringEncoded(schema map[string]any, value any, path string, errs *[]error) {
	s, ok := value.(string)
	if !ok {
		return
	}
	raw, present := schema[PropStringEncodedKind]
	if !present {
		return
	}
	name, ok := raw.(string)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropStringEncodedKind, Detail: "schema x-goStringKind must be a string"})
		return
	}
	elem, ok := stringEncodedKindType(name)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropStringEncodedKind, Detail: fmt.Sprintf("schema x-goStringKind %q is not a supported kind", name)})
		return
	}
	if !stringDecodesInto(elem, s) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropStringEncodedKind, Detail: fmt.Sprintf("value %q does not decode into %s", s, name)})
	}
}

// stringDecodesInto reports whether s, as a JSON string payload, decodes into a
// field of type elem. It reproduces encoding/json's exact behavior by
// unmarshaling into a dynamically built holder struct, so validation and decoding
// never diverge: for a scalar the json:",string" tag enforces the width limits and
// number grammar, and for a byte slice (where json ignores ,string) it enforces
// base64 decodability.
func stringDecodesInto(elem reflect.Type, s string) bool {
	holder := reflect.StructOf([]reflect.StructField{{
		Name: "V",
		Type: elem,
		Tag:  reflect.StructTag(`json:"v,string"`),
	}})
	quoted, err := json.Marshal(s)
	if err != nil {
		return false
	}
	body := make([]byte, 0, len(quoted)+7)
	body = append(body, `{"v":`...)
	body = append(body, quoted...)
	body = append(body, '}')
	return json.Unmarshal(body, reflect.New(holder).Interface()) == nil
}

// stringEncodedKindTypes maps a PropStringEncodedKind token to a representative
// decode target, so applyStringEncoded can reconstruct it from the schema
// keyword. Scalar kinds use their reflect.Kind name; StringEncodedBytes maps to a
// byte slice, which encoding/json decodes from a base64 string.
var stringEncodedKindTypes = map[string]reflect.Type{
	reflect.Bool.String():    reflect.TypeOf(false),
	reflect.Int.String():     reflect.TypeOf(int(0)),
	reflect.Int8.String():    reflect.TypeOf(int8(0)),
	reflect.Int16.String():   reflect.TypeOf(int16(0)),
	reflect.Int32.String():   reflect.TypeOf(int32(0)),
	reflect.Int64.String():   reflect.TypeOf(int64(0)),
	reflect.Uint.String():    reflect.TypeOf(uint(0)),
	reflect.Uint8.String():   reflect.TypeOf(uint8(0)),
	reflect.Uint16.String():  reflect.TypeOf(uint16(0)),
	reflect.Uint32.String():  reflect.TypeOf(uint32(0)),
	reflect.Uint64.String():  reflect.TypeOf(uint64(0)),
	reflect.Uintptr.String(): reflect.TypeOf(uintptr(0)),
	reflect.Float32.String(): reflect.TypeOf(float32(0)),
	reflect.Float64.String(): reflect.TypeOf(float64(0)),
	StringEncodedBytes:       reflect.TypeOf([]byte(nil)),
}

func stringEncodedKindType(name string) (reflect.Type, bool) {
	t, ok := stringEncodedKindTypes[name]
	return t, ok
}

// applyMinimum enforces the minimum keyword. checkNumberInstance has already
// validated the number's magnitude and rational form, so the conversion here
// cannot fail; a non-numeric bound fails closed with a schema error.
func applyMinimum(schema map[string]any, value any, path string, errs *[]error) {
	num, ok := value.(json.Number)
	if !ok {
		return
	}
	val, _ := toBigRat(num)
	lo, valid := boundKeyword(schema, PropMinimum)
	switch {
	case !valid:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinimum, Detail: "schema minimum is not a usable number"})
	case val.Cmp(lo) < 0:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinimum, Detail: fmt.Sprintf("value %s is below minimum %s", num.String(), boundString(schema, PropMinimum))})
	}
}

// applyMaximum enforces the maximum keyword.
func applyMaximum(schema map[string]any, value any, path string, errs *[]error) {
	num, ok := value.(json.Number)
	if !ok {
		return
	}
	val, _ := toBigRat(num)
	hi, valid := boundKeyword(schema, PropMaximum)
	switch {
	case !valid:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaximum, Detail: "schema maximum is not a usable number"})
	case val.Cmp(hi) > 0:
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaximum, Detail: fmt.Sprintf("value %s exceeds maximum %s", num.String(), boundString(schema, PropMaximum))})
	}
}

// validateEnum enforces the enum keyword. An absent enum permits every value; a
// present enum that is not an array, or is an empty array (JSON Schema requires
// at least one member), is an invalid schema that permits nothing and yields a
// FieldError. Otherwise the value must deep-equal one member.
func validateEnum(schema map[string]any, value any, path string, errs *[]error) {
	raw, present := schema[PropEnum]
	if !present {
		return
	}
	members, ok := raw.([]any)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropEnum, Detail: "schema enum must be an array"})
		return
	}
	if len(members) == 0 {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropEnum, Detail: "schema enum is empty; no value is permitted"})
		return
	}
	for _, m := range members {
		if enumEqual(m, value) {
			return
		}
	}
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, fmt.Sprintf("%v", m))
	}
	*errs = append(*errs, &FieldError{
		Path:   path,
		Rule:   PropEnum,
		Detail: fmt.Sprintf("value %v is not one of [%s]", value, strings.Join(parts, ", ")),
	})
}

func enumEqual(member, value any) bool {
	return jsonDeepEqual(member, value)
}

// jsonDeepEqual reports whether two decoded JSON trees are equal, comparing
// numbers by exact rational value (so json.Number("1") equals the integer 1),
// strings and bools by ==, and arrays/objects structurally. It lets enum members
// match composite instances regardless of numeric spelling.
func jsonDeepEqual(a, b any) bool {
	if ar, aok := toBigRat(a); aok {
		br, bok := toBigRat(b)
		return bok && ar.Cmp(br) == 0
	}
	switch av := a.(type) {
	case string:
		bv, ok := b.(string)
		return ok && av == bv
	case bool:
		bv, ok := b.(bool)
		return ok && av == bv
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonDeepEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			bw, present := bv[k]
			if !present || !jsonDeepEqual(v, bw) {
				return false
			}
		}
		return true
	case nil:
		return b == nil
	default:
		return false
	}
}

// requiredNames reads the required keyword. It reports malformed=true when the
// keyword is present but not an array of strings (a non-array, or an array with a
// non-string member), so a misshapen caller schema fails closed instead of
// silently dropping the constraint. An absent keyword yields nil names and
// malformed=false.
func requiredNames(schema map[string]any) (names []string, malformed bool) {
	raw, present := schema[PropRequired]
	if !present {
		return nil, false
	}
	req, ok := raw.([]any)
	if !ok {
		return nil, true
	}
	out := make([]string, 0, len(req))
	for _, r := range req {
		s, ok := r.(string)
		if !ok {
			return nil, true
		}
		out = append(out, s)
	}
	return out, false
}

func addTypeError(errs *[]error, path, want string, value any) {
	*errs = append(*errs, &FieldError{
		Path:   path,
		Rule:   PropType,
		Detail: fmt.Sprintf("expected %s, got %s", want, jsonKind(value)),
	})
}

// jsonKind names the JSON type of a decoded value for error messages.
func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return TypeBoolean
	case json.Number, float64:
		return TypeNumber
	case string:
		return TypeString
	case []any:
		return TypeArray
	case map[string]any:
		return TypeObject
	default:
		return "unknown"
	}
}

func childPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}

func stringField(schema map[string]any, key string) (string, bool) {
	s, ok := schema[key].(string)
	return s, ok
}

// intKeyword reads a schema keyword that must be a non-negative integer (string
// lengths, array item counts). It reports valid=false when the key is absent or
// present but not a usable non-negative integer — a non-numeric value or one whose
// magnitude exceeds the safe conversion bound — so callers fail closed with a
// schema error rather than silently dropping the constraint. Callers apply it only
// when the keyword is present, so absence and malformity share the invalid result.
func intKeyword(schema map[string]any, key string) (n int64, valid bool) {
	raw, ok := schema[key]
	if !ok {
		return 0, false
	}
	r, ok := toBigRat(raw)
	if !ok || !r.IsInt() || r.Sign() < 0 {
		return 0, false
	}
	num := r.Num()
	if !num.IsInt64() {
		return 0, false
	}
	return num.Int64(), true
}

// numberMagnitudeExceeded reports whether a JSON number literal is large enough
// that converting it to an exact rational could exhaust memory: big.Rat.SetString
// expands decimal exponents, so a bounded mantissa-digit count and absolute
// exponent keep conversion cheap for untrusted input.
func numberMagnitudeExceeded(lit string) bool {
	mantissa := lit
	if i := strings.IndexAny(lit, "eE"); i >= 0 {
		mantissa = lit[:i]
		exp, err := strconv.Atoi(strings.TrimPrefix(lit[i+1:], "+"))
		if err != nil {
			return true // exponent too large to even parse
		}
		if exp > maxNumberExponent || exp < -maxNumberExponent {
			return true
		}
	}
	digits := 0
	for _, c := range mantissa {
		if c >= '0' && c <= '9' {
			digits++
		}
	}
	return digits > maxNumberMantissaDigits
}

// toBigRat converts a numeric value to an exact rational. json.Number keeps its
// full decimal literal; integers are converted without passing through float64
// so values beyond 2^53 stay exact. Only float32/float64 inputs (which are
// already inexact) go through SetFloat64.
func toBigRat(v any) (*big.Rat, bool) {
	switch n := v.(type) {
	case json.Number:
		if numberMagnitudeExceeded(n.String()) {
			return nil, false
		}
		r, ok := new(big.Rat).SetString(n.String())
		return r, ok
	case int:
		return new(big.Rat).SetInt64(int64(n)), true
	case int8:
		return new(big.Rat).SetInt64(int64(n)), true
	case int16:
		return new(big.Rat).SetInt64(int64(n)), true
	case int32:
		return new(big.Rat).SetInt64(int64(n)), true
	case int64:
		return new(big.Rat).SetInt64(n), true
	case uint:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(n))), true
	case uint8:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(n))), true
	case uint16:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(n))), true
	case uint32:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(uint64(n))), true
	case uint64:
		return new(big.Rat).SetInt(new(big.Int).SetUint64(n)), true
	case float32:
		r := new(big.Rat).SetFloat64(float64(n))
		return r, r != nil
	case float64:
		r := new(big.Rat).SetFloat64(n)
		return r, r != nil
	default:
		return nil, false
	}
}

// boundKeyword reads a numeric minimum/maximum keyword. It reports valid=false
// when the key is absent or present but not a usable number — non-numeric or an
// over-magnitude literal — so callers fail closed with a schema error rather than
// silently dropping the bound. Callers apply it only when the keyword is present.
func boundKeyword(schema map[string]any, key string) (bound *big.Rat, valid bool) {
	raw, ok := schema[key]
	if !ok {
		return nil, false
	}
	r, ok := toBigRat(raw)
	if !ok {
		return nil, false
	}
	return r, true
}

func boundString(schema map[string]any, key string) string {
	switch n := schema[key].(type) {
	case json.Number:
		return n.String()
	case float64:
		return strconv.FormatFloat(n, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", n)
	}
}
