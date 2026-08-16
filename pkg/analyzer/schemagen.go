package analyzer

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// ErrSchemaDerivation is returned when a value cannot be reflected into a JSON
// Schema, for example when it is not a struct, contains an unsupported type, or
// carries an invalid jsonschema struct tag.
var ErrSchemaDerivation = errors.New("analyzer: schema derivation failed")

// schemaTag is the struct-tag key that carries optional JSON-Schema constraints
// (for example, `jsonschema:"description=City name;enum=a|b;minLength=1"`).
const schemaTag = "jsonschema"

var (
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
)

// SchemaOf reflects a JSON Schema (as a provider-agnostic map) from v, which
// must be a struct or a pointer to one. Field names are taken from json tags; a
// field is required unless it is a pointer or its json tag carries ",omitempty".
// Fields tagged json:",string" are derived as strings. Types implementing
// encoding.TextMarshaler (such as time.Time) are derived as strings; types with
// custom json.Marshaler behaviour and recursive (cyclic) types are rejected with
// ErrSchemaDerivation rather than producing a schema no response can satisfy.
// Anonymous struct fields are flattened following encoding/json promotion, with
// shallower (outer) fields winning name conflicts. The optional jsonschema
// struct tag adds description, enum, minimum/maximum, minLength/maxLength,
// pattern, and can force required/optional; enum members are string-valued for
// string fields and coerced to numbers or booleans for numeric/boolean fields.
// Objects emit additionalProperties:false so Validate rejects unexpected keys.
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
	return schemaForStruct(t, map[reflect.Type]bool{})
}

func schemaForType(t reflect.Type, seen map[reflect.Type]bool) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if implementsMarshaler(t, textMarshalerType) {
		return map[string]any{PropType: TypeString}, nil
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
		items, err := schemaForType(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{PropType: TypeArray, PropItems: items}, nil
	case reflect.Map:
		return map[string]any{PropType: TypeObject, PropAdditional: true}, nil
	case reflect.Struct:
		if implementsMarshaler(t, jsonMarshalerType) {
			return nil, fmt.Errorf("%w: type %s uses custom JSON marshaling", ErrSchemaDerivation, t)
		}
		return schemaForStruct(t, seen)
	default:
		return nil, fmt.Errorf("%w: unsupported type %s", ErrSchemaDerivation, t.Kind())
	}
}

func schemaForStruct(t reflect.Type, seen map[reflect.Type]bool) (map[string]any, error) {
	if seen[t] {
		return nil, fmt.Errorf("%w: recursive type %s", ErrSchemaDerivation, t)
	}
	seen[t] = true
	defer delete(seen, t)

	props := make(map[string]any)
	requiredSet := make(map[string]bool)
	var required []string
	// Embedded (promoted) fields are merged after direct fields so that
	// shallower names win, mirroring encoding/json.
	embedded := make(map[string]any)
	embeddedRequired := make(map[string]bool)
	var embeddedOrder []string

	for i := range t.NumField() {
		field := t.Field(i)
		info := jsonFieldInfo(&field)
		if info.skip || (!field.IsExported() && !field.Anonymous) {
			continue
		}

		if field.Anonymous && !info.explicitName {
			if err := promoteEmbedded(&field, seen, embedded, embeddedRequired, &embeddedOrder); err != nil {
				return nil, err
			}
			continue
		}
		if !field.IsExported() {
			continue
		}

		child, err := fieldSchema(&field, &info, seen)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.Name, err)
		}
		props[info.name] = child
		if isRequired(&field, &info) {
			requiredSet[info.name] = true
			required = append(required, info.name)
		}
	}

	for _, name := range embeddedOrder {
		if _, defined := props[name]; defined {
			continue
		}
		props[name] = embedded[name]
		if embeddedRequired[name] && !requiredSet[name] {
			requiredSet[name] = true
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

// promoteEmbedded flattens an anonymous struct field's properties into the
// parent, following encoding/json field promotion for the common case.
func promoteEmbedded(field *reflect.StructField, seen map[reflect.Type]bool, props map[string]any, required map[string]bool, order *[]string) error {
	ft := field.Type
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	if ft.Kind() != reflect.Struct || implementsMarshaler(ft, textMarshalerType) || implementsMarshaler(ft, jsonMarshalerType) {
		return fmt.Errorf("%w: unsupported embedded field %s", ErrSchemaDerivation, field.Name)
	}
	child, err := schemaForStruct(ft, seen)
	if err != nil {
		return err
	}
	childProps, ok := child[PropProperties].(map[string]any)
	if !ok {
		return nil
	}
	for name, sub := range childProps {
		if _, exists := props[name]; !exists {
			props[name] = sub
			*order = append(*order, name)
		}
	}
	for _, name := range requiredNames(child) {
		required[name] = true
	}
	return nil
}

func implementsMarshaler(t, iface reflect.Type) bool {
	return t.Implements(iface) || reflect.PointerTo(t).Implements(iface)
}

// fieldInfo holds the parsed json tag data for a struct field.
type fieldInfo struct {
	name         string
	explicitName bool
	omitempty    bool
	asString     bool
	skip         bool
}

// jsonFieldInfo parses a field's json struct tag.
func jsonFieldInfo(field *reflect.StructField) fieldInfo {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return fieldInfo{skip: true}
	}
	parts := strings.Split(tag, ",")
	info := fieldInfo{name: field.Name}
	if parts[0] != "" {
		info.name = parts[0]
		info.explicitName = true
	}
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty":
			info.omitempty = true
		case "string":
			info.asString = true
		}
	}
	return info
}

// fieldSchema derives the schema for a single field, applying the json:",string"
// option and any jsonschema tag constraints.
func fieldSchema(field *reflect.StructField, info *fieldInfo, seen map[reflect.Type]bool) (map[string]any, error) {
	var child map[string]any
	if info.asString {
		child = map[string]any{PropType: TypeString}
	} else {
		derived, err := schemaForType(field.Type, seen)
		if err != nil {
			return nil, err
		}
		child = derived
	}
	if err := applySchemaTag(child, field.Tag.Get(schemaTag)); err != nil {
		return nil, err
	}
	return child, nil
}

// isRequired reports whether a field is required in the derived schema.
func isRequired(field *reflect.StructField, info *fieldInfo) bool {
	tag := parseRequiredOverride(field.Tag.Get(schemaTag))
	switch {
	case tag == requiredForce:
		return true
	case tag == requiredForbid:
		return false
	case field.Type.Kind() == reflect.Pointer:
		return false
	default:
		return !info.omitempty
	}
}

type requiredOverride int

const (
	requiredDefault requiredOverride = iota
	requiredForce
	requiredForbid
)

const (
	schemaTagRequired = "required"
	schemaTagOptional = "optional"
)

func parseRequiredOverride(tag string) requiredOverride {
	for _, part := range strings.Split(tag, ";") {
		switch strings.TrimSpace(part) {
		case schemaTagRequired:
			return requiredForce
		case schemaTagOptional:
			return requiredForbid
		}
	}
	return requiredDefault
}

// applySchemaTag parses a jsonschema struct tag and merges its constraints into
// schema. It returns ErrSchemaDerivation for malformed directives so callers
// never receive a schema that silently drops a requested constraint.
func applySchemaTag(schema map[string]any, tag string) error {
	if strings.TrimSpace(tag) == "" {
		return nil
	}
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, _ := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if err := applySchemaDirective(schema, key, val); err != nil {
			return err
		}
	}
	return coerceEnumTypes(schema)
}

func applySchemaDirective(schema map[string]any, key, val string) error {
	switch key {
	case schemaTagRequired, schemaTagOptional:
		return nil // handled by parseRequiredOverride
	case "description":
		schema[PropDescription] = val
	case "pattern":
		if _, err := regexp.Compile(val); err != nil {
			return fmt.Errorf("%w: invalid pattern %q: %w", ErrSchemaDerivation, val, err)
		}
		schema[PropPattern] = val
	case "enum":
		schema[PropEnum] = parseEnum(val)
	case "min":
		return setFloat(schema, PropMinimum, key, val)
	case "max":
		return setFloat(schema, PropMaximum, key, val)
	case "minLength":
		return setFloat(schema, PropMinLength, key, val)
	case "maxLength":
		return setFloat(schema, PropMaxLength, key, val)
	default:
		return fmt.Errorf("%w: unknown jsonschema directive %q", ErrSchemaDerivation, key)
	}
	return nil
}

func setFloat(schema map[string]any, key, directive, val string) error {
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return fmt.Errorf("%w: %s expects a number, got %q", ErrSchemaDerivation, directive, val)
	}
	schema[key] = f
	return nil
}

// parseEnum splits a pipe-separated enum list into string members;
// coerceEnumTypes converts them to numbers or booleans as the field type
// requires.
func parseEnum(val string) []any {
	raw := strings.Split(val, "|")
	out := make([]any, 0, len(raw))
	for _, s := range raw {
		out = append(out, strings.TrimSpace(s))
	}
	return out
}

// coerceEnumTypes converts string enum members to the type of the schema so
// numeric and boolean enums declared via struct tags match decoded values. A
// member that cannot be converted is an error rather than a silently
// unmatchable constraint.
func coerceEnumTypes(schema map[string]any) error {
	members, ok := schema[PropEnum].([]any)
	if !ok {
		return nil
	}
	typ, _ := stringField(schema, PropType)
	switch typ {
	case TypeString:
		return nil
	case TypeInteger, TypeNumber:
		for i, m := range members {
			s, ok := m.(string)
			if !ok {
				continue
			}
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return fmt.Errorf("%w: enum member %q is not a number", ErrSchemaDerivation, s)
			}
			members[i] = f
		}
		return nil
	case TypeBoolean:
		for i, m := range members {
			s, ok := m.(string)
			if !ok {
				continue
			}
			b, err := strconv.ParseBool(s)
			if err != nil {
				return fmt.Errorf("%w: enum member %q is not a boolean", ErrSchemaDerivation, s)
			}
			members[i] = b
		}
		return nil
	default:
		return fmt.Errorf("%w: enum is not supported for type %q", ErrSchemaDerivation, typ)
	}
}
