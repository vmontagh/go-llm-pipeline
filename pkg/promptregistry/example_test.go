package promptregistry_test

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"

	"github.com/vmontagh/go-llm-pipeline/pkg/promptregistry"
)

//go:embed testdata/templates testdata/overlays
var templatesFS embed.FS

// Example shows the intended usage: prompts are embedded as .tmpl files and
// rendered offline by a stable (name, version) pair.
func Example() {
	sub, err := fs.Sub(templatesFS, "testdata/templates")
	if err != nil {
		log.Fatal(err)
	}
	reg, err := promptregistry.New(sub)
	if err != nil {
		log.Fatal(err)
	}

	out, err := reg.Render(context.Background(), "summarize", "v1", map[string]any{
		"MaxWords": 20,
		"Body":     "Hello world.",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Print(out)
	// Output:
	// Summarize the text below in 20 words or fewer.
	//
	// Hello world.
}

// ExampleWithOverlay renders learned rules on top of a base template without
// editing it, and records which template and rules produced the prompt.
func ExampleWithOverlay() {
	sub, err := fs.Sub(templatesFS, "testdata/templates")
	if err != nil {
		log.Fatal(err)
	}
	reg, err := promptregistry.New(sub)
	if err != nil {
		log.Fatal(err)
	}
	rules, err := promptregistry.LoadOverlay(templatesFS, "testdata/overlays/photo-rules.yaml")
	if err != nil {
		log.Fatal(err)
	}

	ref := promptregistry.Ref{Name: "judge", Version: "v1"}
	out, err := reg.Render(context.Background(), ref.Name, ref.Version,
		map[string]any{"Title": "Sunset over the bay"},
		promptregistry.WithOverlay(rules), promptregistry.WithOverlayLimit(4096))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(promptregistry.VersionID(ref, rules))
	fmt.Print(out)
	// Output:
	// judge/v1+photo-rules@7#d3c48e706130
	// Decide whether to keep the photo "Sunset over the bay".
	//
	// Follow these guidelines:
	// - Prefer the sharpest frame of a burst.
	// - Keep photos with people looking at the camera.
	//
	// Reject the photo if:
	// - The main subject is out of focus.
	//
	// Answer "keep" or "reject".
}
