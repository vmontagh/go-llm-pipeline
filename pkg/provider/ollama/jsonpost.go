package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/httputil"
)

// errRequestNotSent marks a doJSONPost failure that occurred before the request
// reached the server (marshaling the body or building the request). Callers use
// errors.Is to treat these as terminal rather than a retryable transport error.
var errRequestNotSent = errors.New("request not sent")

// jsonPostRequest describes a single JSON POST exchange for doJSONPost. The
// context is passed separately as the first argument rather than stored here.
type jsonPostRequest struct {
	client *http.Client
	logger *slog.Logger
	host   string
	path   string
	body   any
}

// jsonPostResult carries the raw outcome of a JSON POST exchange for callers to
// interpret. statusCode is populated whenever a response was received, including
// when reading its body later failed.
type jsonPostResult struct {
	statusCode int
	body       []byte
	retryAfter time.Duration
}

// doJSONPost POSTs req.body (JSON-encoded) to req.host+req.path using req.client,
// reads and returns the full response body, status code, and parsed Retry-After,
// then closes the response body (logging any close error via req.logger). It does
// not interpret status codes; callers classify and shape their own results.
//
// A failure before the request is sent (marshaling body or building the
// request) is wrapped with errRequestNotSent so callers can treat it as
// terminal. A read failure returns the status code that was already received.
func doJSONPost(ctx context.Context, req jsonPostRequest) (jsonPostResult, error) {
	reqBytes, err := json.Marshal(req.body)
	if err != nil {
		return jsonPostResult{}, fmt.Errorf("marshaling request body: %w: %w", errRequestNotSent, err)
	}

	url := strings.TrimRight(req.host, "/") + req.path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return jsonPostResult{}, fmt.Errorf("creating request: %w: %w", errRequestNotSent, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := req.client.Do(httpReq)
	if err != nil {
		return jsonPostResult{}, fmt.Errorf("sending request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			req.logger.WarnContext(ctx, "close error", "err", cerr)
		}
	}()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return jsonPostResult{statusCode: resp.StatusCode}, fmt.Errorf("reading response body: %w", err)
	}

	return jsonPostResult{
		statusCode: resp.StatusCode,
		body:       bodyBytes,
		retryAfter: httputil.ParseRetryAfter(resp.Header.Get("Retry-After")),
	}, nil
}
