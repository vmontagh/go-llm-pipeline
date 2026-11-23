package llmvcr_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
)

// ExampleNew replays a recorded call offline. Pass rec.HTTPClient() to a
// provider (for example ollama.WithHTTPClient) the same way. A request with
// no recording fails with ErrCassetteMiss instead of reaching the network.
func ExampleNew() {
	rec, err := llmvcr.New("testdata/cassettes/example_generate", llmvcr.ModeReplayOnly)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() {
		if err := rec.Stop(); err != nil {
			fmt.Println(err)
		}
	}()
	ctx := context.Background()
	client := rec.HTTPClient()

	body, err := send(ctx, client, http.MethodPost, "http://localhost:11434/api/generate", `{"model":"qwen3","prompt":"hi"}`)
	if err != nil {
		fmt.Println(err)
		return
	}
	var reply struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(reply.Response)

	_, err = send(ctx, client, http.MethodGet, "http://localhost:11434/api/tags", "")
	fmt.Println(errors.Is(err, llmvcr.ErrCassetteMiss))

	// Output:
	// Hello!
	// true
}

// send makes a request with client and returns the response body.
func send(ctx context.Context, client *http.Client, method, url, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	b, err := io.ReadAll(resp.Body)
	return b, errors.Join(err, resp.Body.Close())
}
