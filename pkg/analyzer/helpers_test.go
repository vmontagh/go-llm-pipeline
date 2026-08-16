package analyzer_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"testing"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// dupTagStruct builds, via reflection, a struct with two direct fields sharing
// the json name "x" (a compile-time source literal would trip govet's structtag
// check for the intentional duplicate).
func dupTagStruct() reflect.Type {
	return reflect.StructOf([]reflect.StructField{
		{Name: "A", Type: reflect.TypeOf(""), Tag: `json:"x"`},
		{Name: "B", Type: reflect.TypeOf(""), Tag: `json:"x"`},
	})
}

// schemaOfTagged derives the schema for a single field of the given kind
// carrying the supplied jsonschema tag, built dynamically so tag cases stay
// table-driven.
func schemaOfTagged(t *testing.T, kind reflect.Type, jsonschemaTag string) (map[string]any, error) {
	t.Helper()
	field := reflect.StructField{
		Name: "Field",
		Type: kind,
		Tag:  reflect.StructTag(fmt.Sprintf(`json:"field" jsonschema:%q`, jsonschemaTag)),
	}
	st := reflect.StructOf([]reflect.StructField{field})
	return analyzer.SchemaOf(reflect.New(st).Elem().Interface())
}

// propNames returns the sorted property names declared by a schema.
func propNames(schema map[string]any) []string {
	props, ok := schema[analyzer.PropProperties].(map[string]any)
	if !ok || len(props) == 0 {
		return nil
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// requiredList returns the sorted required property names of a schema.
func requiredList(schema map[string]any) []string {
	req, ok := schema[analyzer.PropRequired].([]string)
	if !ok || len(req) == 0 {
		return nil
	}
	out := append([]string(nil), req...)
	sort.Strings(out)
	return out
}

// schemaOfField derives the schema for a single field of the given type carrying
// arbitrary json and jsonschema tags, built dynamically so tag cases stay
// table-driven and govet-clean.
func schemaOfField(t *testing.T, typ reflect.Type, jsonTag, jsonschemaTag string) (map[string]any, error) {
	t.Helper()
	tag := "json:" + strconv.Quote(jsonTag)
	if jsonschemaTag != "" {
		tag += " jsonschema:" + strconv.Quote(jsonschemaTag)
	}
	st := reflect.StructOf([]reflect.StructField{{
		Name: "Field",
		Type: typ,
		Tag:  reflect.StructTag(tag),
	}})
	return analyzer.SchemaOf(reflect.New(st).Elem().Interface())
}

// chain is a cyclic named slice type (H1): it refers to itself and cannot be
// reflected into a finite schema.
type chain []chain

// tree is a cyclic named map type (H1).
type tree map[string]tree

// stringyInt decodes from a JSON string via a custom UnmarshalJSON, so its wire
// shape cannot be reflected from its underlying integer kind (H2).
type stringyInt int

func (s *stringyInt) UnmarshalJSON([]byte) error { return nil }

// tags is a named slice with custom JSON unmarshaling (H2).
type tags []string

func (t *tags) UnmarshalJSON([]byte) error { return nil }

// signedBounds returns the inclusive range of a signed integer of bits width as
// exact JSON-number literals, mirroring integerBounds so expectations stay
// architecture-correct rather than hardcoding 2^63.
func signedBounds(bits int) (lo, hi string) {
	one := big.NewInt(1)
	half := new(big.Int).Lsh(one, uint(bits-1))
	return new(big.Int).Neg(half).String(), new(big.Int).Sub(half, one).String()
}

// unsignedMax returns 2^bits-1 as a decimal string.
func unsignedMax(bits int) string {
	one := big.NewInt(1)
	return new(big.Int).Sub(new(big.Int).Lsh(one, uint(bits)), one).String()
}

type shadowedInner struct {
	X chan int
}

// shadowedOuter promotes shadowedInner.X (a chan, unsupported) but shadows it
// with a direct X of a supported type, which encoding/json keeps.
type shadowedOuter struct {
	shadowedInner
	X int
}

type ambiguousA struct {
	X chan int
}

type ambiguousB struct {
	X chan int
}

// ambiguousOuter promotes X from two embeddings at the same depth with no tag,
// so encoding/json drops the name entirely.
type ambiguousOuter struct {
	ambiguousA
	ambiguousB
	Y int
}

// stringOptionDecodes reports whether s decodes into a json:",string" field of
// the given kind exactly as encoding/json would. It is the oracle the derived
// schema must agree with, so schema-validity and decodability never diverge.
func stringOptionDecodes(t *testing.T, typ reflect.Type, s string) bool {
	t.Helper()
	holder := reflect.StructOf([]reflect.StructField{{
		Name: "Field",
		Type: typ,
		Tag:  reflect.StructTag(`json:"field,string"`),
	}})
	quoted, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("json.Marshal(%q): %v", s, err)
	}
	body := []byte(`{"field":`)
	body = append(body, quoted...)
	body = append(body, '}')
	return json.Unmarshal(body, reflect.New(holder).Interface()) == nil
}

type recursiveNode struct {
	Next *recursiveNode `json:"next"`
}

type textValued struct {
	When customTime `json:"when"`
}

type customTime struct{}

func (customTime) MarshalText() ([]byte, error) { return []byte("2020-01-01"), nil }

func (*customTime) UnmarshalText([]byte) error { return nil }

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

func collectFieldErrors(err error) []analyzer.FieldError {
	var out []analyzer.FieldError
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if joined, ok := e.(interface{ Unwrap() []error }); ok {
			for _, sub := range joined.Unwrap() {
				walk(sub)
			}
			return
		}
		var fe *analyzer.FieldError
		if errors.As(e, &fe) {
			out = append(out, *fe)
		}
	}
	walk(err)
	return out
}

type invoice struct {
	Number string  `json:"number" jsonschema:"minLength=1"`
	Total  float64 `json:"total" jsonschema:"min=0"`
}

// scriptedAnalyzer returns the next scripted response on each Analyze call,
// recording the prompts it received.
type scriptedAnalyzer struct {
	responses []string
	err       error
	prompts   []string
}

func (s *scriptedAnalyzer) Analyze(_ context.Context, prompt string, _ []byte, _ string, _ analyzer.PublishFn) (string, error) {
	s.prompts = append(s.prompts, prompt)
	if s.err != nil {
		return "", s.err
	}
	i := len(s.prompts) - 1
	if i >= len(s.responses) {
		return s.responses[len(s.responses)-1], nil
	}
	return s.responses[i], nil
}
