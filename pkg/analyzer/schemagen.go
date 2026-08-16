package analyzer

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// ErrSchemaDerivation is returned when a value cannot be reflected into a JSON
// Schema, for example when it is not a struct or contains an unsupported type.
var ErrSchemaDerivation = errors.New("analyzer: schema derivation failed")

// schemaTag is the struct-tag key that carries optional JSON-Schema constraints
// (for example, `jsonschema:"description=City name;enum=a|b;minLength=1"`).
const schemaTag = "jsonschema"

// SchemaOf reflects a JSON Schema (as a provider-agnostic map) from v, which
// must be a struct or a pointer to one. Field names are taken from json tags;
// a field is required unless it is a pointer or its json tag carries
// ",omitempty". The optional `jsonschema` struct tag adds description, enum,
// minimum/maximum, minLength/maxLength, pattern, and can force required. Enum
// members are string-valued for string fields and coerced to numbers for
// integer/number fields (for example, `jsonschema:"enum=1|2|3"`). Objects emit
// additionalProperties:false so Validate rejects unexpected keys.
func SchemaOf(v any) (map[string]any, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: nil value", ErrSchemaDerivation)
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("%w: want struct, got %s", ErrSchemaDerivation, t.Kind())
	}
	return schemaForStruct(t)
}

func schemaForType(t reflect.Type) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{PropType: TypeString}, nil
	case reflect.Bool:
		return map[string]any{PropType: TypeBoolean}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{PropType: TypeInteger}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{PropType: TypeNumber}, nil
	case reflect.Slice, reflect.Array:
		items, err := schemaForType(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{PropType: TypeArray, PropItems: items}, nil
	case reflect.Map:
		return map[string]any{PropType: TypeObject, PropAdditional: true}, nil
	case reflect.Struct:
		return schemaForStruct(t)
	default:
		return nil, fmt.Errorf("%w: unsupported type %s", ErrSchemaDerivation, t.Kind())
	}
}

func schemaForStruct(t reflect.Type) (map[string]any, error) {
	props := make(map[string]any)
	var required []string
	for i := range t.NumField() {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, omitempty, skip := jsonFieldName(&field)
		if skip {
			continue
		}
		child, err := schemaForType(field.Type)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.Name, err)
		}
		constraints, forceRequired, forbidRequired := parseSchemaTag(field.Tag.Get(schemaTag))
		for k, v := range constraints {
			child[k] = v
		}
		coerceEnumTypes(child)
		props[name] = child
		if isRequired(&field, omitempty, forceRequired, forbidRequired) {
			required = append(required, name)
		}
	}
	schema := map[string]any{
		PropType:       TypeObject,
		PropProperties: props,
		PropAdditional: false,
	}
	if len(required) > 0 {
		schema[PropRequired] = required
	}
	return schema, nil
}

// jsonFieldName resolves the JSON object key for a struct field, reporting
// whether the field carries ",omitempty" and whether it is skipped ("-").
func jsonFieldName(field *reflect.StructField) (name string, omitempty, skip bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name = field.Name
	if parts[0] != "" {
		name = parts[0]
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty, false
}

// isRequired reports whether a field is required in the derived schema. A field
// is required unless it is a pointer or json ",omitempty" marks it optional; the
// jsonschema tag can force ("required") or forbid ("optional") that decision.
func isRequired(field *reflect.StructField, omitempty, forceRequired, forbidRequired bool) bool {
	switch {
	case forceRequired:
		return true
	case forbidRequired:
		return false
	case field.Type.Kind() == reflect.Pointer:
		return false
	default:
		return !omitempty
	}
}

// parseSchemaTag parses a jsonschema struct tag into JSON-Schema constraint
// keys. It also reports an explicit required/optional override.
func parseSchemaTag(tag string) (constraints map[string]any, forceRequired, forbidRequired bool) {
	constraints = map[string]any{}
	if strings.TrimSpace(tag) == "" {
		return constraints, false, false
	}
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, hasVal := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch {
		case key == "required" && !hasVal:
			forceRequired = true
		case key == "optional" && !hasVal:
			forbidRequired = true
		case key == "description":
			constraints[PropDescription] = val
		case key == "pattern":
			constraints[PropPattern] = val
		case key == "enum":
			constraints[PropEnum] = parseEnum(val)
		case key == "min":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				constraints[PropMinimum] = f
			}
		case key == "max":
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				constraints[PropMaximum] = f
			}
		case key == "minLength":
			if n, err := strconv.Atoi(val); err == nil {
				constraints[PropMinLength] = float64(n)
			}
		case key == "maxLength":
			if n, err := strconv.Atoi(val); err == nil {
				constraints[PropMaxLength] = float64(n)
			}
		}
	}
	return constraints, forceRequired, forbidRequired
}

// parseEnum splits a pipe-separated enum list into its member values. Members
// are string-valued here; coerceEnumTypes converts them to numbers for numeric
// fields.
func parseEnum(val string) []any {
	raw := strings.Split(val, "|")
	out := make([]any, 0, len(raw))
	for _, s := range raw {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// coerceEnumTypes converts a schema's string enum members to float64 when the
// schema type is integer or number, so numeric enums declared via struct tags
// (for example, `jsonschema:"enum=1|2|3"`) match decoded numeric values. Members
// that do not parse as numbers are left unchanged.
func coerceEnumTypes(schema map[string]any) {
	typ, ok := schema[PropType].(string)
	if !ok || (typ != TypeInteger && typ != TypeNumber) {
		return
	}
	members, ok := schema[PropEnum].([]any)
	if !ok {
		return
	}
	coerced := make([]any, len(members))
	for i, m := range members {
		s, ok := m.(string)
		if !ok {
			coerced[i] = m
			continue
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			coerced[i] = f
			continue
		}
		coerced[i] = m
	}
	schema[PropEnum] = coerced
}
