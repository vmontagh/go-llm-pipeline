package analyzer

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
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
	textMarshalerType   = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	jsonMarshalerType   = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	jsonUnmarshalerType = reflect.TypeOf((*json.Unmarshaler)(nil)).Elem()
	jsonNumberType      = reflect.TypeOf(json.Number(""))
)

// SchemaOf reflects a JSON Schema (as a provider-agnostic map) from v, which
// must be a struct or a pointer to one. Field names are taken from json tags; a
// field is required unless it is a pointer or its json tag carries ",omitempty".
// Fields tagged json:",string" are derived as strings, but only for the kinds
// encoding/json actually quotes (bool, integer, float, and string); for any
// other kind the option is ignored and the field's real shape is derived, so the
// response still decodes. Because Extract decodes a
// response into T, string handling follows the decode contract: types that
// implement encoding.TextUnmarshaler (such as time.Time) are derived as strings,
// while a type that only marshals to text — or a struct with custom
// json.Marshaler/json.Unmarshaler behaviour — is rejected with ErrSchemaDerivation
// rather than producing a schema no decoded value can satisfy. Recursive (cyclic)
// types are also rejected. Maps with string keys become objects whose
// additionalProperties schema is derived from the element type. Anonymous struct
// fields are flattened following encoding/json promotion: the shallowest field
// wins a name conflict, a same-depth tie is resolved only when exactly one field
// is explicitly tagged, and an otherwise ambiguous name is omitted. The optional
// jsonschema struct tag adds description, enum, minimum/maximum, minLength/
// maxLength, pattern, and can force required/optional; enum members are
// string-valued for string fields and coerced to numbers or booleans for
// numeric/boolean fields. A constraint that cannot be enforced against the
// field's type is rejected with ErrSchemaDerivation (min/max require a numeric
// field; minLength/maxLength/pattern require a string field) so a requested
// constraint is never silently dropped. Objects emit additionalProperties:false
// so Validate rejects unexpected keys.
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
	return schemaForType(t, map[reflect.Type]bool{})
}

// schemaForType derives the schema for an arbitrary type, tracking every active
// named type in seen so cyclic collection/struct types (for example
// `type Chain []Chain`) are rejected instead of recursing forever.
func schemaForType(t reflect.Type, seen map[reflect.Type]bool) (map[string]any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// Only named types can form a cycle; anonymous composites such as [][]int
	// cannot refer to themselves, so guarding them would be a false positive.
	if isNamedType(t) {
		if seen[t] {
			return nil, fmt.Errorf("%w: recursive type %s", ErrSchemaDerivation, t)
		}
		seen[t] = true
		defer delete(seen, t)
	}

	// encoding/json gives UnmarshalJSON precedence over UnmarshalText, so decide
	// the wire shape from json.Unmarshaler first. A type that also unmarshals from
	// text (such as time.Time) round-trips through a JSON string; one with only a
	// custom UnmarshalJSON has an unknown wire shape and cannot be reflected.
	jsonU := implementsMarshaler(t, jsonUnmarshalerType)
	textU := implementsMarshaler(t, textUnmarshalerType)
	switch {
	case jsonU && textU:
		return map[string]any{PropType: TypeString}, nil
	case jsonU:
		return nil, fmt.Errorf("%w: type %s uses custom JSON unmarshaling; its wire shape cannot be reflected", ErrSchemaDerivation, t)
	case textU:
		return map[string]any{PropType: TypeString}, nil
	}
	if implementsMarshaler(t, textMarshalerType) {
		return nil, fmt.Errorf("%w: type %s marshals to text but cannot unmarshal from it", ErrSchemaDerivation, t)
	}
	switch t.Kind() {
	case reflect.String:
		// encoding/json marshals json.Number as a bare JSON number, not a quoted
		// string, even though its kind is reflect.String; describe it as a number
		// so a numeric response validates and decodes.
		if t == jsonNumberType {
			return map[string]any{PropType: TypeNumber}, nil
		}
		return map[string]any{PropType: TypeString}, nil
	case reflect.Bool:
		return map[string]any{PropType: TypeBoolean}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return integerSchema(t.Kind()), nil
	case reflect.Float32, reflect.Float64:
		return floatSchema(t.Kind()), nil
	case reflect.Slice:
		// encoding/json marshals a byte slice as a base64 string, not a JSON array,
		// so describe it as type:string and record the marker that drives a
		// decode-backed base64 check (schema-valid iff base64-decodable). A named
		// byte element decodes identically, so it takes the same path.
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{PropType: TypeString, PropStringEncodedKind: StringEncodedBytes}, nil
		}
		items, err := schemaForType(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{PropType: TypeArray, PropItems: items}, nil
	case reflect.Array:
		items, err := schemaForType(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		// A Go array has a fixed length, so bound the schema to exactly that many
		// items; without this a response with too few or too many elements would
		// be silently zero-filled or truncated on decode with no feedback.
		n := json.Number(strconv.Itoa(t.Len()))
		return map[string]any{PropType: TypeArray, PropItems: items, PropMinItems: n, PropMaxItems: n}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%w: map key type %s is not a string", ErrSchemaDerivation, t.Key())
		}
		elem, err := schemaForType(t.Elem(), seen)
		if err != nil {
			return nil, err
		}
		return map[string]any{PropType: TypeObject, PropAdditional: elem}, nil
	case reflect.Struct:
		if implementsMarshaler(t, jsonMarshalerType) {
			return nil, fmt.Errorf("%w: type %s uses custom JSON marshaling", ErrSchemaDerivation, t)
		}
		return structSchema(t, seen)
	default:
		return nil, fmt.Errorf("%w: unsupported type %s", ErrSchemaDerivation, t.Kind())
	}
}

// isNamedType reports whether t is a defined (named) type, which is a
// prerequisite for a type to refer to itself.
func isNamedType(t reflect.Type) bool {
	return t.Name() != ""
}

// integerSchema derives a type:integer schema whose minimum and maximum bound the
// concrete Go width, so a schema-conforming number always decodes into the field
// (a uint8 rejects -1 and 256 as schema violations with field-level feedback
// rather than a generic decode failure). Bounds are exact json.Number literals.
func integerSchema(kind reflect.Kind) map[string]any {
	schema := map[string]any{PropType: TypeInteger}
	lo, hi := integerBounds(kind)
	schema[PropMinimum] = lo
	schema[PropMaximum] = hi
	return schema
}

// integerBounds returns the inclusive range representable by an integer kind as
// exact json.Number literals. The platform-sized kinds (int, uint, uintptr) use
// strconv.IntSize so a 32-bit target derives 32-bit bounds; the explicitly-sized
// kinds keep their fixed widths.
func integerBounds(kind reflect.Kind) (lo, hi json.Number) {
	bits, signed := strconv.IntSize, true
	switch kind {
	case reflect.Int8:
		bits = 8
	case reflect.Int16:
		bits = 16
	case reflect.Int32:
		bits = 32
	case reflect.Int64:
		bits = 64
	case reflect.Int:
		bits = strconv.IntSize
	case reflect.Uint8:
		bits, signed = 8, false
	case reflect.Uint16:
		bits, signed = 16, false
	case reflect.Uint32:
		bits, signed = 32, false
	case reflect.Uint64:
		bits, signed = 64, false
	case reflect.Uint, reflect.Uintptr:
		bits, signed = strconv.IntSize, false
	}
	one := big.NewInt(1)
	if signed {
		half := new(big.Int).Lsh(one, uint(bits-1))
		return json.Number(new(big.Int).Neg(half).String()),
			json.Number(new(big.Int).Sub(half, one).String())
	}
	return json.Number("0"),
		json.Number(new(big.Int).Sub(new(big.Int).Lsh(one, uint(bits)), one).String())
}

// floatSchema derives a type:number schema carrying the representable bounds of
// the concrete float width as exact json.Number literals, so a value outside the
// range (for example 1e400 for float64) is rejected as a schema violation with
// field-level feedback rather than surfacing as an opaque decode failure.
func floatSchema(kind reflect.Kind) map[string]any {
	schema := map[string]any{PropType: TypeNumber}
	bitSize := 64
	maxVal := math.MaxFloat64
	if kind == reflect.Float32 {
		bitSize = 32
		maxVal = math.MaxFloat32
	}
	schema[PropMinimum] = json.Number(strconv.FormatFloat(-maxVal, 'g', -1, bitSize))
	schema[PropMaximum] = json.Number(strconv.FormatFloat(maxVal, 'g', -1, bitSize))
	return schema
}

// structSchema builds the object schema for a struct. The caller (schemaForType)
// has already guarded t against cycles.
func structSchema(t reflect.Type, seen map[reflect.Type]bool) (map[string]any, error) {
	fc := &fieldCollector{seen: seen}
	if err := fc.collect(t, 0, true); err != nil {
		return nil, err
	}
	props, required, err := resolveFields(fc.candidates)
	if err != nil {
		return nil, err
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

// fieldCandidate is one struct field (direct or promoted from an embedded
// struct) competing to define a JSON property. The schema is derived only after
// dominance resolution, so a field encoding/json would ignore (shadowed or part
// of an ambiguous name) never forces schema derivation of an unsupported type.
type fieldCandidate struct {
	name     string
	depth    int
	tagged   bool
	required bool
	field    reflect.StructField
	info     fieldInfo
	seen     map[reflect.Type]bool
}

// fieldCollector accumulates field candidates while walking a struct and its
// embedded structs, keeping shared state (visited types, discovery order) off
// the recursive call signature.
type fieldCollector struct {
	seen       map[reflect.Type]bool
	candidates []fieldCandidate
}

// collect walks t and its anonymous embedded structs, recording one candidate
// per exported field. Embedded structs are flattened following encoding/json
// promotion; parentRequired carries whether the embedding chain is itself
// required, so a field promoted through an optional (pointer/omitempty/
// jsonschema:"optional") embedding is never required.
func (fc *fieldCollector) collect(t reflect.Type, depth int, parentRequired bool) error {
	for i := range t.NumField() {
		field := t.Field(i)
		info := jsonFieldInfo(&field)
		if info.skip {
			continue
		}

		if field.Anonymous && !info.explicitName {
			ft := field.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct &&
				!implementsMarshaler(ft, textUnmarshalerType) &&
				!implementsMarshaler(ft, jsonMarshalerType) &&
				!implementsMarshaler(ft, jsonUnmarshalerType) {
				if fc.seen[ft] {
					return fmt.Errorf("%w: recursive type %s", ErrSchemaDerivation, ft)
				}
				fc.seen[ft] = true
				embRequired := parentRequired && isRequired(&field, &info)
				err := fc.collect(ft, depth+1, embRequired)
				delete(fc.seen, ft)
				if err != nil {
					return err
				}
				continue
			}
			// An embedded marshaler or non-struct type is treated as a single
			// field named after the type, matching encoding/json.
		}
		if !field.IsExported() {
			continue
		}

		fc.candidates = append(fc.candidates, fieldCandidate{
			name:     info.name,
			depth:    depth,
			tagged:   info.explicitName,
			required: parentRequired && isRequired(&field, &info),
			field:    field,
			info:     info,
			seen:     fc.seen,
		})
	}
	return nil
}

// resolveFields applies encoding/json dominance to the collected candidates:
// for each JSON name the shallowest field wins; a tie at the shallowest depth is
// resolved only when exactly one candidate is explicitly tagged, otherwise the
// name is ambiguous and omitted entirely. Schemas are derived only for the
// winning fields, so a shadowed or ambiguous field of an unsupported type is
// dropped (as encoding/json would) instead of failing derivation. Properties and
// required names are emitted in first-discovery order for stable output.
func resolveFields(candidates []fieldCandidate) (props map[string]any, required []string, err error) {
	groups := make(map[string][]fieldCandidate)
	var order []string
	for i := range candidates {
		c := &candidates[i]
		if _, seen := groups[c.name]; !seen {
			order = append(order, c.name)
		}
		groups[c.name] = append(groups[c.name], *c)
	}

	props = make(map[string]any)
	for _, name := range order {
		winner, ok := dominantField(groups[name])
		if !ok {
			continue
		}
		schema, derr := fieldSchema(&winner.field, &winner.info, winner.seen)
		if derr != nil {
			return nil, nil, fmt.Errorf("field %s: %w", winner.field.Name, derr)
		}
		props[name] = schema
		if winner.required {
			required = append(required, name)
		}
	}
	return props, required, nil
}

// dominantField selects the winning candidate for a JSON name, or reports false
// when the name is ambiguous.
func dominantField(group []fieldCandidate) (*fieldCandidate, bool) {
	if len(group) == 1 {
		return &group[0], true
	}
	minDepth := group[0].depth
	for i := range group {
		if group[i].depth < minDepth {
			minDepth = group[i].depth
		}
	}
	var shallow []*fieldCandidate
	for i := range group {
		if group[i].depth == minDepth {
			shallow = append(shallow, &group[i])
		}
	}
	if len(shallow) == 1 {
		return shallow[0], true
	}
	var tagged []*fieldCandidate
	for _, c := range shallow {
		if c.tagged {
			tagged = append(tagged, c)
		}
	}
	if len(tagged) == 1 {
		return tagged[0], true
	}
	return nil, false
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
	if info.asString && stringOptionApplies(baseKind(field.Type)) {
		if baseKind(field.Type) == reflect.String {
			return nil, fmt.Errorf(`%w: json:",string" on a string field requires double-encoded JSON, which cannot be expressed as a plain-string schema; remove the ,string option`, ErrSchemaDerivation)
		}
		child = map[string]any{PropType: TypeString}
		// A json:",string" numeric/bool field decodes from a quoted literal. Record
		// the Go kind so Validate can confirm the quoted string actually decodes
		// into it, making schema-validity and decodability identical by
		// construction (including width limits and encoding/json's exact number
		// grammar). A regular-expression approximation would drift from
		// encoding/json in both directions, so it is deliberately avoided. Recorded
		// before applySchemaTag so an explicit jsonschema:"pattern=" still composes.
		child[PropStringEncodedKind] = baseKind(field.Type).String()
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

// baseKind returns t's kind after dereferencing any pointer indirection.
func baseKind(t reflect.Type) reflect.Kind {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind()
}

// stringOptionApplies reports whether encoding/json honors the json:",string"
// option for a field of the given kind. json only quotes bool, integer, float,
// and string fields (including uintptr); for any other kind the option is
// silently ignored, so the schema must describe the field's real shape or no
// response could decode. A string-kind field is reported here but rejected by
// fieldSchema, because json:",string" double-encodes a string and cannot be a
// plain-string schema.
func stringOptionApplies(kind reflect.Kind) bool {
	switch kind {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.String:
		return true
	default:
		return false
	}
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
	if err := checkRequiredExclusive(tag); err != nil {
		return err
	}
	for _, part := range strings.Split(tag, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, hasValue := strings.Cut(part, "=")
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if err := applySchemaDirective(schema, key, val, hasValue); err != nil {
			return err
		}
	}
	return coerceEnumTypes(schema)
}

// checkRequiredExclusive rejects a tag that carries both the required and
// optional flags, whose effect would otherwise depend on directive order.
func checkRequiredExclusive(tag string) error {
	var hasRequired, hasOptional bool
	for _, part := range strings.Split(tag, ";") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch strings.TrimSpace(name) {
		case schemaTagRequired:
			hasRequired = true
		case schemaTagOptional:
			hasOptional = true
		}
	}
	if hasRequired && hasOptional {
		return fmt.Errorf("%w: required and optional are mutually exclusive", ErrSchemaDerivation)
	}
	return nil
}

func applySchemaDirective(schema map[string]any, key, val string, hasValue bool) error {
	switch key {
	case schemaTagRequired, schemaTagOptional:
		// Requiredness is applied by parseRequiredOverride from the bare flag; any
		// '=' form (required=true or even a trailing required=) is a mistake that
		// would otherwise be silently dropped.
		if hasValue {
			return fmt.Errorf("%w: %s does not take a value", ErrSchemaDerivation, key)
		}
		return nil
	case "description":
		schema[PropDescription] = val
	case "pattern":
		if err := requireType(schema, key, TypeString); err != nil {
			return err
		}
		if _, err := regexp.Compile(val); err != nil {
			return fmt.Errorf("%w: invalid pattern %q: %w", ErrSchemaDerivation, val, err)
		}
		schema[PropPattern] = val
	case "enum":
		schema[PropEnum] = parseEnum(val)
	case "min":
		if err := requireType(schema, key, TypeInteger, TypeNumber); err != nil {
			return err
		}
		return setBound(schema, PropMinimum, key, val)
	case "max":
		if err := requireType(schema, key, TypeInteger, TypeNumber); err != nil {
			return err
		}
		return setBound(schema, PropMaximum, key, val)
	case "minLength":
		if err := requireType(schema, key, TypeString); err != nil {
			return err
		}
		return setLength(schema, PropMinLength, key, val)
	case "maxLength":
		if err := requireType(schema, key, TypeString); err != nil {
			return err
		}
		return setLength(schema, PropMaxLength, key, val)
	default:
		return fmt.Errorf("%w: unknown jsonschema directive %q", ErrSchemaDerivation, key)
	}
	return nil
}

// requireType rejects a directive whose keyword cannot be enforced against the
// field's derived type, so a constraint is never silently dropped by Validate.
func requireType(schema map[string]any, directive string, want ...string) error {
	typ, _ := stringField(schema, PropType)
	for _, w := range want {
		if typ == w {
			return nil
		}
	}
	return fmt.Errorf("%w: %s is only valid for %s fields, not %s", ErrSchemaDerivation, directive, strings.Join(want, "/"), typ)
}

// setBound stores a numeric minimum/maximum as the exact JSON-number literal
// (json.Number) so validation compares against the value the caller wrote with
// no float64 rounding. It rejects anything that is not valid JSON-number syntax
// (for example "1/2"), which would otherwise fail later when the schema is
// marshaled for the model. When the field already carries a native width bound
// (from integerSchema/floatSchema), the tag may only narrow the range: a minimum
// is intersected via max(native, tagged) and a maximum via min(native, tagged),
// so a tag can never widen the range beyond what decodes into T.
func setBound(schema map[string]any, key, directive, val string) error {
	n, err := parseJSONNumber(val)
	if err != nil {
		return fmt.Errorf("%w: %s expects a number, got %q", ErrSchemaDerivation, directive, val)
	}
	schema[key] = narrowBound(schema[key], n, key)
	return nil
}

// narrowBound intersects a tagged bound with any pre-existing native bound so a
// tag only ever tightens the range. It returns tagged when there is no native
// bound. key selects the direction: PropMinimum keeps the larger value, any other
// (PropMaximum) keeps the smaller.
func narrowBound(native any, tagged json.Number, key string) json.Number {
	nativeNum, ok := native.(json.Number)
	if !ok {
		return tagged
	}
	nativeRat, nok := new(big.Rat).SetString(nativeNum.String())
	taggedRat, tok := new(big.Rat).SetString(tagged.String())
	if !nok || !tok {
		return tagged
	}
	cmp := taggedRat.Cmp(nativeRat)
	if key == PropMinimum {
		if cmp < 0 {
			return nativeNum
		}
		return tagged
	}
	if cmp > 0 {
		return nativeNum
	}
	return tagged
}

// setLength stores a string-length keyword (minLength/maxLength) as a
// non-negative integer json.Number. It rejects non-JSON-number forms (for
// example "01" or "+1"), fractional, negative, and non-finite values, which
// would otherwise fail later when the schema is marshaled for the model.
func setLength(schema map[string]any, key, directive, val string) error {
	n, err := parseJSONNumber(val)
	if err != nil {
		return fmt.Errorf("%w: %s expects a non-negative integer, got %q", ErrSchemaDerivation, directive, val)
	}
	r, ok := new(big.Rat).SetString(n.String())
	if !ok || !r.IsInt() || r.Sign() < 0 {
		return fmt.Errorf("%w: %s expects a non-negative integer, got %q", ErrSchemaDerivation, directive, val)
	}
	schema[key] = n
	return nil
}

// parseJSONNumber returns val as a json.Number only when it is valid JSON-number
// syntax, so bounds and enum members always marshal cleanly. JSON null decodes
// into an empty json.Number without error, so an empty result is rejected.
func parseJSONNumber(val string) (json.Number, error) {
	var n json.Number
	if err := json.Unmarshal([]byte(val), &n); err != nil {
		return "", err
	}
	if n == "" {
		return "", errNotJSONNumber
	}
	if numberMagnitudeExceeded(n.String()) {
		return "", errNotJSONNumber
	}
	return n, nil
}

// errNotJSONNumber flags a value (such as JSON null) that unmarshals into an
// empty json.Number; callers wrap it with ErrSchemaDerivation.
var errNotJSONNumber = errors.New("not a JSON number")

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
			n, err := parseJSONNumber(s)
			if err != nil {
				return fmt.Errorf("%w: enum member %q is not a number", ErrSchemaDerivation, s)
			}
			if typ == TypeInteger {
				r, ok := new(big.Rat).SetString(s)
				if !ok || !r.IsInt() {
					return fmt.Errorf("%w: enum member %q is not an integer", ErrSchemaDerivation, s)
				}
			}
			members[i] = n
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
