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
	var errs []error
	validateValue(canonical, value, "", &errs)
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
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

func validateValue(schema map[string]any, value any, path string, errs *[]error) {
	types, present, malformed := schemaTypes(schema)
	switch {
	case malformed:
		*errs = append(*errs, &FieldError{
			Path:   path,
			Rule:   PropType,
			Detail: fmt.Sprintf("invalid schema type: %v", schema[PropType]),
		})
	case present:
		validateTypes(schema, types, value, path, errs)
	default:
		// A typeless schema constrains any instance, so JSON Schema still applies
		// the keywords relevant to whatever kind the instance actually is (for
		// example required/properties on an object, minLength on a string). It
		// never reports a type mismatch, since absence means every type is allowed.
		validateInferred(schema, value, path, errs)
	}
	// enum applies to every instance type, including arrays and objects, so it is
	// checked once here rather than inside each structural branch.
	validateEnum(schema, value, path, errs)
}

// validateInferred applies the keyword constraints relevant to the instance's
// own kind when the schema declares no type, so a typeless schema does not fail
// open. A number is validated as TypeNumber (no integer check, since no type
// declared it); bool and null carry no structural keywords.
func validateInferred(schema map[string]any, value any, path string, errs *[]error) {
	switch value.(type) {
	case map[string]any:
		validateObject(schema, value, path, errs)
	case []any:
		validateArray(schema, value, path, errs)
	case string:
		validateString(schema, value, path, errs)
	case json.Number:
		validateNumber(schema, TypeNumber, value, path, errs)
	}
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

// validateTypes enforces the type keyword without failing open: an unrecognized
// token yields a FieldError, a single type dispatches to its keyword checks, and
// a union accepts the value when it matches any listed type (applying that type's
// keyword checks) or reports a type error naming every allowed type.
func validateTypes(schema map[string]any, types []string, value any, path string, errs *[]error) {
	for _, typ := range types {
		if !isKnownType(typ) {
			*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("unsupported schema type %q", typ)})
			return
		}
	}
	if len(types) == 1 {
		validateTypedValue(schema, types[0], value, path, errs)
		return
	}
	for _, typ := range types {
		if valueMatchesType(typ, value) {
			validateTypedValue(schema, typ, value, path, errs)
			return
		}
	}
	*errs = append(*errs, &FieldError{
		Path:   path,
		Rule:   PropType,
		Detail: fmt.Sprintf("expected one of [%s], got %s", strings.Join(types, ", "), jsonKind(value)),
	})
}

// validateTypedValue applies the keyword constraints for a single JSON Schema
// type to value.
func validateTypedValue(schema map[string]any, typ string, value any, path string, errs *[]error) {
	switch typ {
	case TypeObject:
		validateObject(schema, value, path, errs)
	case TypeArray:
		validateArray(schema, value, path, errs)
	case TypeString:
		validateString(schema, value, path, errs)
	case TypeInteger, TypeNumber:
		validateNumber(schema, typ, value, path, errs)
	case TypeBoolean:
		if _, ok := value.(bool); !ok {
			addTypeError(errs, path, typ, value)
		}
	case TypeNull:
		if value != nil {
			addTypeError(errs, path, TypeNull, value)
		}
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
// json.Number with no fractional part.
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
		if !ok {
			return false
		}
		r, ok := toBigRat(n)
		return ok && r.IsInt()
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

func validateObject(schema map[string]any, value any, path string, errs *[]error) {
	obj, ok := value.(map[string]any)
	if !ok {
		addTypeError(errs, path, TypeObject, value)
		return
	}
	props, ok := propertiesMap(schema)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropProperties, Detail: "schema properties must be a schema object map"})
		return
	}
	names, malformed := requiredNames(schema)
	if malformed {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropRequired, Detail: "schema required must be an array of strings"})
	} else {
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
	validateAdditional(schema, obj, props, path, errs)
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

func validateArray(schema map[string]any, value any, path string, errs *[]error) {
	arr, ok := value.([]any)
	if !ok {
		addTypeError(errs, path, TypeArray, value)
		return
	}
	if minItems, present, valid := intKeyword(schema, PropMinItems); present {
		switch {
		case !valid:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMinItems, Detail: "schema minItems is not a usable non-negative integer"})
		case int64(len(arr)) < minItems:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMinItems, Detail: fmt.Sprintf("array length %d is below minimum %d", len(arr), minItems)})
		}
	}
	if maxItems, present, valid := intKeyword(schema, PropMaxItems); present {
		switch {
		case !valid:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxItems, Detail: "schema maxItems is not a usable non-negative integer"})
		case int64(len(arr)) > maxItems:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxItems, Detail: fmt.Sprintf("array length %d exceeds maximum %d", len(arr), maxItems)})
		}
	}
	raw, present := schema[PropItems]
	if !present {
		return
	}
	items, ok := raw.(map[string]any)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropItems, Detail: "schema items must be a schema object"})
		return
	}
	for i, elem := range arr {
		validateValue(items, elem, fmt.Sprintf("%s[%d]", path, i), errs)
	}
}

func validateString(schema map[string]any, value any, path string, errs *[]error) {
	s, ok := value.(string)
	if !ok {
		addTypeError(errs, path, TypeString, value)
		return
	}
	runes := int64(utf8.RuneCountInString(s))
	if minLen, present, valid := intKeyword(schema, PropMinLength); present {
		switch {
		case !valid:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMinLength, Detail: "schema minLength is not a usable non-negative integer"})
		case runes < minLen:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMinLength, Detail: fmt.Sprintf("length %d is below minimum %d", runes, minLen)})
		}
	}
	if maxLen, present, valid := intKeyword(schema, PropMaxLength); present {
		switch {
		case !valid:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxLength, Detail: "schema maxLength is not a usable non-negative integer"})
		case runes > maxLen:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxLength, Detail: fmt.Sprintf("length %d exceeds maximum %d", runes, maxLen)})
		}
	}
	if raw, present := schema[PropPattern]; present {
		pattern, ok := raw.(string)
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
	validateStringEncoded(schema, s, path, errs)
}

// validateStringEncoded enforces the internal x-goStringKind keyword emitted for
// json:",string" fields: the quoted string must decode into the recorded Go kind
// exactly as encoding/json would, so a schema-valid string is always decodable. A
// present-but-unusable keyword (non-string, or an unknown kind name) fails closed
// with a FieldError rather than silently skipping the check.
func validateStringEncoded(schema map[string]any, s, path string, errs *[]error) {
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
// decode target, so validateStringEncoded can reconstruct it from the schema
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

func validateNumber(schema map[string]any, typ string, value any, path string, errs *[]error) {
	num, ok := value.(json.Number)
	if !ok {
		addTypeError(errs, path, typ, value)
		return
	}
	s := num.String()
	// Guard before any big.Rat conversion: big.Rat.SetString expands decimal
	// exponents, so an untrusted literal like 1e100000000 could exhaust memory.
	if numberMagnitudeExceeded(s) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: "number magnitude exceeds supported range"})
		return
	}
	val, ok := toBigRat(num)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %s is not a number", s)})
		return
	}
	if typ == TypeInteger && !val.IsInt() {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %s is not an integer", s)})
	}
	if lo, present, valid := boundKeyword(schema, PropMinimum); present {
		switch {
		case !valid:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMinimum, Detail: "schema minimum is not a usable number"})
		case val.Cmp(lo) < 0:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMinimum, Detail: fmt.Sprintf("value %s is below minimum %s", s, boundString(schema, PropMinimum))})
		}
	}
	if hi, present, valid := boundKeyword(schema, PropMaximum); present {
		switch {
		case !valid:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMaximum, Detail: "schema maximum is not a usable number"})
		case val.Cmp(hi) > 0:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropMaximum, Detail: fmt.Sprintf("value %s exceeds maximum %s", s, boundString(schema, PropMaximum))})
		}
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
// lengths, array item counts). It reports present=false when the key is absent
// (skip the constraint) and valid=false when the key is present but not a usable
// non-negative integer — including a non-numeric value or one whose magnitude
// exceeds the safe conversion bound — so callers fail closed with a schema error
// rather than silently dropping the constraint.
func intKeyword(schema map[string]any, key string) (n int64, present, valid bool) {
	raw, ok := schema[key]
	if !ok {
		return 0, false, false
	}
	r, ok := toBigRat(raw)
	if !ok || !r.IsInt() || r.Sign() < 0 {
		return 0, true, false
	}
	num := r.Num()
	if !num.IsInt64() {
		return 0, true, false
	}
	return num.Int64(), true, true
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

// boundKeyword reads a numeric minimum/maximum keyword. It reports present=false
// when the key is absent (skip the comparison) and valid=false when the key is
// present but cannot be interpreted as a usable number — non-numeric or an
// over-magnitude literal — so callers fail closed with a schema error rather than
// silently dropping the bound.
func boundKeyword(schema map[string]any, key string) (bound *big.Rat, present, valid bool) {
	raw, ok := schema[key]
	if !ok {
		return nil, false, false
	}
	r, ok := toBigRat(raw)
	if !ok {
		return nil, true, false
	}
	return r, true, true
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
