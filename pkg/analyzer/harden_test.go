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
			name:  "text marshaler is a string",
			value: textValued{},
			check: func(t *testing.T, schema map[string]any) {
				got := prop(schema, "when")
				if got[analyzer.PropType] != analyzer.TypeString {
					t.Errorf("when type = %v, want string", got[analyzer.PropType])
				}
			},
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

	t.Run("large integer maximum is exact", func(t *testing.T) {
		t.Parallel()
		schema := map[string]any{
			analyzer.PropType: analyzer.TypeInteger,
			// 2^53, where float64 can no longer distinguish n and n+1.
			analyzer.PropMaximum: json.Number("9007199254740992"),
		}
		if err := analyzer.Validate(schema, []byte(`9007199254740993`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
			t.Errorf("Validate(2^53+1) = %v, want ErrSchemaValidation", err)
		}
	})

	t.Run("unicode length counts runes", func(t *testing.T) {
		t.Parallel()
		schema := map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropMaxLength: 1}
		if err := analyzer.Validate(schema, []byte(`"é"`)); err != nil {
			t.Errorf(`Validate("é", maxLength 1) = %v, want nil`, err)
		}
	})

	t.Run("int keyword from caller schema enforced", func(t *testing.T) {
		t.Parallel()
		schema := map[string]any{analyzer.PropType: analyzer.TypeInteger, analyzer.PropMinimum: 5}
		if err := analyzer.Validate(schema, []byte(`3`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
			t.Errorf("Validate(3, min int 5) = %v, want ErrSchemaValidation", err)
		}
	})

	t.Run("string slice enum enforced", func(t *testing.T) {
		t.Parallel()
		schema := map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropEnum: []string{"new", "active"}}
		if err := analyzer.Validate(schema, []byte(`"paused"`)); !errors.Is(err, analyzer.ErrSchemaValidation) {
			t.Errorf("Validate(paused, []string enum) = %v, want ErrSchemaValidation", err)
		}
		if err := analyzer.Validate(schema, []byte(`"new"`)); err != nil {
			t.Errorf("Validate(new, []string enum) = %v, want nil", err)
		}
	})

	t.Run("invalid pattern surfaced not skipped", func(t *testing.T) {
		t.Parallel()
		schema := map[string]any{analyzer.PropType: analyzer.TypeString, analyzer.PropPattern: "("}
		err := analyzer.Validate(schema, []byte(`"anything"`))
		if !errors.Is(err, analyzer.ErrSchemaValidation) {
			t.Fatalf("Validate(invalid pattern) = %v, want ErrSchemaValidation", err)
		}
		if !strings.Contains(err.Error(), "invalid") {
			t.Errorf("error %q should explain the invalid pattern", err)
		}
	})
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
