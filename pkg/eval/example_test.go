package eval_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/vmontagh/go-llm-pipeline/pkg/eval"
)

type caption struct {
	Text string `json:"text"`
}

func ExampleRun() {
	examples := []eval.Example[string, caption]{
		{Name: "cat", Input: "cat", Want: caption{Text: "CAT"}},
		{Name: "dog", Input: "dog", Want: caption{Text: "DOG!"}},
	}
	// In a real test, target calls the pipeline through a cassette replay client.
	target := func(_ context.Context, in string) (caption, error) {
		return caption{Text: strings.ToUpper(in)}, nil
	}

	report, err := eval.Run(context.Background(), examples, target, eval.FieldMatch[caption]{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(report.PassRate, report.Check(0.5))
	// Output: 0.5 <nil>
}
