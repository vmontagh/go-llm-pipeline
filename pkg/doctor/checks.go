package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	modelsPath          = "models"
	ollamaTagsPath      = "api/tags"
	authorizationHeader = "Authorization"
	bearerPrefix        = "Bearer "
	//nolint:gosec // G101: this is an HTTP header name, not a credential value.
	geminiAPIKeyHeader = "x-goog-api-key"
	maxProbeBodyBytes  = 1 << 20
)

// httpProbe is the outcome of a single HTTP request, decoupled from the clock
// and network so classification logic can be unit-tested directly.
type httpProbe struct {
	status  int
	elapsed time.Duration
	body    []byte
	err     error
}

// buildURL joins base and path using net/url so trailing slashes and path
// escaping are handled correctly instead of by fragile string concatenation.
func buildURL(base, path string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parsing base url: %w", err)
	}
	return u.JoinPath(path).String(), nil
}

// probe performs a GET against base+path with the given headers and returns the
// observed httpProbe. Secrets must be passed via header, never baked into base
// or path; the result never surfaces the built URL, so a transport error cannot
// leak a credential.
func (r *Runner) probe(ctx context.Context, base, path string, header http.Header) httpProbe {
	rawURL, err := buildURL(base, path)
	if err != nil {
		return httpProbe{err: err}
	}

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
	resp, err := r.clientFor(header).Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return httpProbe{elapsed: elapsed, err: sanitizeURLError(err)}
	}

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxProbeBodyBytes))
	closeErr := resp.Body.Close()
	switch {
	case readErr != nil:
		return httpProbe{status: resp.StatusCode, elapsed: elapsed, err: fmt.Errorf("reading response: %w", readErr)}
	case closeErr != nil:
		return httpProbe{status: resp.StatusCode, elapsed: elapsed, err: fmt.Errorf("closing response body: %w", closeErr)}
	default:
		return httpProbe{status: resp.StatusCode, elapsed: elapsed, body: body}
	}
}

// clientFor returns the *http.Client to use for a probe. Requests that carry
// credentials (a non-empty header) get a shallow copy of the injected client
// with redirect following disabled: net/http forwards custom headers such as
// Authorization and x-goog-api-key across redirects — including cross-origin
// ones — so following a redirect could disclose the credential to the redirect
// target, and a redirect landing on an unrelated 200 page could be misread as
// "key accepted". Disabling redirects returns the original 3xx response so it
// is classified honestly. The copy leaves the caller's client unmutated.
func (r *Runner) clientFor(header http.Header) *http.Client {
	if len(header) == 0 {
		return r.client
	}
	c := *r.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &c
}

// sanitizeURLError strips the URL from a *url.Error so a credential carried in
// the request URL can never reach logs. It is defense in depth: the checks pass
// secrets via headers, but a future caller might not.
func sanitizeURLError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("%s: %w", uerr.Op, uerr.Err)
	}
	return err
}

// checkFileDescriptors verifies the process open-file soft limit against the
// configured thresholds.
func (r *Runner) checkFileDescriptors(_ context.Context) Result {
	soft, hard, err := r.fdReader.ReadFileLimit()
	if err != nil {
		if errors.Is(err, ErrFileLimitUnsupported) {
			return Result{
				Check:  CheckFileDescriptors,
				Name:   nameFileDescriptors,
				Status: StatusSkip,
				Detail: "file-descriptor limit unavailable on this platform",
			}
		}
		return Result{
			Check:       CheckFileDescriptors,
			Name:        nameFileDescriptors,
			Status:      StatusWarn,
			Detail:      "could not read the file-descriptor limit",
			Remediation: "inspect the host's rlimit configuration",
			Attrs:       []slog.Attr{slog.String("err", err.Error())},
		}
	}

	status, detail := classifyFileLimit(soft, r.cfg.FileLimitMin, r.cfg.FileLimitWarn)
	res := Result{
		Check:  CheckFileDescriptors,
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
	p := r.probe(ctx, r.cfg.OpenAIBaseURL, modelsPath, nil)
	return latencyResult(CheckOpenAILatency, nameOpenAILatency, p, r.cfg.OpenAIAPIKey != "", r.cfg.LatencyWarn)
}

// checkGeminiLatency probes Gemini reachability without authenticating.
func (r *Runner) checkGeminiLatency(ctx context.Context) Result {
	p := r.probe(ctx, r.cfg.GeminiBaseURL, modelsPath, nil)
	return latencyResult(CheckGeminiLatency, nameGeminiLatency, p, r.cfg.GeminiAPIKey != "", r.cfg.LatencyWarn)
}

// latencyResult builds a Result from a reachability probe.
func latencyResult(check, name string, p httpProbe, keyConfigured bool, warn time.Duration) Result {
	status, detail := classifyLatency(p, keyConfigured, warn)
	res := Result{
		Check:    check,
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
		if status == StatusWarn {
			res.Remediation = "investigate network latency to the provider or a nearby region"
		}
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
		return skippedKeyResult(CheckOpenAIKey, nameOpenAIKey, "OpenAI API key")
	}
	header := http.Header{}
	header.Set(authorizationHeader, bearerPrefix+r.cfg.OpenAIAPIKey)
	p := r.probe(ctx, r.cfg.OpenAIBaseURL, modelsPath, header)
	return keyResult(CheckOpenAIKey, nameOpenAIKey, p)
}

// checkGeminiKey validates the Gemini credential against the models endpoint.
// The key travels in the x-goog-api-key header (not the URL query) so it can
// never appear in a request URL, and therefore never in an error string.
func (r *Runner) checkGeminiKey(ctx context.Context) Result {
	if r.cfg.GeminiAPIKey == "" {
		return skippedKeyResult(CheckGeminiKey, nameGeminiKey, "Gemini API key")
	}
	header := http.Header{}
	header.Set(geminiAPIKeyHeader, r.cfg.GeminiAPIKey)
	p := r.probe(ctx, r.cfg.GeminiBaseURL, modelsPath, header)
	return keyResult(CheckGeminiKey, nameGeminiKey, p)
}

// skippedKeyResult builds the SKIP Result used when a credential is absent.
func skippedKeyResult(check, name, label string) Result {
	return Result{
		Check:       check,
		Name:        name,
		Status:      StatusSkip,
		Detail:      label + " is not configured",
		Remediation: "set the credential to validate it",
	}
}

// keyResult builds a Result from an authenticated probe.
func keyResult(check, name string, p httpProbe) Result {
	status, detail := classifyKey(p)
	res := Result{Check: check, Name: name, Status: status, Detail: detail, Duration: p.elapsed}
	if p.err != nil {
		res.Attrs = append(res.Attrs, slog.String("err", p.err.Error()))
	} else {
		res.Attrs = append(res.Attrs, slog.Int("status", p.status))
	}
	if status == StatusFail {
		res.Remediation = "verify the API key value and that it has access to this provider"
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
	case p.status >= http.StatusMultipleChoices && p.status < http.StatusBadRequest:
		return StatusFail, fmt.Sprintf("provider redirected the credentialed request (status %d); refusing to forward the key", p.status)
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

// checkOllama probes the local Ollama instance and lists installed models. When
// Config.OllamaModel is set it also asserts that model is installed and runs the
// Ollama context-window preflight against it. An empty OllamaHost skips the
// check entirely.
func (r *Runner) checkOllama(ctx context.Context) Result {
	if r.cfg.OllamaHost == "" {
		return Result{
			Check:  CheckOllama,
			Name:   nameOllama,
			Status: StatusSkip,
			Detail: "Ollama host is not configured",
		}
	}

	p := r.probe(ctx, r.cfg.OllamaHost, ollamaTagsPath, nil)
	if p.err != nil {
		return Result{
			Check:       CheckOllama,
			Name:        nameOllama,
			Status:      StatusWarn,
			Detail:      "local Ollama instance unreachable",
			Duration:    p.elapsed,
			Remediation: "start Ollama or clear the Ollama host if unused",
			Attrs:       []slog.Attr{slog.String("err", p.err.Error())},
		}
	}
	if p.status != http.StatusOK {
		return Result{
			Check:    CheckOllama,
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
			Check:    CheckOllama,
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
			Check:       CheckOllama,
			Name:        nameOllama,
			Status:      StatusWarn,
			Detail:      "Ollama is reachable but no models are installed",
			Duration:    p.elapsed,
			Remediation: "pull a model (for example, ollama pull llama3)",
		}
	}

	res := Result{
		Check:    CheckOllama,
		Name:     nameOllama,
		Status:   StatusPass,
		Detail:   fmt.Sprintf("reachable with %d model(s) installed", len(models)),
		Duration: p.elapsed,
		Attrs:    []slog.Attr{slog.String("models", strings.Join(models, ","))},
	}

	if r.cfg.OllamaModel == "" {
		return res
	}
	if !modelInstalled(models, r.cfg.OllamaModel) {
		return Result{
			Check:       CheckOllama,
			Name:        nameOllama,
			Status:      StatusFail,
			Detail:      fmt.Sprintf("requested model %q is not installed", r.cfg.OllamaModel),
			Duration:    p.elapsed,
			Remediation: fmt.Sprintf("pull the model (ollama pull %s)", r.cfg.OllamaModel),
			Attrs:       []slog.Attr{slog.String("models", strings.Join(models, ","))},
		}
	}

	verified, err := r.ollamaPreflight(ctx)
	switch {
	case err != nil:
		res.Status = StatusWarn
		res.Detail = fmt.Sprintf("model %q is installed but its context-window preflight failed", r.cfg.OllamaModel)
		res.Remediation = "review the model's context window or set ollama.num_ctx"
		res.Attrs = append(res.Attrs, slog.String("err", err.Error()))
	case !verified:
		res.Status = StatusWarn
		res.Detail = fmt.Sprintf("model %q is installed but its context window could not be verified", r.cfg.OllamaModel)
		res.Remediation = "ensure Ollama's /api/show endpoint is reachable so the context window can be verified"
	}
	return res
}

// modelInstalled reports whether want matches an installed model. Ollama
// resolves a request without a tag to that name's ":latest" tag, so a tagless
// want matches an installed model only by its exact name or its ":latest" form
// — not by an arbitrary tag. Requesting "llama3" is therefore satisfied by
// "llama3" or "llama3:latest", but not by "llama3:70b" (which Ollama would not
// resolve "llama3" to). A want that already carries a tag matches exactly.
func modelInstalled(models []string, want string) bool {
	wantLatest := want
	if !strings.Contains(want, ":") {
		wantLatest = want + ":latest"
	}
	for _, m := range models {
		if m == want || m == wantLatest {
			return true
		}
	}
	return false
}

// ollamaPreflight reuses the Ollama provider's context-window preflight for the
// configured model. The provider's Preflight is best-effort: it returns nil not
// only on success but also when it cannot complete the check (for example
// /api/show is unreachable, returns non-200, or omits the context length), in
// which case it logs a warning. ollamaPreflight injects a recording logger so
// it can distinguish a verified context window (verified=true) from a check it
// had to skip (verified=false), letting the caller warn instead of silently
// passing. A non-nil error means the preflight ran and found a real problem
// (for example a context window too small for the prompt budget).
func (r *Runner) ollamaPreflight(ctx context.Context) (verified bool, err error) {
	if r.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.cfg.Timeout)
		defer cancel()
	}
	var skipped bool
	p := ollama.NewProvider(
		r.cfg.OllamaHost,
		r.cfg.OllamaModel,
		ollama.WithHTTPClient(r.client),
		ollama.WithLogger(slog.New(&warnRecorder{skipped: &skipped})),
	)
	pf, ok := p.(interface {
		Preflight(context.Context) error
	})
	if !ok {
		return true, nil
	}
	if err := pf.Preflight(ctx); err != nil {
		return false, err
	}
	return !skipped, nil
}

// warnRecorder is a slog.Handler that records whether the Ollama provider
// emitted a warning while running Preflight. The provider logs at WARN on every
// path where it abandons the context-window check but still returns nil, so a
// recorded warning marks the check as skipped rather than verified.
type warnRecorder struct {
	skipped *bool
}

func (h *warnRecorder) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}

// Handle records a warning; the slog.Handler contract fixes the by-value
// slog.Record parameter, so the hugeParam hint does not apply.
//
//nolint:gocritic // slog.Handler.Handle requires slog.Record by value.
func (h *warnRecorder) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level >= slog.LevelWarn {
		*h.skipped = true
	}
	return nil
}

func (h *warnRecorder) WithAttrs(_ []slog.Attr) slog.Handler { return h }

func (h *warnRecorder) WithGroup(_ string) slog.Handler { return h }
