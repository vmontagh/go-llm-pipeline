package promptregistry_test

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log"

	"github.com/vmontagh/go-llm-pipeline/pkg/promptregistry"
)

//go:embed testdata/templates
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
