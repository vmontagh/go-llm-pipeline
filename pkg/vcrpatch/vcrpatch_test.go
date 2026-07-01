package vcrpatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

var errMutateTest = errors.New("mutate error")

func TestLoadSaveCassette(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	validYAML := `interactions:
- id: 1
  request:
    method: GET
    url: http://example.com
`
	validPath := filepath.Join(tempDir, "valid.yaml")
	if err := os.WriteFile(validPath, []byte(validYAML), 0o600); err != nil {
		t.Fatalf("error writing file: %v", err)
	}

	c, err := LoadCassette(validPath)
	if err != nil {
		t.Fatalf("LoadCassette failed: %v", err)
	}

	if len(c.Interactions) != 1 || c.Interactions[0].Request.Method != "GET" {
		t.Errorf("unexpected loaded cassette contents: %+v", c)
	}

	savedPath := filepath.Join(tempDir, "saved.yaml")
	if err := SaveCassette(savedPath, c); err != nil {
		t.Fatalf("SaveCassette failed: %v", err)
	}

	c2, err := LoadCassette(savedPath)
	if err != nil {
		t.Fatalf("LoadCassette on saved failed: %v", err)
	}

	if diff := cmp.Diff(c, c2, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("saved cassette differs from loaded (-want +got):\n%s", diff)
	}
}

func TestMatchers(t *testing.T) {
	t.Parallel()
	req := &InteractionReq{
		Method: "POST",
		URL:    "https://example.com/v1/generate?key=secret&method=test",
		Body:   `{"input": "hello"}`,
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
	}

	tests := []struct {
		name    string
		matcher Matcher
		want    bool
	}{
		{"URLContains success", URLContains("/v1/"), true},
		{"URLContains fail", URLContains("/v2/"), false},
		{"MethodEquals success", MethodEquals("post"), true},
		{"MethodEquals fail", MethodEquals("get"), false},
		{"BodyContains success", BodyContains("hello"), true},
		{"BodyContains fail", BodyContains("world"), false},
		{"HeaderEquals success", HeaderEquals("Content-Type", "application/json"), true},
		{"HeaderEquals case-insensitive key", HeaderEquals("content-type", "application/json"), true},
		{"HeaderEquals fail", HeaderEquals("Content-Type", "text/plain"), false},
		{"URLQueryParam success", URLQueryParam("method", "test"), true},
		{"URLQueryParam fail", URLQueryParam("method", "other"), false},
		{"And success", And(MethodEquals("POST"), URLContains("example.com")), true},
		{"And fail", And(MethodEquals("GET"), URLContains("example.com")), false},
		{"Or success", Or(MethodEquals("GET"), URLContains("example.com")), true},
		{"Or fail", Or(MethodEquals("GET"), URLContains("other.com")), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.matcher.Match(req)
			if got != tt.want {
				t.Errorf("matcher %s: got %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}

func TestNthMatch(t *testing.T) {
	t.Parallel()
	matcher := NthMatch(MethodEquals("POST"), 2)

	req1 := &InteractionReq{Method: "POST"}
	req2 := &InteractionReq{Method: "GET"}
	req3 := &InteractionReq{Method: "POST"}

	if matcher.Match(req1) {
		t.Error("expected 1st match to be false (n=2)")
	}
	if matcher.Match(req2) {
		t.Error("expected 2nd non-matching request to be false")
	}
	if !matcher.Match(req3) {
		t.Error("expected 3rd request (2nd POST match) to be true")
	}
}

func TestActionsAndScenario(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	baseYAML := `interactions:
- id: 1
  request:
    method: POST
    url: http://example.com/api
    body: '{"foo": "bar"}'
  response:
    code: 200
    body: '{"original": true}'
    headers:
      Content-Type: [application/json]
`
	basePath := filepath.Join(tempDir, "base.yaml")
	targetPath := filepath.Join(tempDir, "target.yaml")

	if err := os.WriteFile(basePath, []byte(baseYAML), 0o600); err != nil {
		t.Fatalf("error writing file: %v", err)
	}

	scenario := Scenario{
		BaseCassette:   basePath,
		TargetCassette: targetPath,
		Patches: []Patch{
			{
				Match: URLContains("/api"),
				Action: Chain(
					ReplaceStatusCode(429),
					ReplaceBody(`{"error": "rate limited"}`),
				),
			},
		},
	}

	if err := scenario.Execute(); err != nil {
		t.Fatalf("Scenario execution failed: %v", err)
	}

	c, err := LoadCassette(targetPath)
	if err != nil {
		t.Fatalf("LoadCassette target failed: %v", err)
	}

	if len(c.Interactions) != 1 {
		t.Fatalf("expected 1 interaction, got %d", len(c.Interactions))
	}

	resp := c.Interactions[0].Response
	if resp.Code != 429 {
		t.Errorf("expected status code 429, got %d", resp.Code)
	}
	wantBody := "{\n  \"error\": \"rate limited\"\n}"
	if resp.Body != wantBody {
		t.Errorf("expected body %q, got %q", wantBody, resp.Body)
	}
}

func TestInjectBeforeAction(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	baseYAML := `interactions:
- id: 1
  request:
    method: POST
    url: http://example.com/api
  response:
    code: 200
    body: 'success'
`
	basePath := filepath.Join(tempDir, "base.yaml")
	targetPath := filepath.Join(tempDir, "target.yaml")

	if err := os.WriteFile(basePath, []byte(baseYAML), 0o600); err != nil {
		t.Fatalf("error writing file: %v", err)
	}

	scenario := Scenario{
		BaseCassette:   basePath,
		TargetCassette: targetPath,
		Patches: []Patch{
			{
				Match: URLContains("/api"),
				Action: InjectBefore(
					Interaction{
						Response: InteractionResp{
							Code: 500,
							Body: "error",
						},
					},
				),
			},
		},
	}

	if err := scenario.Execute(); err != nil {
		t.Fatalf("Scenario execution failed: %v", err)
	}

	c, err := LoadCassette(targetPath)
	if err != nil {
		t.Fatalf("LoadCassette target failed: %v", err)
	}

	if len(c.Interactions) != 2 {
		t.Fatalf("expected 2 interactions, got %d", len(c.Interactions))
	}

	if c.Interactions[0].Response.Code != 500 || c.Interactions[0].Response.Body != "error" {
		t.Errorf("unexpected injected interaction: %+v", c.Interactions[0])
	}
	if c.Interactions[1].Response.Code != 200 || c.Interactions[1].Response.Body != "success" {
		t.Errorf("unexpected original interaction: %+v", c.Interactions[1])
	}
}

func TestMutateBody(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	baseYAML := `interactions:
- id: 1
  request:
    method: GET
    url: http://example.com/api
  response:
    code: 200
    body: 'hello'
`
	basePath := filepath.Join(tempDir, "base.yaml")
	targetPath := filepath.Join(tempDir, "target.yaml")

	if err := os.WriteFile(basePath, []byte(baseYAML), 0o600); err != nil {
		t.Fatalf("error writing file: %v", err)
	}

	scenario := Scenario{
		BaseCassette:   basePath,
		TargetCassette: targetPath,
		Patches: []Patch{
			{
				Match: URLContains("/api"),
				Action: MutateBody(func(body string) (string, error) {
					return body + " world", nil
				}),
			},
		},
	}

	if err := scenario.Execute(); err != nil {
		t.Fatalf("Scenario execution failed: %v", err)
	}

	c, err := LoadCassette(targetPath)
	if err != nil {
		t.Fatalf("LoadCassette target failed: %v", err)
	}

	if c.Interactions[0].Response.Body != "hello world" {
		t.Errorf("expected body 'hello world', got %q", c.Interactions[0].Response.Body)
	}
}

func TestMutateBodyError(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()

	baseYAML := `interactions:
- id: 1
  request:
    method: GET
    url: http://example.com/api
  response:
    code: 200
    body: 'hello'
`
	basePath := filepath.Join(tempDir, "base.yaml")
	targetPath := filepath.Join(tempDir, "target.yaml")

	if err := os.WriteFile(basePath, []byte(baseYAML), 0o600); err != nil {
		t.Fatalf("error writing file: %v", err)
	}

	scenario := Scenario{
		BaseCassette:   basePath,
		TargetCassette: targetPath,
		Patches: []Patch{
			{
				Match: URLContains("/api"),
				Action: MutateBody(func(body string) (string, error) {
					return "", errMutateTest
				}),
			},
		},
	}

	err := scenario.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestScenario_ValidatePatches(t *testing.T) {
	t.Parallel()

	scenarioNilMatch := Scenario{
		BaseCassette:   "base.yaml",
		TargetCassette: "target.yaml",
		Patches: []Patch{
			{
				Match:  nil,
				Action: ReplaceStatusCode(500),
			},
		},
	}
	if err := scenarioNilMatch.Execute(); err == nil || !errors.Is(err, ErrInvalidPatch) {
		t.Errorf("expected ErrInvalidPatch, got: %v", err)
	}

	scenarioNilAction := Scenario{
		BaseCassette:   "base.yaml",
		TargetCassette: "target.yaml",
		Patches: []Patch{
			{
				Match:  URLContains("/api"),
				Action: nil,
			},
		},
	}
	if err := scenarioNilAction.Execute(); err == nil || !errors.Is(err, ErrInvalidPatch) {
		t.Errorf("expected ErrInvalidPatch, got: %v", err)
	}
}
