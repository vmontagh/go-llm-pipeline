package gemini

// JSON Schema type names accepted by convertJSONSchema.
const (
	typeString  = "string"
	typeNumber  = "number"
	typeInteger = "integer"
	typeBoolean = "boolean"
	typeArray   = "array"
	typeObject  = "object"
)

// Gemini Schema.Type enum values (an OpenAPI 3.0 subset).
const (
	geminiTypeUnspecified = "TYPE_UNSPECIFIED"
	geminiTypeString      = "STRING"
	geminiTypeNumber      = "NUMBER"
	geminiTypeInteger     = "INTEGER"
	geminiTypeBoolean     = "BOOLEAN"
	geminiTypeArray       = "ARRAY"
	geminiTypeObject      = "OBJECT"
)

// schema is the Gemini REST API Schema object used for structured output.
type schema struct {
	Type        string             `json:"type"`
	Description string             `json:"description,omitempty"`
	Nullable    bool               `json:"nullable,omitempty"`
	Required    []string           `json:"required,omitempty"`
	Properties  map[string]*schema `json:"properties,omitempty"`
	Items       *schema            `json:"items,omitempty"`
}

// convertJSONSchema translates a provider-agnostic JSON Schema representation into a Gemini schema.
func convertJSONSchema(s map[string]any) *schema {
	if len(s) == 0 {
		return nil
	}
	t, ok := s["type"].(string)
	if !ok {
		return nil
	}
	out := &schema{Type: geminiType(t)}
	if d, ok := s["description"].(string); ok {
		out.Description = d
	}
	if n, ok := s["nullable"].(bool); ok {
		out.Nullable = n
	}
	if req, ok := s["required"]; ok {
		out.Required = toStringSlice(req)
	}
	if props, ok := s["properties"].(map[string]any); ok && len(props) > 0 {
		out.Properties = make(map[string]*schema, len(props))
		for name, v := range props {
			if child, ok := v.(map[string]any); ok {
				if converted := convertJSONSchema(child); converted != nil {
					out.Properties[name] = converted
				}
			}
		}
	}
	if items, ok := s["items"].(map[string]any); ok {
		out.Items = convertJSONSchema(items)
	}
	return out
}

func geminiType(t string) string {
	switch t {
	case typeString:
		return geminiTypeString
	case typeNumber:
		return geminiTypeNumber
	case typeInteger:
		return geminiTypeInteger
	case typeBoolean:
		return geminiTypeBoolean
	case typeArray:
		return geminiTypeArray
	case typeObject:
		return geminiTypeObject
	default:
		return geminiTypeUnspecified
	}
}

func toStringSlice(v any) []string {
	switch xs := v.(type) {
	case []string:
		return append([]string(nil), xs...)
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}
