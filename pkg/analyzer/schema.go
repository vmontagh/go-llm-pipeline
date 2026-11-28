package analyzer

// JSON Schema primitives for schema-guided decoding.
const (
	typeArray      = "array"
	typeBoolean    = "boolean"
	typeInteger    = "integer"
	typeNull       = "null"
	typeNumber     = "number"
	typeObject     = "object"
	typeString     = "string"
	propAdditional = "additionalProperties"
	propItems      = "items"
	propMinItems   = "minItems"
	propMaxItems   = "maxItems"
	propProperties = "properties"
	propRequired   = "required"
	propType       = "type"
	// Constraint keywords emitted by SchemaOf and enforced by Validate.
	propDescription = "description"
	propEnum        = "enum"
	propMinimum     = "minimum"
	propMaximum     = "maximum"
	propMinLength   = "minLength"
	propMaxLength   = "maxLength"
	propPattern     = "pattern"
	// propStringEncodedKind is an internal, non-standard keyword recording the Go
	// type a type:string field decodes into: a json:",string" scalar kind (for
	// example "int8" or "float64") or stringEncodedBytes for a byte slice that
	// encoding/json marshals as a base64 string. Validate uses it to confirm a
	// quoted payload actually decodes into that type rather than approximating the
	// grammar with a regular expression, so schema-validity and decodability stay
	// identical by construction. The wire contract to the model remains
	// type:string.
	propStringEncodedKind = "x-goStringKind"
	// stringEncodedBytes is the propStringEncodedKind token for a []byte field,
	// which encoding/json marshals as a base64 string rather than a JSON array. It
	// is deliberately not a reflect.Kind name so it cannot collide with a scalar
	// kind token.
	stringEncodedBytes = "bytes"
)
