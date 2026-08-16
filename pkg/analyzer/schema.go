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
)
