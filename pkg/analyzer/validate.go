package analyzer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
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

// Validate checks data (a JSON document) against schema, a JSON Schema map as
// produced by SchemaOf or supplied via WithSchema. It returns nil when data
// conforms, or an errors.Join of *FieldError values (each wrapping
// ErrSchemaValidation) describing every violation. Numbers are decoded with
// json.Number so integer bounds are compared exactly beyond 2^53. Numeric
// keywords accept any JSON-compatible Go numeric type and enums accept both
// []any and []string, so caller-supplied schemas are enforced consistently with
// the schema shown to the model.
func Validate(schema map[string]any, data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return &FieldError{Rule: "json", Detail: fmt.Sprintf("invalid JSON: %v", err)}
	}
	var errs []error
	validateValue(schema, value, "", &errs)
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

func validateValue(schema map[string]any, value any, path string, errs *[]error) {
	typ, _ := stringField(schema, PropType)
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
			return
		}
		if !enumAllows(schema, value) {
			*errs = append(*errs, &FieldError{Path: path, Rule: PropEnum, Detail: enumDetail(schema, value)})
		}
	}
}

func validateObject(schema map[string]any, value any, path string, errs *[]error) {
	obj, ok := value.(map[string]any)
	if !ok {
		addTypeError(errs, path, TypeObject, value)
		return
	}
	props, _ := mapField(schema, PropProperties)
	for _, name := range requiredNames(schema) {
		if _, present := obj[name]; !present {
			*errs = append(*errs, &FieldError{
				Path:   childPath(path, name),
				Rule:   PropRequired,
				Detail: "missing required property",
			})
		}
	}
	if additional, ok := schema[PropAdditional].(bool); ok && !additional {
		for name := range obj {
			if _, declared := props[name]; !declared {
				*errs = append(*errs, &FieldError{
					Path:   childPath(path, name),
					Rule:   PropAdditional,
					Detail: "unexpected property",
				})
			}
		}
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		child, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		if v, present := obj[name]; present {
			validateValue(child, v, childPath(path, name), errs)
		}
	}
}

func validateArray(schema map[string]any, value any, path string, errs *[]error) {
	arr, ok := value.([]any)
	if !ok {
		addTypeError(errs, path, TypeArray, value)
		return
	}
	items, ok := mapField(schema, PropItems)
	if !ok {
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
	if !enumAllows(schema, s) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropEnum, Detail: enumDetail(schema, s)})
	}
	runes := utf8.RuneCountInString(s)
	if minLen, ok := floatField(schema, PropMinLength); ok && float64(runes) < minLen {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinLength, Detail: fmt.Sprintf("length %d is below minimum %d", runes, int(minLen))})
	}
	if maxLen, ok := floatField(schema, PropMaxLength); ok && float64(runes) > maxLen {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxLength, Detail: fmt.Sprintf("length %d exceeds maximum %d", runes, int(maxLen))})
	}
	if pattern, ok := stringField(schema, PropPattern); ok && pattern != "" {
		re, err := regexp.Compile(pattern)
		switch {
		case err != nil:
			*errs = append(*errs, &FieldError{Path: path, Rule: PropPattern, Detail: fmt.Sprintf("schema pattern %q is invalid: %v", pattern, err)})
		case !re.MatchString(s):
			*errs = append(*errs, &FieldError{Path: path, Rule: PropPattern, Detail: fmt.Sprintf("value %q does not match pattern %q", s, pattern)})
		}
	}
}

func validateNumber(schema map[string]any, typ string, value any, path string, errs *[]error) {
	num, ok := value.(json.Number)
	if !ok {
		addTypeError(errs, path, typ, value)
		return
	}
	s := num.String()
	val, ok := new(big.Float).SetString(s)
	if !ok {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %s is not a number", s)})
		return
	}
	if typ == TypeInteger && !isIntegerLiteral(s) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %s is not an integer", s)})
	}
	if !enumAllows(schema, num) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropEnum, Detail: enumDetail(schema, num)})
	}
	if lo, ok := bigBound(schema, PropMinimum); ok && val.Cmp(lo) < 0 {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinimum, Detail: fmt.Sprintf("value %s is below minimum %s", s, boundString(schema, PropMinimum))})
	}
	if hi, ok := bigBound(schema, PropMaximum); ok && val.Cmp(hi) > 0 {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaximum, Detail: fmt.Sprintf("value %s exceeds maximum %s", s, boundString(schema, PropMaximum))})
	}
}

// enumAllows reports whether value is permitted by an enum constraint. It
// returns true when no enum constraint is present.
func enumAllows(schema map[string]any, value any) bool {
	members, ok := enumMembers(schema)
	if !ok || len(members) == 0 {
		return true
	}
	for _, m := range members {
		if enumEqual(m, value) {
			return true
		}
	}
	return false
}

func enumEqual(member, value any) bool {
	switch mv := member.(type) {
	case string:
		v, ok := value.(string)
		return ok && v == mv
	case bool:
		v, ok := value.(bool)
		return ok && v == mv
	default:
		mb, ok1 := toBigFloat(member)
		vb, ok2 := toBigFloat(value)
		return ok1 && ok2 && mb.Cmp(vb) == 0
	}
}

func enumDetail(schema map[string]any, value any) string {
	members, _ := enumMembers(schema)
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, fmt.Sprintf("%v", m))
	}
	return fmt.Sprintf("value %v is not one of [%s]", value, strings.Join(parts, ", "))
}

// enumMembers returns the enum members regardless of whether the schema stores
// them as []any (derived / JSON-decoded) or []string (caller-supplied).
func enumMembers(schema map[string]any) ([]any, bool) {
	switch e := schema[PropEnum].(type) {
	case []any:
		return e, true
	case []string:
		out := make([]any, len(e))
		for i, s := range e {
			out[i] = s
		}
		return out, true
	default:
		return nil, false
	}
}

func requiredNames(schema map[string]any) []string {
	switch req := schema[PropRequired].(type) {
	case []string:
		return req
	case []any:
		out := make([]string, 0, len(req))
		for _, r := range req {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
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

func mapField(schema map[string]any, key string) (map[string]any, bool) {
	m, ok := schema[key].(map[string]any)
	return m, ok
}

// floatField reads a numeric schema keyword, accepting any JSON-compatible Go
// numeric type so caller-supplied schemas (which may use int, int64, etc.) are
// enforced the same as derived schemas.
func floatField(schema map[string]any, key string) (float64, bool) {
	return toFloat(schema[key])
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// toBigFloat converts a numeric value to an arbitrary-precision float, using the
// exact decimal string for json.Number so large integers keep full precision.
func toBigFloat(v any) (*big.Float, bool) {
	if n, ok := v.(json.Number); ok {
		f, ok := new(big.Float).SetString(n.String())
		return f, ok
	}
	f, ok := toFloat(v)
	if !ok {
		return nil, false
	}
	return big.NewFloat(f), true
}

func bigBound(schema map[string]any, key string) (*big.Float, bool) {
	raw, present := schema[key]
	if !present {
		return nil, false
	}
	return toBigFloat(raw)
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

// isIntegerLiteral reports whether a json.Number string denotes an integer (no
// fraction or exponent), so integer validation is exact for values beyond 2^53.
func isIntegerLiteral(s string) bool {
	return !strings.ContainsAny(s, ".eE")
}
