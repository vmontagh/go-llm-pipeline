package analyzer

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
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
// produced by SchemaOf. It returns nil when data conforms, or an errors.Join of
// *FieldError values (each wrapping ErrSchemaValidation) describing every
// violation. Malformed JSON is reported as a single root-level error.
func Validate(schema map[string]any, data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
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
	items, ok := schema[PropItems].(map[string]any)
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
	if minLen, ok := schema[PropMinLength].(float64); ok && float64(len(s)) < minLen {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinLength, Detail: fmt.Sprintf("length %d is below minimum %d", len(s), int(minLen))})
	}
	if maxLen, ok := schema[PropMaxLength].(float64); ok && float64(len(s)) > maxLen {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaxLength, Detail: fmt.Sprintf("length %d exceeds maximum %d", len(s), int(maxLen))})
	}
	if pattern, ok := schema[PropPattern].(string); ok && pattern != "" {
		re, err := regexp.Compile(pattern)
		if err == nil && !re.MatchString(s) {
			*errs = append(*errs, &FieldError{Path: path, Rule: PropPattern, Detail: fmt.Sprintf("value %q does not match pattern %q", s, pattern)})
		}
	}
}

func validateNumber(schema map[string]any, typ string, value any, path string, errs *[]error) {
	n, ok := value.(float64)
	if !ok {
		addTypeError(errs, path, typ, value)
		return
	}
	if typ == TypeInteger && n != math.Trunc(n) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropType, Detail: fmt.Sprintf("value %v is not an integer", n)})
	}
	if !enumAllows(schema, n) {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropEnum, Detail: enumDetail(schema, n)})
	}
	if minVal, ok := schema[PropMinimum].(float64); ok && n < minVal {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMinimum, Detail: fmt.Sprintf("value %v is below minimum %v", n, minVal)})
	}
	if maxVal, ok := schema[PropMaximum].(float64); ok && n > maxVal {
		*errs = append(*errs, &FieldError{Path: path, Rule: PropMaximum, Detail: fmt.Sprintf("value %v exceeds maximum %v", n, maxVal)})
	}
}

// enumAllows reports whether value is permitted by an enum constraint. It
// returns true when no enum constraint is present.
func enumAllows(schema map[string]any, value any) bool {
	raw, ok := schema[PropEnum]
	if !ok {
		return true
	}
	members, ok := raw.([]any)
	if !ok || len(members) == 0 {
		return true
	}
	for _, m := range members {
		switch want := m.(type) {
		case string:
			if got, ok := value.(string); ok && got == want {
				return true
			}
		case float64:
			if got, ok := value.(float64); ok && got == want {
				return true
			}
		}
	}
	return false
}

func enumDetail(schema map[string]any, value any) string {
	members, _ := sliceField(schema, PropEnum)
	parts := make([]string, 0, len(members))
	for _, m := range members {
		parts = append(parts, fmt.Sprintf("%v", m))
	}
	return fmt.Sprintf("value %v is not one of [%s]", value, strings.Join(parts, ", "))
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
	case float64:
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

func sliceField(schema map[string]any, key string) ([]any, bool) {
	s, ok := schema[key].([]any)
	return s, ok
}
