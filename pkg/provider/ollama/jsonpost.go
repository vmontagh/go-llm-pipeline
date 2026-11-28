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
	resp, err := sendJSON(ctx, req)
	if err != nil {
		return jsonPostResult{}, err
	}
	return readJSONResult(ctx, req.logger, resp)
}

// sendJSON POSTs req.body (JSON-encoded) and returns the response unread.
// The caller must close its body.
func sendJSON(ctx context.Context, req jsonPostRequest) (*http.Response, error) {
	reqBytes, err := json.Marshal(req.body)
	if err != nil {
		return nil, fmt.Errorf("%w: marshaling request body: %w", errRequestNotSent, err)
	}

	url := strings.TrimRight(req.host, "/") + req.path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: creating request: %w", errRequestNotSent, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := req.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	return resp, nil
}

// readJSONResult reads and closes resp.
func readJSONResult(ctx context.Context, logger *slog.Logger, resp *http.Response) (jsonPostResult, error) {
	defer closeBody(ctx, logger, resp.Body)

	// Parse the cooldown first so a failed body read still honors it.
	res := jsonPostResult{
		statusCode: resp.StatusCode,
		retryAfter: httputil.ParseRetryAfter(resp.Header.Get(httputil.RetryAfterHeader)),
	}
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return res, fmt.Errorf("reading response body: %w", err)
	}
	res.body = bodyBytes
	return res, nil
}

func closeBody(ctx context.Context, logger *slog.Logger, body io.Closer) {
	if err := body.Close(); err != nil {
		logger.WarnContext(ctx, "close error", "err", err)
	}
}
