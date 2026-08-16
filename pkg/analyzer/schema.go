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
	// scalar kind a json:",string" field decodes into (for example "int8" or
	// "float64"). Validate uses it to confirm a quoted payload actually decodes
	// into that kind rather than approximating the numeric/bool grammar with a
	// regular expression, so schema-validity and decodability stay identical by
	// construction. The wire contract to the model remains type:string.
	PropStringEncodedKind = "x-goStringKind"
)
