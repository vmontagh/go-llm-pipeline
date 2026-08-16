package analyzer

// JSON Schema primitives for schema-guided decoding.
const (
	TypeArray      = "array"
	TypeBoolean    = "boolean"
	TypeInteger    = "integer"
	TypeNull       = "null"
	TypeNumber     = "number"
	TypeObject     = "object"
	TypeString     = "string"
	PropAdditional = "additionalProperties"
	PropItems      = "items"
	PropMinItems   = "minItems"
	PropMaxItems   = "maxItems"
	PropProperties = "properties"
	PropRequired   = "required"
	PropType       = "type"
	// Constraint keywords emitted by SchemaOf and enforced by Validate.
	PropDescription = "description"
	PropEnum        = "enum"
	PropMinimum     = "minimum"
	PropMaximum     = "maximum"
	PropMinLength   = "minLength"
	PropMaxLength   = "maxLength"
	PropPattern     = "pattern"
	// PropStringEncodedKind is an internal, non-standard keyword recording the Go
	// type a type:string field decodes into: a json:",string" scalar kind (for
	// example "int8" or "float64") or StringEncodedBytes for a byte slice that
	// encoding/json marshals as a base64 string. Validate uses it to confirm a
	// quoted payload actually decodes into that type rather than approximating the
	// grammar with a regular expression, so schema-validity and decodability stay
	// identical by construction. The wire contract to the model remains
	// type:string.
	PropStringEncodedKind = "x-goStringKind"
	// StringEncodedBytes is the PropStringEncodedKind token for a []byte field,
	// which encoding/json marshals as a base64 string rather than a JSON array. It
	// is deliberately not a reflect.Kind name so it cannot collide with a scalar
	// kind token.
	StringEncodedBytes = "bytes"
)
