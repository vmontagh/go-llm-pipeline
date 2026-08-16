package analyzer_test

import (
	"bytes"
	"encoding/json"
	"math"
	"math/rand/v2"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
)

// This file is the differential regression net for the analyzer's schema layer
// (issue #26). Schema derivation (SchemaOf), encoding/json's decode behavior, and
// the hand-written validator (Validate) are three independent sources of truth
// that must agree; almost every hardening round of the original PR closed a spot
// where they had drifted. Instead of enumerating those cases by hand forever,
// these tests generate random Go values across a catalog of tricky types and pin
// two invariants:
//
//	Property A (canonical acceptance):
//	    Validate(SchemaOf(T), json.Marshal(v)) == nil  for every value v of T.
//	    The schema must never reject an encoding the model could legitimately
//	    produce. This is the exact class of bug the review rounds kept finding.
//
//	Property B (no validate-before-decode gap):
//	    Validate(SchemaOf(T), x) == nil  =>  json.Unmarshal(x) into T succeeds.
//	    The validator must never be more permissive than the decoder, or a
//	    "schema-valid" response would still fail to decode.
//
// Property B has one documented exception: types whose wire form is type:string
// but whose decodable strings are a strict subset of all strings (for example
// time.Time, which parses RFC 3339). For those the schema string is a superset by
// design, so they are excluded from Property B via catalogEntry.stringSuperset.
//
// The Test* functions run a deterministic, seeded sweep so the invariants are
// exercised on every `make test`; the Fuzz* functions expose the same properties
// to `go test -fuzz` for deeper exploration.

// --- catalog of representative types -----------------------------------------

// Scalars covers every leaf kind SchemaOf supports directly.
type diffScalars struct {
	B    bool    `json:"b"`
	I    int     `json:"i"`
	I8   int8    `json:"i8"`
	I16  int16   `json:"i16"`
	I32  int32   `json:"i32"`
	I64  int64   `json:"i64"`
	U    uint    `json:"u"`
	U8   uint8   `json:"u8"`
	U16  uint16  `json:"u16"`
	U32  uint32  `json:"u32"`
	U64  uint64  `json:"u64"`
	UP   uintptr `json:"up"`
	F32  float32 `json:"f32"`
	F64  float64 `json:"f64"`
	Text string  `json:"text"`
}

// StringOpts exercises the json:",string" decode-backed path for the kinds
// encoding/json quotes (numbers and bool).
type diffStringOpts struct {
	I   int     `json:"i,string"`
	I8  int8    `json:"i8,string"`
	I64 int64   `json:"i64,string"`
	U16 uint16  `json:"u16,string"`
	F64 float64 `json:"f64,string"`
	B   bool    `json:"b,string"`
}

// JSONNumber exercises the reflect.String type that marshals as a bare number.
type diffJSONNumber struct {
	N json.Number `json:"n"`
}

type diffByte byte

// Bytes exercises the base64-string wire form of byte slices (plain and named
// element).
type diffBytes struct {
	Raw   []byte     `json:"raw"`
	Named []diffByte `json:"named"`
}

// ByteArray exercises the fixed-length array path, which stays a JSON array with
// minItems==maxItems rather than a base64 string.
type diffByteArray struct {
	A [4]byte `json:"a"`
}

// Collections exercises slices, arrays, and nested composites.
type diffCollections struct {
	Ints    []int     `json:"ints"`
	Strs    []string  `json:"strs"`
	Matrix  [][]int   `json:"matrix"`
	Fixed   [3]int    `json:"fixed"`
	Fixed2D [2][2]int `json:"fixed2d"`
}

type diffInner struct {
	X int    `json:"x"`
	Y string `json:"y"`
}

// Maps exercises map[string]T objects (additionalProperties).
type diffMaps struct {
	Counts map[string]int       `json:"counts"`
	Rows   map[string]diffInner `json:"rows"`
}

// Nested exercises structs within structs and slices of structs.
type diffNested struct {
	Inner  diffInner   `json:"inner"`
	List   []diffInner `json:"list"`
	Nested struct {
		Deep diffInner `json:"deep"`
	} `json:"nested"`
}

// Pointers exercises pointer fields; the generator always allocates them so the
// encoding is a value rather than null.
type diffPointers struct {
	P     *int       `json:"p"`
	Inner *diffInner `json:"inner"`
}

type diffEmbeddedBase struct {
	Base int `json:"base"`
}

// Embedded exercises anonymous-field promotion.
type diffEmbedded struct {
	diffEmbeddedBase
	Extra string `json:"extra"`
}

// Times exercises the type:string wire form of a TextUnmarshaler (time.Time),
// whose valid strings are a strict subset of all strings.
type diffTimes struct {
	When time.Time `json:"when"`
}

// catalogEntry pairs a struct type with its precomputed schema.
type catalogEntry struct {
	name string
	typ  reflect.Type
	// schema is SchemaOf applied to a zero value of typ.
	schema map[string]any
	// stringSuperset marks a type whose schema accepts strings the decoder
	// rejects (for example time.Time). Such types are excluded from Property B.
	stringSuperset bool
}

// buildCatalog reflects a schema for each catalog type, failing the test if any
// type is unexpectedly unsupported.
func buildCatalog(t testing.TB) []catalogEntry {
	t.Helper()
	specs := []struct {
		zero           any
		stringSuperset bool
	}{
		{zero: diffScalars{}},
		{zero: diffStringOpts{}},
		{zero: diffJSONNumber{}},
		{zero: diffBytes{}},
		{zero: diffByteArray{}},
		{zero: diffCollections{}},
		{zero: diffMaps{}},
		{zero: diffNested{}},
		{zero: diffPointers{}},
		{zero: diffEmbedded{}},
		{zero: diffTimes{}, stringSuperset: true},
	}
	catalog := make([]catalogEntry, 0, len(specs))
	for _, spec := range specs {
		typ := reflect.TypeOf(spec.zero)
		schema, err := analyzer.SchemaOf(spec.zero)
		if err != nil {
			t.Fatalf("SchemaOf(%s) failed: %v", typ, err)
		}
		catalog = append(catalog, catalogEntry{
			name:           typ.Name(),
			typ:            typ,
			schema:         schema,
			stringSuperset: spec.stringSuperset,
		})
	}
	return catalog
}

// --- random value generation --------------------------------------------------

// customGen produces random values for types that must not be filled field by
// field: json.Number marshals its raw contents (so it must be a valid number
// literal) and time.Time has unexported fields and a constrained textual form.
var customGen = map[reflect.Type]func(*rand.Rand) reflect.Value{
	reflect.TypeOf(json.Number("")): func(r *rand.Rand) reflect.Value {
		var s string
		if r.IntN(2) == 0 {
			s = strconv.FormatInt(int64(r.Uint64()), 10)
		} else {
			s = strconv.FormatFloat(randFloat(r, reflect.Float64), 'g', -1, 64)
		}
		return reflect.ValueOf(json.Number(s))
	},
	reflect.TypeOf(time.Time{}): func(r *rand.Rand) reflect.Value {
		// Keep the year within [0,9999] so json.Marshal does not reject it, and
		// drop monotonic/location noise so the RFC 3339 round-trip is exact.
		sec := r.Int64N(4_102_444_800) // ~year 2100
		return reflect.ValueOf(time.Unix(sec, r.Int64N(1_000_000_000)).UTC())
	},
}

// randValue builds a random value of type t whose JSON encoding is, by
// construction, schema-valid for SchemaOf's derivation of t.
func randValue(r *rand.Rand, t reflect.Type) reflect.Value {
	if gen, ok := customGen[t]; ok {
		return gen(r)
	}
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.Pointer:
		// Always allocate: a nil pointer would encode as null, which a non-null
		// property schema rejects and Property A does not intend to probe.
		v.Set(reflect.New(t.Elem()))
		v.Elem().Set(randValue(r, t.Elem()))
	case reflect.Bool:
		v.SetBool(r.IntN(2) == 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(int64(r.Uint64())) // truncates to the field width: always in range
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		v.SetUint(r.Uint64())
	case reflect.Float32, reflect.Float64:
		v.SetFloat(randFloat(r, t.Kind()))
	case reflect.String:
		v.SetString(randString(r))
	case reflect.Slice:
		n := r.IntN(5)
		s := reflect.MakeSlice(t, n, n) // non-nil so it encodes as [] not null
		for i := 0; i < n; i++ {
			s.Index(i).Set(randValue(r, t.Elem()))
		}
		v.Set(s)
	case reflect.Array:
		for i := 0; i < t.Len(); i++ {
			v.Index(i).Set(randValue(r, t.Elem()))
		}
	case reflect.Map:
		m := reflect.MakeMap(t)
		n := r.IntN(5)
		for i := 0; i < n; i++ {
			key := reflect.New(t.Key()).Elem()
			key.SetString(randString(r))
			m.SetMapIndex(key, randValue(r, t.Elem()))
		}
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" {
				continue // unexported; not part of the JSON encoding
			}
			v.Field(i).Set(randValue(r, field.Type))
		}
	}
	return v
}

// randFloat returns a finite value comfortably inside the width's range, so
// json.Marshal never fails on NaN/Inf and the value round-trips.
func randFloat(r *rand.Rand, kind reflect.Kind) float64 {
	mantissa := r.Float64()*2 - 1 // [-1,1]
	exp := r.IntN(41) - 20        // [-20,20]
	f := mantissa * math.Pow(10, float64(exp))
	if kind == reflect.Float32 {
		return float64(float32(f))
	}
	return f
}

var stringRunes = []rune("abcXYZ0123_-/ é™{}\"\\\n")

// randString builds a short random string, including characters that require
// JSON escaping, to exercise the string wire form.
func randString(r *rand.Rand) string {
	n := r.IntN(9)
	out := make([]rune, n)
	for i := range out {
		out[i] = stringRunes[r.IntN(len(stringRunes))]
	}
	return string(out)
}

// newRand returns a deterministic generator seeded from the two knobs the fuzz
// engine (or the deterministic sweep) supplies.
func newRand(a, b uint64) *rand.Rand {
	return rand.New(rand.NewPCG(a|1, b|1))
}

func iterations() int {
	if testing.Short() {
		return 50
	}
	return 400
}

// --- Property A: canonical encodings are always schema-valid -------------------

func TestSchemaAcceptsCanonicalEncoding(t *testing.T) {
	t.Parallel()
	catalog := buildCatalog(t)
	n := iterations()
	for _, entry := range catalog {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			for i := 0; i < n; i++ {
				r := newRand(uint64(i), 0x9E3779B97F4A7C15)
				checkCanonicalAccepted(t, entry, r)
			}
		})
	}
}

// checkCanonicalAccepted asserts Property A for one random value of entry's type.
func checkCanonicalAccepted(t *testing.T, entry catalogEntry, r *rand.Rand) {
	t.Helper()
	v := randValue(r, entry.typ)
	data, err := json.Marshal(v.Interface())
	if err != nil {
		t.Fatalf("marshal %s value: %v", entry.name, err)
	}
	if err := analyzer.Validate(entry.schema, data); err != nil {
		t.Fatalf("Validate rejected a canonical encoding of %s\njson: %s\nerror: %v",
			entry.name, data, err)
	}
}

// --- Property B: schema-valid documents always decode --------------------------

func TestValidateImpliesDecodable(t *testing.T) {
	t.Parallel()
	catalog := buildCatalog(t)
	n := iterations()
	for _, entry := range catalog {
		if entry.stringSuperset {
			continue // documented exception: schema string is a superset of decodable strings
		}
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			for i := 0; i < n; i++ {
				r := newRand(uint64(i), 0xD1B54A32D192ED03)
				checkValidateImpliesDecode(t, entry, r)
			}
		})
	}
}

// checkValidateImpliesDecode generates a canonical encoding, applies a random
// mutation, and asserts that whenever Validate accepts the result the decoder
// (configured to match additionalProperties:false) accepts it too.
func checkValidateImpliesDecode(t *testing.T, entry catalogEntry, r *rand.Rand) {
	t.Helper()
	v := randValue(r, entry.typ)
	data, err := json.Marshal(v.Interface())
	if err != nil {
		t.Fatalf("marshal %s value: %v", entry.name, err)
	}
	doc := mutateJSON(r, data)
	if err := analyzer.Validate(entry.schema, doc); err != nil {
		return // Validate rejected: the implication is vacuously satisfied
	}
	if derr := decodeStrict(doc, entry.typ); derr != nil {
		t.Fatalf("Validate accepted a document that does not decode into %s\njson: %s\ndecode error: %v",
			entry.name, doc, derr)
	}
}

// decodeStrict decodes doc into a fresh value of typ with DisallowUnknownFields,
// mirroring the additionalProperties:false that SchemaOf emits, so the decoder
// and validator judge unknown keys the same way.
func decodeStrict(doc []byte, typ reflect.Type) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	return dec.Decode(reflect.New(typ).Interface())
}

// mutateJSON perturbs a canonical document to explore the boundary of what
// Validate accepts. Most mutations make Validate reject (a vacuous case for
// Property B); the interesting ones keep it accepting, where the decoder must
// still succeed. An unparseable or non-object document is returned unchanged.
func mutateJSON(r *rand.Rand, data []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || len(obj) == 0 {
		return data
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	target := keys[r.IntN(len(keys))]
	switch r.IntN(6) {
	case 0:
		// unchanged canonical document
	case 1:
		delete(obj, target) // usually violates required
	case 2:
		obj["unexpected_"+randString(r)] = 1 // violates additionalProperties:false
	case 3:
		obj[target] = nil // usually violates the property's type
	case 4:
		obj[target] = json.Number("1e4000") // exceeds numeric magnitude bounds
	case 5:
		obj[target] = randString(r) // valid only where a string is expected
	}
	out, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return out
}

// --- fuzz targets -------------------------------------------------------------

// FuzzSchemaAcceptsCanonical exposes Property A to the native fuzz engine. sel
// selects a catalog type and seed drives the random value.
func FuzzSchemaAcceptsCanonical(f *testing.F) {
	catalog := buildCatalog(f)
	for i := range catalog {
		f.Add(uint(i), uint64(i)+1)
	}
	f.Fuzz(func(t *testing.T, sel uint, seed uint64) {
		entry := catalog[int(sel%uint(len(catalog)))]
		checkCanonicalAccepted(t, entry, newRand(seed, uint64(sel)+1))
	})
}

// FuzzValidateImpliesDecode exposes Property B to the native fuzz engine, feeding
// arbitrary documents straight to Validate and, on acceptance, to the decoder.
func FuzzValidateImpliesDecode(f *testing.F) {
	catalog := buildCatalog(f)
	strict := make([]catalogEntry, 0, len(catalog))
	for _, entry := range catalog {
		if !entry.stringSuperset {
			strict = append(strict, entry)
		}
	}
	for i, entry := range strict {
		data, err := json.Marshal(randValue(newRand(uint64(i)+1, 1), entry.typ).Interface())
		if err != nil {
			f.Fatalf("seed marshal %s: %v", entry.name, err)
		}
		f.Add(uint(i), data)
	}
	f.Fuzz(func(t *testing.T, sel uint, doc []byte) {
		entry := strict[int(sel%uint(len(strict)))]
		if err := analyzer.Validate(entry.schema, doc); err != nil {
			return
		}
		if derr := decodeStrict(doc, entry.typ); derr != nil {
			t.Fatalf("Validate accepted a document that does not decode into %s\njson: %s\ndecode error: %v",
				entry.name, doc, derr)
		}
	})
}
