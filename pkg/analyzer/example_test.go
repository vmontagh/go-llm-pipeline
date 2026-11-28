package analyzer_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vmontagh/go-llm-pipeline/pkg/analyzer"
	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider"
	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
)

// The examples replay recorded Ollama replies from testdata/cassettes, so they
// run offline: no request reaches this host.
const (
	exampleHost  = "http://localhost:11434"
	exampleModel = "qwen3"
)

// replayProvider returns an Ollama provider whose calls are served by rec.
func replayProvider(rec *llmvcr.Recorder) provider.Provider[struct{}] {
	return ollama.NewProvider(exampleHost, exampleModel, ollama.WithHTTPClient(rec.HTTPClient()))
}

func stop(rec *llmvcr.Recorder) {
	if err := rec.Stop(); err != nil {
		fmt.Println(err)
	}
}

type caption struct {
	Heading string   `json:"heading"`
	Tags    []string `json:"tags"`
}

func parseCaption(s string) (caption, error) {
	var c caption
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return caption{}, fmt.Errorf("parsing caption: %w", err)
	}
	return c, nil
}

// ExampleNewBuilder builds an analyzer on a local Ollama model and parses its
// JSON reply into a Go type.
func ExampleNewBuilder() {
	rec, err := llmvcr.New("testdata/cassettes/example_analyze", llmvcr.ModeReplayOnly)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stop(rec)
	p := replayProvider(rec)

	a, err := analyzer.NewBuilder(p, parseCaption).Build()
	if err != nil {
		fmt.Println(err)
		return
	}
	c, err := a.Analyze(context.Background(), "Caption this photo of a beach as JSON.", nil, "job-1", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(c.Heading, c.Tags)

	// Output:
	// Sandy Beach [beach sea]
}

// ExampleExtract decodes the reply into a struct and checks it against the
// schema derived from the struct. The first reply lacks the required "born"
// field, so Extract sends the error back and the model corrects itself.
func ExampleExtract() {
	rec, err := llmvcr.New("testdata/cassettes/example_extract", llmvcr.ModeReplayOnly)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stop(rec)
	p := replayProvider(rec)
	a, err := analyzer.NewTextBuilder(p).Build()
	if err != nil {
		fmt.Println(err)
		return
	}

	type person struct {
		Name string `json:"name"`
		Born int    `json:"born"`
	}
	got, err := analyzer.Extract[person](context.Background(), a, analyzer.ExtractRequest{
		Prompt: "Who wrote the first published computer program?",
		JobID:  "job-1",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(got.Name, got.Born)

	// Output:
	// Ada Lovelace 1815
}

// ExampleTemplate_AnalyzeWithTools lets the model call a typed Go function
// before it answers.
func ExampleTemplate_AnalyzeWithTools() {
	rec, err := llmvcr.New("testdata/cassettes/example_tools", llmvcr.ModeReplayOnly)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer stop(rec)
	p := replayProvider(rec)

	type sum struct {
		Sum int `json:"sum"`
	}
	a, err := analyzer.NewBuilder(p, func(s string) (sum, error) {
		var v sum
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			return sum{}, fmt.Errorf("parsing sum: %w", err)
		}
		return v, nil
	}).Build()
	if err != nil {
		fmt.Println(err)
		return
	}

	type addArgs struct {
		A int `json:"a"`
		B int `json:"b"`
	}
	add, err := analyzer.NewTool("add", "Adds two integers", func(_ context.Context, args addArgs) (int, error) {
		fmt.Printf("add(%d, %d)\n", args.A, args.B)
		return args.A + args.B, nil
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	msgs := []provider.Message{provider.TextMessage(provider.RoleUser, `Use the add tool to add 2 and 3. Answer as {"sum": n}.`)}
	got, err := a.AnalyzeWithTools(context.Background(), msgs, analyzer.ToolLoop{Tools: []analyzer.Tool{add}}, "job-1", nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(got.Sum)

	// Output:
	// add(2, 3)
	// 5
}

// ExampleSchemaOf derives the JSON schema that Extract checks replies
// against. Fields are required unless they are pointers or omitempty.
func ExampleSchemaOf() {
	type photo struct {
		Heading string   `json:"heading" jsonschema:"description=Short title"`
		Tags    []string `json:"tags,omitempty"`
	}
	schema, err := analyzer.SchemaOf(photo{})
	if err != nil {
		fmt.Println(err)
		return
	}
	b, err := json.Marshal(schema)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(string(b))

	// Output:
	// {"additionalProperties":false,"properties":{"heading":{"description":"Short title","type":"string"},"tags":{"items":{"type":"string"},"type":"array"}},"required":["heading"],"type":"object"}
}
