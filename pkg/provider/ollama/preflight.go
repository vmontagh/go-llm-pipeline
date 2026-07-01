package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const minPromptTokenBudget = 1000
const responseTokenReserveRatio = 0.3

var ErrContextWindowTooSmall = errors.New("ollama preflight: context window too small for prompt budget")
var ErrContextLengthMissing = errors.New("ollama preflight: model_info has no .context_length key")
var ErrUnsupportedNumericType = errors.New("unsupported numeric type")
var ErrContextLengthUnsupportedType = errors.New("ollama preflight: .context_length value is not numeric")

type showRequest struct {
	Model string `json:"model"`
}

type showResponse struct {
	ModelInfo map[string]any `json:"model_info"`
}

func (t *transport) Preflight(ctx context.Context) error {
	body, err := json.Marshal(showRequest{Model: t.model})
	if err != nil {
		return fmt.Errorf("ollama preflight: marshaling /api/show request: %w", err)
	}

	url := strings.TrimRight(t.host, "/") + "/api/show"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("ollama preflight: creating /api/show request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		t.logger.WarnContext(ctx, "ollama preflight: skipping context-window check (cannot reach /api/show)", "model", t.model, "err", err)
		return nil
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.logger.WarnContext(ctx, "ollama preflight: close error", "err", cerr)
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.logger.WarnContext(ctx, "ollama preflight: skipping context-window check (cannot read /api/show body)", "model", t.model, "err", err)
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		t.logger.WarnContext(ctx, "ollama preflight: skipping context-window check (/api/show returned non-200)", "model", t.model, "status", resp.StatusCode, "body", strings.TrimSpace(string(respBody)))
		return nil
	}

	var sr showResponse
	if err := json.Unmarshal(respBody, &sr); err != nil {
		return fmt.Errorf("ollama preflight: parsing /api/show response: %w", err)
	}

	ctxLen, err := extractContextLength(sr.ModelInfo)
	if err != nil {
		t.logger.WarnContext(ctx, "ollama preflight: context length unavailable; skipping budget check", "model", t.model, "err", err)
		return nil
	}
	t.contextLength = ctxLen

	effective := ctxLen
	if t.numCtx > 0 && t.numCtx < ctxLen {
		effective = t.numCtx
	}
	promptBudget := int(float64(effective) * (1 - responseTokenReserveRatio))
	if promptBudget < minPromptTokenBudget {
		return fmt.Errorf(
			"%w: model %q has a context window of %d tokens; after reserving space for the response, only ~%d tokens remain for the prompt (need at least %d); consider using a model with a larger context window or setting ollama.num_ctx in config.yaml",
			ErrContextWindowTooSmall, t.model, effective, promptBudget, minPromptTokenBudget,
		)
	}
	t.logger.DebugContext(ctx, "ollama preflight", "model", t.model, "context_length", effective, "prompt_budget", promptBudget)
	return nil
}

func extractContextLength(modelInfo map[string]any) (int, error) {
	for k, v := range modelInfo {
		if !strings.HasSuffix(k, ".context_length") {
			continue
		}
		n, err := toInt(v)
		if err != nil {
			return 0, fmt.Errorf("%w: %q has value %T", ErrContextLengthUnsupportedType, k, v)
		}
		return n, nil
	}
	return 0, ErrContextLengthMissing
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case float64:
		return int(n), nil
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, err
		}
		return int(i), nil
	default:
		return 0, fmt.Errorf("%w: %T", ErrUnsupportedNumericType, v)
	}
}
