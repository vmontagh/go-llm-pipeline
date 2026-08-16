package analyzer_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

type recursiveNode struct {
	Next *recursiveNode `json:"next"`
}

type textValued struct {
	When customTime `json:"when"`
}

type customTime struct{}

func (customTime) MarshalText() ([]byte, error) { return []byte("2020-01-01"), nil }
func (*customTime) UnmarshalText([]byte) error  { return nil }

type marshalOnly struct{}

func (marshalOnly) MarshalText() ([]byte, error) { return []byte("x"), nil }

type textMarshalOnly struct {
	When marshalOnly `json:"when"`
}

type quotedNumber struct {
	Count int `json:"count,string"`
}

type flagged struct {
	On bool `json:"on" jsonschema:"enum=true|false"`
}

type embeddedInner struct {
	City string `json:"city"`
}

type embeddedOuter struct {
	embeddedInner
	Name string `json:"name"`
}

func TestSchemaOfHardening(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   any
		wantErr error
		check   func(t *testing.T, schema map[string]any)
	}{
		{
			name:    "recursive type rejected",
			value:   recursiveNode{},
			wantErr: analyzer.ErrSchemaDerivation,
		},
		{
			name:  "text marshaler and unmarshaler is a string",
			value: textValued{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "when")
				if got[analyzer.PropType] != analyzer.TypeString {
					t.Errorf("when type = %v, want string", got[analyzer.PropType])
				}
			},
		},
		{
			name:    "marshal-only text type rejected",
			value:   textMarshalOnly{},
			wantErr: analyzer.ErrSchemaDerivation,
		},
		{
			name:  "json string option is a string",
			value: quotedNumber{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "count")
				if got[analyzer.PropType] != analyzer.TypeString {
					t.Errorf("count type = %v, want string", got[analyzer.PropType])
				}
			},
		},
		{
			name:  "boolean enum coerced",
			value: flagged{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "on")
				want := []any{true, false}
				if diff := cmp.Diff(want, got[analyzer.PropEnum]); diff != "" {
					t.Errorf("on enum mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name:  "embedded fields promoted",
			value: embeddedOuter{},
			check: func(t *testing.T, schema map[string]any) {
				props, ok := schema[analyzer.PropProperties].(map[string]any)
				if !ok {
					t.Fatalf("schema has no properties: %v", schema)
				}
				if _, ok := props["city"]; !ok {
					t.Errorf("promoted field city missing: %v", props)
				}
				if _, ok := props["name"]; !ok {
					t.Errorf("field name missing: %v", props)
				}
				if _, ok := props["embeddedInner"]; ok {
					t.Errorf("embedded type leaked as a property")
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := analyzer.SchemaOf(tc.value)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("SchemaOf() error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SchemaOf() unexpected error: %v", err)
			}
			tc.check(t, got)
		})
	}
}

func TestSchemaOfInvalidTag(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value any
	}{
		{name: "invalid min", value: struct {
			N int `json:"n" jsonschema:"min=zero"`
		}{}},
		{name: "invalid pattern", value: struct {
			S string `json:"s" jsonschema:"pattern=("`
		}{}},
		{name: "unknown directive", value: struct {
			S string `json:"s" jsonschema:"bogus=1"`
		}{}},
		{name: "non-numeric enum on int", value: struct {
			N int `json:"n" jsonschema:"enum=a|b"`
		}{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := analyzer.SchemaOf(tc.value); !errors.Is(err, analyzer.ErrSchemaDerivation) {
				t.Errorf("SchemaOf() error = %v, want ErrSchemaDerivation", err)
			}
		})
	}
}

func TestValidateExactAndUnicode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		schema        map[string]any
		doc           string
		wantErr       error  // nil means the document must validate
		wantDetailHas string // substring required in the error message
	}{
		{
			name: "large integer maximum is exact",
			// 2^53, where float64 can no longer distinguish n and n+1.
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMaximum: json.Number("9007199254740992")},
			doc:     `9007199254740993`,
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "unicode length counts runes",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMaxLength: 1},
			doc:     `"é"`,
			wantErr: nil,
		},
		{
			name:    "int keyword from caller schema enforced",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: 5},
			doc:     `3`,
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "string slice enum rejects non-member",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []string{"new", "active"}},
			doc:     `"paused"`,
			wantErr: analyzer.ErrSchemaValidation,
		},
		{
			name:    "string slice enum accepts member",
			schema:  map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []string{"new", "active"}},
			doc:     `"new"`,
			wantErr: nil,
		},
		{
			name:          "invalid pattern surfaced not skipped",
			schema:        map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropPattern: "("},
			doc:           `"anything"`,
			wantErr:       analyzer.ErrSchemaValidation,
			wantDetailHas: "invalid",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := analyzer.Validate(tc.schema, []byte(tc.doc))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate(%s) error = %v, want errors.Is %v", tc.doc, err, tc.wantErr)
			}
			if tc.wantDetailHas != "" && !strings.Contains(err.Error(), tc.wantDetailHas) {
				t.Errorf("Validate(%s) error %q should contain %q", tc.doc, err, tc.wantDetailHas)
			}
		})
	}
}

func TestExactNumericPrecision(t *testing.T) {
	t.Parallel()

	type tagMin struct {
		N int64 `json:"n" jsonschema:"min=9007199254740993"`
	}
	type tagEnum struct {
		N int64 `json:"n" jsonschema:"enum=9007199254740993"`
	}

	schemaOf := func(t *testing.T, v any) map[string]any {
		t.Helper()
		s, err := analyzer.SchemaOf(v)
		if err != nil {
			t.Fatalf("SchemaOf(%T) error: %v", v, err)
		}
		return s
	}

	tests := []struct {
		name string
		// schema builds the schema under test; t is available for SchemaOf setup.
		schema func(t *testing.T) map[string]any
		// wantLiteral, when set, must appear in the marshaled schema so the exact
		// literal is preserved for the model.
		wantLiteral string
		accept      string // JSON document expected to validate
		reject      string // JSON document expected to fail validation
	}{
		{
			name:        "tag bound stores and validates exact literal",
			schema:      func(t *testing.T) map[string]any { return schemaOf(t, tagMin{}) },
			wantLiteral: "9007199254740993",
			accept:      `{"n":9007199254740993}`,
			reject:      `{"n":9007199254740992}`,
		},
		{
			name:   "numeric enum matches exact member only",
			schema: func(t *testing.T) map[string]any { return schemaOf(t, tagEnum{}) },
			accept: `{"n":9007199254740993}`,
			reject: `{"n":9007199254740992}`,
		},
		{
			name: "caller schema uint64 minimum enforced exactly",
			schema: func(*testing.T) map[string]any {
				return map[string]any{
					analyzer.PropType:    analyzer.TypeInteger,
					analyzer.PropMinimum: uint64(9007199254740993),
				}
			},
			accept: `9007199254740993`,
			reject: `9007199254740992`,
		},
		{
			// 2^100 and 2^100+1 round to the same big.Float at 64-bit precision but
			// are distinct big.Rat values.
			name: "bound beyond big.Float precision is exact",
			schema: func(*testing.T) map[string]any {
				return map[string]any{
					analyzer.PropType:    analyzer.TypeInteger,
					analyzer.PropMaximum: json.Number("1267650600228229401496703205376"),
				}
			},
			accept: `1267650600228229401496703205376`,
			reject: `1267650600228229401496703205377`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema := tc.schema(t)
			if tc.wantLiteral != "" {
				marshaled, err := json.Marshal(schema)
				if err != nil {
					t.Fatalf("Marshal(schema) error: %v", err)
				}
				if !strings.Contains(string(marshaled), tc.wantLiteral) {
					t.Errorf("schema %s should keep the exact literal %s", marshaled, tc.wantLiteral)
				}
			}
			if err := analyzer.Validate(schema, []byte(tc.accept)); err != nil {
				t.Errorf("Validate(%s) = %v, want nil", tc.accept, err)
			}
			if err := analyzer.Validate(schema, []byte(tc.reject)); !errors.Is(err, analyzer.ErrSchemaValidation) {
				t.Errorf("Validate(%s) = %v, want ErrSchemaValidation", tc.reject, err)
			}
		})
	}
}

func prop(schema map[string]any, name string) map[string]any {
	props, ok := schema[analyzer.PropProperties].(map[string]any)
	if !ok {
		return nil
	}
	got, ok := props[name].(map[string]any)
	if !ok {
		return nil
	}
	return got
}
