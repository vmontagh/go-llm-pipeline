package llmvcr_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
)

type result struct {
	Tags  []string          `json:"tags"`
	Attrs map[string]string `json:"attrs"`
}

type outcome struct {
	Errored, Fataled bool
}

// fakeTB records failures instead of failing the real test.
type fakeTB struct {
	testing.TB
	got outcome
}

func (*fakeTB) Helper()                 {}
func (f *fakeTB) Errorf(string, ...any) { f.got.Errored = true }
func (f *fakeTB) Fatalf(string, ...any) { f.got.Fataled = true }

const (
	permDir     = 0o750
	permFile    = 0o600
	goldenPath  = "testdata/sub/r.golden"
	goldenName  = "sub/r"
	escapeName  = "../outside" // resolves to outsidePath without os.Root
	outsidePath = "outside.golden"
)

const resultJSON = "{\n  \"tags\": [\n    \"cat\"\n  ],\n  \"attrs\": {\n    \"a\": \"1\",\n    \"b\": \"2\"\n  }\n}\n"

func TestGolden(t *testing.T) {
	got := result{Tags: []string{"cat"}, Attrs: map[string]string{"b": "2", "a": "1"}}
	tests := []struct {
		name     string
		file     string // Golden name; defaults to goldenName
		golden   string // existing file content; empty means no file
		outside  string // content of outsidePath, which must stay unchanged
		got      any
		update   bool
		want     outcome
		wantFile string // file content after the call; empty skips the check
	}{
		{name: "match", golden: resultJSON, got: got},
		{name: "escapeCompare", file: escapeName, golden: resultJSON, outside: resultJSON, got: got, want: outcome{Fataled: true}},
		{name: "escapeUpdate", file: escapeName, golden: resultJSON, outside: "stale\n", got: got, update: true, want: outcome{Fataled: true}},
		{name: "matchCRLF", golden: "{\r\n  \"tags\": [\r\n    \"cat\"\r\n  ],\r\n  \"attrs\": {\r\n    \"a\": \"1\",\r\n    \"b\": \"2\"\r\n  }\r\n}\r\n", got: got},
		{name: "mismatch", golden: resultJSON, got: result{Tags: []string{"dog"}}, want: outcome{Errored: true}},
		{name: "missing", got: got, want: outcome{Fataled: true}},
		{name: "unencodable", golden: resultJSON, got: make(chan int), want: outcome{Fataled: true}},
		{name: "updateCreates", got: got, update: true, wantFile: resultJSON},
		{name: "updateOverwrites", golden: "stale\n", got: got, update: true, wantFile: resultJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tt.golden != "" {
				writeFile(t, goldenPath, tt.golden)
			}
			if tt.outside != "" {
				writeFile(t, outsidePath, tt.outside)
			}
			update := "" // overrides any value inherited from the shell
			if tt.update {
				update = "1"
			}
			t.Setenv(llmvcr.GoldenUpdateEnv, update)

			tb := &fakeTB{}
			name := tt.file
			if name == "" {
				name = goldenName
			}
			llmvcr.Golden(tb, name, tt.got)

			if diff := cmp.Diff(tt.want, tb.got); diff != "" {
				t.Errorf("Golden(%q, %v) outcome mismatch (-want +got):\n%s", name, tt.got, diff)
			}
			if tt.outside != "" {
				assertFile(t, outsidePath, tt.outside)
			}
			if tt.wantFile == "" {
				return
			}
			assertFile(t, goldenPath, tt.wantFile)
		})
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %q: %v", path, err)
	}
	if diff := cmp.Diff(want, string(data)); diff != "" {
		t.Errorf("%q content mismatch (-want +got):\n%s", path, diff)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), permDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), permFile); err != nil {
		t.Fatal(err)
	}
}
