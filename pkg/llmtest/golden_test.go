package llmtest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/llmtest"
)

type result struct {
	Tags  []string          `json:"tags"`
	Attrs map[string]string `json:"attrs"`
}

// fakeTB records failures instead of failing the real test.
type fakeTB struct {
	testing.TB
	errored, fataled bool
}

func (*fakeTB) Helper()                 {}
func (f *fakeTB) Errorf(string, ...any) { f.errored = true }
func (f *fakeTB) Fatalf(string, ...any) { f.fataled = true }

const (
	permDir    = 0o750
	permFile   = 0o600
	goldenPath = "testdata/sub/r.golden"
)

const resultJSON = "{\n  \"tags\": [\n    \"cat\"\n  ],\n  \"attrs\": {\n    \"a\": \"1\",\n    \"b\": \"2\"\n  }\n}\n"

func TestGolden(t *testing.T) {
	got := result{Tags: []string{"cat"}, Attrs: map[string]string{"b": "2", "a": "1"}}
	tests := []struct {
		name        string
		golden      string // existing file content; empty means no file
		got         any
		update      bool
		wantErrored bool
		wantFataled bool
		wantFile    string // file content after the call; empty skips the check
	}{
		{name: "match", golden: resultJSON, got: got},
		{name: "matchCRLF", golden: "{\r\n  \"tags\": [\r\n    \"cat\"\r\n  ],\r\n  \"attrs\": {\r\n    \"a\": \"1\",\r\n    \"b\": \"2\"\r\n  }\r\n}\r\n", got: got},
		{name: "mismatch", golden: resultJSON, got: result{Tags: []string{"dog"}}, wantErrored: true},
		{name: "missing", got: got, wantFataled: true},
		{name: "unencodable", golden: resultJSON, got: make(chan int), wantFataled: true},
		{name: "updateCreates", got: got, update: true, wantFile: resultJSON},
		{name: "updateOverwrites", golden: "stale\n", got: got, update: true, wantFile: resultJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tt.golden != "" {
				writeFile(t, goldenPath, tt.golden)
			}
			update := "" // overrides any value inherited from the shell
			if tt.update {
				update = "1"
			}
			t.Setenv(llmtest.UpdateEnv, update)

			tb := &fakeTB{}
			llmtest.Golden(tb, "sub/r", tt.got)

			if tb.errored != tt.wantErrored || tb.fataled != tt.wantFataled {
				t.Errorf("Golden(%v) errored, fataled = %t, %t; want %t, %t", tt.got, tb.errored, tb.fataled, tt.wantErrored, tt.wantFataled)
			}
			if tt.wantFile == "" {
				return
			}
			data, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("reading golden: %v", err)
			}
			if diff := cmp.Diff(tt.wantFile, string(data)); diff != "" {
				t.Errorf("golden file mismatch (-want +got):\n%s", diff)
			}
		})
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
