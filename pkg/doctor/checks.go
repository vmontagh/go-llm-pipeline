package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/vmontagh/go-llm-pipeline/pkg/provider/ollama"
)

// Check names surfaced in Result.Name and log output.
const (
	nameFileDescriptors = "file descriptors"
	nameOpenAILatency   = "openai latency"
	nameGeminiLatency   = "gemini latency"
	nameOpenAIKey       = "openai api key"
	nameGeminiKey       = "gemini api key"
	nameOllama          = "ollama"
)

// API paths and headers used by the network checks.
const (
	modelsPath          = "/models"
	ollamaTagsPath      = "/api/tags"
	authorizationHeader = "Authorization"
	bearerPrefix        = "Bearer "
	maxProbeBodyBytes   = 1 << 20
)

// httpProbe is the outcome of a single HTTP request, decoupled from the clock
// and network so classification logic can be unit-tested directly.
type httpProbe struct {
	status  int
	elapsed time.Duration
	body    []byte
	err     error
}

// probe performs a GET against rawURL and returns the observed httpProbe. It
// never returns the URL or headers in the result, so callers may pass secrets
// in the query string or headers without leaking them.
func (r *Runner) probe(ctx context.Context, rawURL string, header http.Header) httpProbe {
	reqCtx := ctx
	if r.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		reqCtx, cancel = context.WithTimeout(ctx, r.cfg.Timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return httpProbe{err: fmt.Errorf("creating request: %w", err)}
	}
	for key, values := range header {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}

	start := time.Now()
	resp, err := r.client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return httpProbe{elapsed: elapsed, err: err}
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			r.logger.Debug("doctor: response close error", "err", cerr)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))
	if err != nil {
		return httpProbe{status: resp.StatusCode, elapsed: elapsed, err: fmt.Errorf("reading response: %w", err)}
	}
	return httpProbe{status: resp.StatusCode, elapsed: elapsed, body: body}
}

// checkFileDescriptors verifies the process open-file soft limit against the
// configured thresholds.
func (r *Runner) checkFileDescriptors(_ context.Context) Result {
	soft, hard, err := r.fdReader.ReadFileLimit()
	if err != nil {
		return Result{
			Name:   nameFileDescriptors,
			Status: StatusSkip,
			Detail: "file-descriptor limit unavailable on this platform",
		}
	}

	status, detail := classifyFileLimit(soft, r.cfg.FileLimitMin, r.cfg.FileLimitWarn)
	res := Result{
		Name:   nameFileDescriptors,
		Status: status,
		Detail: detail,
		Attrs: []slog.Attr{
			slog.Uint64("soft", soft),
			slog.Uint64("hard", hard),
		},
	}
	if status != StatusPass {
		res.Remediation = fmt.Sprintf("raise the open-file limit (for example, ulimit -n %d)", r.cfg.FileLimitWarn)
	}
	return res
}

// classifyFileLimit maps a soft limit to a Status against the min/warn floors.
func classifyFileLimit(soft, minLimit, warnLimit uint64) (status Status, detail string) {
	switch {
	case soft < minLimit:
		return StatusFail, fmt.Sprintf("soft limit %d is below the minimum %d", soft, minLimit)
	case soft < warnLimit:
		return StatusWarn, fmt.Sprintf("soft limit %d is below the recommended %d", soft, warnLimit)
	default:
		return StatusPass, fmt.Sprintf("soft limit %d meets the recommended %d", soft, warnLimit)
	}
}

// checkOpenAILatency probes OpenAI reachability without authenticating.
func (r *Runner) checkOpenAILatency(ctx context.Context) Result {
	p := r.probe(ctx, r.cfg.OpenAIBaseURL+modelsPath, nil)
	return latencyResult(nameOpenAILatency, p, r.cfg.OpenAIAPIKey != "", r.cfg.LatencyWarn)
}

// checkGeminiLatency probes Gemini reachability without authenticating.
func (r *Runner) checkGeminiLatency(ctx context.Context) Result {
	p := r.probe(ctx, r.cfg.GeminiBaseURL+modelsPath, nil)
	return latencyResult(nameGeminiLatency, p, r.cfg.GeminiAPIKey != "", r.cfg.LatencyWarn)
}

// latencyResult builds a Result from a reachability probe.
func latencyResult(name string, p httpProbe, keyConfigured bool, warn time.Duration) Result {
	status, detail := classifyLatency(p, keyConfigured, warn)
	res := Result{
		Name:     name,
		Status:   status,
		Detail:   detail,
		Duration: p.elapsed,
	}
	if p.err != nil {
		res.Attrs = append(res.Attrs, slog.String("err", p.err.Error()))
		res.Remediation = "check network connectivity and any proxy or firewall rules"
	} else {
		res.Attrs = append(res.Attrs, slog.Int("status", p.status))
	}
	return res
}

// classifyLatency maps a reachability probe to a Status. Any HTTP response —
// including 401/403 — proves the network path works; authentication is graded
// separately by the key checks.
func classifyLatency(p httpProbe, keyConfigured bool, warn time.Duration) (status Status, detail string) {
	if p.err != nil {
		if keyConfigured {
			return StatusFail, "endpoint unreachable and a key is configured"
		}
		return StatusWarn, "endpoint unreachable"
	}
	if warn > 0 && p.elapsed > warn {
		return StatusWarn, fmt.Sprintf("reachable but slow (%s)", p.elapsed.Round(time.Millisecond))
	}
	return StatusPass, "reachable"
}

// checkOpenAIKey validates the OpenAI credential against the models endpoint.
func (r *Runner) checkOpenAIKey(ctx context.Context) Result {
	if r.cfg.OpenAIAPIKey == "" {
		return skippedKeyResult(nameOpenAIKey, "OpenAI API key")
	}
	header := http.Header{}
	header.Set(authorizationHeader, bearerPrefix+r.cfg.OpenAIAPIKey)
	p := r.probe(ctx, r.cfg.OpenAIBaseURL+modelsPath, header)
	return keyResult(nameOpenAIKey, p)
}

// checkGeminiKey validates the Gemini credential against the models endpoint.
func (r *Runner) checkGeminiKey(ctx context.Context) Result {
	if r.cfg.GeminiAPIKey == "" {
		return skippedKeyResult(nameGeminiKey, "Gemini API key")
	}
	// The key travels as a query parameter; probe never surfaces the URL.
	p := r.probe(ctx, r.cfg.GeminiBaseURL+modelsPath+"?key="+r.cfg.GeminiAPIKey, nil)
	return keyResult(nameGeminiKey, p)
}

// skippedKeyResult builds the SKIP Result used when a credential is absent.
func skippedKeyResult(name, label string) Result {
	return Result{
		Name:        name,
		Status:      StatusSkip,
		Detail:      label + " is not configured",
		Remediation: "set the credential to validate it",
	}
}

// keyResult builds a Result from an authenticated probe.
func keyResult(name string, p httpProbe) Result {
	status, detail := classifyKey(p)
	res := Result{Name: name, Status: status, Detail: detail, Duration: p.elapsed}
	if p.err != nil {
		res.Attrs = append(res.Attrs, slog.String("err", p.err.Error()))
	} else {
		res.Attrs = append(res.Attrs, slog.Int("status", p.status))
	}
	return res
}

// classifyKey grades an authenticated probe. A network error is a warning
// because the credential itself was neither accepted nor rejected.
func classifyKey(p httpProbe) (status Status, detail string) {
	if p.err != nil {
		return StatusWarn, "could not reach the provider to validate the key"
	}
	switch {
	case p.status == http.StatusOK:
		return StatusPass, "key accepted"
	case p.status == http.StatusUnauthorized, p.status == http.StatusForbidden:
		return StatusFail, "key rejected"
	case p.status == http.StatusTooManyRequests:
		return StatusWarn, "key reached the provider but is rate-limited"
	case p.status >= http.StatusInternalServerError:
		return StatusWarn, "provider returned a server error"
	default:
		return StatusFail, fmt.Sprintf("unexpected status %d", p.status)
	}
}

// ollamaTagsResponse is the subset of the /api/tags payload the probe needs.
type ollamaTagsResponse struct {
	Models []ollamaModel `json:"models"`
}

type ollamaModel struct {
	Name string `json:"name"`
}

// checkOllama probes the local Ollama instance and lists loaded models. When
// Config.OllamaModel is set it also asserts that model is present and runs the
// Ollama context-window preflight against it.
func (r *Runner) checkOllama(ctx context.Context) Result {
	p := r.probe(ctx, strings.TrimRight(r.cfg.OllamaHost, "/")+ollamaTagsPath, nil)
	if p.err != nil {
		return Result{
			Name:        nameOllama,
			Status:      StatusWarn,
			Detail:      "local Ollama instance unreachable",
			Duration:    p.elapsed,
			Remediation: "start Ollama or unset the Ollama host if unused",
			Attrs:       []slog.Attr{slog.String("err", p.err.Error())},
		}
	}
	if p.status != http.StatusOK {
		return Result{
			Name:     nameOllama,
			Status:   StatusWarn,
			Detail:   "Ollama returned an unexpected status",
			Duration: p.elapsed,
			Attrs:    []slog.Attr{slog.Int("status", p.status)},
		}
	}

	var tags ollamaTagsResponse
	if err := json.Unmarshal(p.body, &tags); err != nil {
		return Result{
			Name:     nameOllama,
			Status:   StatusFail,
			Detail:   "Ollama returned a malformed /api/tags response",
			Duration: p.elapsed,
			Attrs:    []slog.Attr{slog.String("err", err.Error())},
		}
	}

	models := make([]string, 0, len(tags.Models))
	for _, m := range tags.Models {
		models = append(models, m.Name)
	}

	if len(models) == 0 {
		return Result{
			Name:        nameOllama,
			Status:      StatusWarn,
			Detail:      "Ollama is reachable but no models are loaded",
			Duration:    p.elapsed,
			Remediation: "pull a model (for example, ollama pull llama3)",
		}
	}

	res := Result{
		Name:     nameOllama,
		Status:   StatusPass,
		Detail:   fmt.Sprintf("reachable with %d model(s) loaded", len(models)),
		Duration: p.elapsed,
		Attrs:    []slog.Attr{slog.String("models", strings.Join(models, ","))},
	}

	if r.cfg.OllamaModel == "" {
		return res
	}
	if !slices.Contains(models, r.cfg.OllamaModel) {
		return Result{
			Name:        nameOllama,
			Status:      StatusFail,
			Detail:      fmt.Sprintf("requested model %q is not loaded", r.cfg.OllamaModel),
			Duration:    p.elapsed,
			Remediation: fmt.Sprintf("pull the model (ollama pull %s)", r.cfg.OllamaModel),
			Attrs:       []slog.Attr{slog.String("models", strings.Join(models, ","))},
		}
	}

	if err := r.ollamaPreflight(ctx); err != nil {
		res.Status = StatusWarn
		res.Detail = fmt.Sprintf("model %q is loaded but preflight reported an issue", r.cfg.OllamaModel)
		res.Attrs = append(res.Attrs, slog.String("err", err.Error()))
	}
	return res
}

// ollamaPreflight reuses the Ollama provider's context-window preflight for the
// configured model. It is best-effort: providers without a Preflight method are
// treated as a pass.
func (r *Runner) ollamaPreflight(ctx context.Context) error {
	p := ollama.NewProvider(
		r.cfg.OllamaHost,
		r.cfg.OllamaModel,
		ollama.WithHTTPClient(r.client),
		ollama.WithLogger(r.logger),
	)
	pf, ok := p.(interface {
		Preflight(context.Context) error
	})
	if !ok {
		return nil
	}
	return pf.Preflight(ctx)
}
