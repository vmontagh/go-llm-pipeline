package gemini

import "github.com/google/generative-ai-go/genai"

const (
	typeString  = "string"
	typeNumber  = "number"
	typeInteger = "integer"
	typeBoolean = "boolean"
	typeArray   = "array"
	typeObject  = "object"
)

// convertJSONSchema translates a provider-agnostic JSON Schema
func convertJSONSchema(s map[string]any) *genai.Schema {
	if len(s) == 0 {
		return nil
	}
	out := &genai.Schema{}
	if t, ok := s["type"].(string); ok {
		out.Type = jsonTypeToGenAI(t)
	} else {
		return nil
	}
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
		out.Properties = make(map[string]*genai.Schema, len(props))
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

func jsonTypeToGenAI(t string) genai.Type {
	switch t {
	case typeString:
		return genai.TypeString
	case typeNumber:
		return genai.TypeNumber
	case typeInteger:
		return genai.TypeInteger
	case typeBoolean:
		return genai.TypeBoolean
	case typeArray:
		return genai.TypeArray
	case typeObject:
		return genai.TypeObject
	default:
		return genai.TypeUnspecified
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
