package llmvcr_test

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
)

const (
	// cassetteFormatVersion is the go-vcr v3 on-disk format version; the
	// recorder refuses to replay a cassette with any other version.
	cassetteFormatVersion = 2
	cassetteExt           = ".yaml"
	cannedText            = `{"tags":["cat"]}`
	jsonContentType       = "application/json"
	// longURLPathLen pushes a URL past the inspect column width so it is
	// truncated.
	longURLPathLen = 120
)

// saveCassette writes interactions to dir/name.yaml and returns the path
// without the extension, as llmvcr.New expects.
func saveCassette(t *testing.T, dir, name string, interactions ...llmvcr.Interaction) string {
	t.Helper()
	base := filepath.Join(dir, name)
	c := &llmvcr.Cassette{Version: cassetteFormatVersion, Interactions: interactions}
	if err := llmvcr.SaveCassette(base+cassetteExt, c); err != nil {
		t.Fatalf("SaveCassette(%q) = %v", base+cassetteExt, err)
	}
	return base
}

func loadCassette(t *testing.T, path string) *llmvcr.Cassette {
	t.Helper()
	c, err := llmvcr.LoadCassette(path)
	if err != nil {
		t.Fatalf("LoadCassette(%q) = %v", path, err)
	}
	return c
}

// postStatusBody issues a POST through client and returns the status code and
// body.
func postStatusBody(t *testing.T, client *http.Client, u string) (code int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, u, bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatalf("NewRequestWithContext(%q) = %v", u, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s = %v", u, err)
	}
	defer closeBody(t, resp)
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body of POST %s = %v", u, err)
	}
	return resp.StatusCode, string(raw)
}

// TestCannedInteractions_Replay builds a cassette from the canned provider
// interactions — a quota rejection followed by a success — and replays it,
// checking each provider's endpoint is matched and the interactions are
// served in order, as a retry test relies on.
func TestCannedInteractions_Replay(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		interaction func(code int, body string) llmvcr.Interaction
		success     func(text string) string
		quota       func() string
	}{
		{name: "gemini", interaction: llmvcr.GeminiInteraction, success: llmvcr.GeminiResponse, quota: llmvcr.GeminiQuotaExceeded},
		{name: "openai", interaction: llmvcr.OpenAIInteraction, success: llmvcr.OpenAIResponse, quota: llmvcr.OpenAIQuotaExceeded},
		{name: "ollama", interaction: llmvcr.OllamaInteraction, success: llmvcr.OllamaResponse, quota: llmvcr.OllamaQuotaExceeded},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rejected := tc.interaction(http.StatusTooManyRequests, tc.quota())
			accepted := tc.interaction(http.StatusOK, tc.success(cannedText))
			path := saveCassette(t, t.TempDir(), tc.name, rejected, accepted)
			rec, err := llmvcr.New(path, llmvcr.ModeReplayOnly)
			if err != nil {
				t.Fatalf("New(%q, ModeReplayOnly) = %v", path, err)
			}
			t.Cleanup(func() { stopRec(t, rec) })
			client := rec.HTTPClient()

			for _, want := range []llmvcr.Interaction{rejected, accepted} {
				gotCode, gotBody := postStatusBody(t, client, want.Request.URL)
				if gotCode != want.Response.Code {
					t.Errorf("POST %s status = %d, want %d", want.Request.URL, gotCode, want.Response.Code)
				}
				if diff := cmp.Diff(want.Response.Body, gotBody); diff != "" {
					t.Errorf("POST %s body mismatch (-want +got):\n%s", want.Request.URL, diff)
				}
			}
			if !strings.Contains(accepted.Response.Body, `\"cat\"`) {
				t.Errorf("%s success body %q does not embed the payload %q", tc.name, accepted.Response.Body, cannedText)
			}
		})
	}
}

func TestInspectCassette(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	short := llmvcr.OllamaInteraction(http.StatusOK, llmvcr.OllamaResponse(cannedText))
	long := llmvcr.OpenAIInteraction(http.StatusTooManyRequests, llmvcr.OpenAIQuotaExceeded())
	long.Request.URL += "?" + strings.Repeat("q", longURLPathLen)
	path := saveCassette(t, dir, "inspect", short, long) + cassetteExt

	var out bytes.Buffer
	if err := llmvcr.InspectCassette(&out, path); err != nil {
		t.Fatalf("InspectCassette(%q) = %v", path, err)
	}

	got := out.String()
	for _, want := range []string{"(Interactions: 2)", short.Request.URL, "-> 200", "-> 429", "..."} {
		if !strings.Contains(got, want) {
			t.Errorf("InspectCassette(%q) output missing %q:\n%s", path, want, got)
		}
	}
	if strings.Contains(got, long.Request.URL) {
		t.Errorf("InspectCassette(%q) printed the over-long URL untruncated:\n%s", path, got)
	}
}

func TestCassetteTools_MissingFile(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "InspectCassette", run: func() error { return llmvcr.InspectCassette(io.Discard, missing) }},
		{name: "FormatCassette", run: func() error { _, err := llmvcr.FormatCassette(missing); return err }},
		{name: "ExecuteAll", run: func() error {
			return llmvcr.ExecuteAll([]llmvcr.Scenario{{BaseCassette: missing, TargetCassette: missing + ".out"}})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(os.ErrNotExist, tc.run(), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("%s(%q) error mismatch (-want +got):\n%s", tc.name, missing, diff)
			}
		})
	}
}

func TestFormatCassette(t *testing.T) {
	t.Parallel()
	const compactBody = `{"a":1}`
	pretty := llmvcr.FormatJSON(compactBody)
	tests := []struct {
		name        string
		body        string
		contentType string
		wantChanged bool
		wantBody    string
	}{
		{name: "compact JSON is formatted", body: compactBody, contentType: jsonContentType, wantChanged: true, wantBody: pretty},
		{name: "formatted JSON is untouched", body: pretty, contentType: jsonContentType, wantBody: pretty},
		{name: "non-JSON body is untouched", body: compactBody, contentType: "text/plain", wantBody: compactBody},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := llmvcr.OllamaInteraction(http.StatusOK, tc.body)
			in.Response.ContentLength = len(tc.body)
			in.Response.Headers["Content-Type"] = []string{tc.contentType}
			path := saveCassette(t, t.TempDir(), "format", in) + cassetteExt

			gotChanged, err := llmvcr.FormatCassette(path)
			if err != nil {
				t.Fatalf("FormatCassette(%q) = %v", path, err)
			}

			if gotChanged != tc.wantChanged {
				t.Errorf("FormatCassette(%q) changed = %t, want %t", path, gotChanged, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.wantBody, loadCassette(t, path).Interactions[0].Response.Body); diff != "" {
				t.Errorf("FormatCassette(%q) saved body mismatch (-want +got):\n%s", path, diff)
			}
		})
	}
}

// TestExecuteAll_ChainsScenarios runs two scenarios where the second reads the
// first one's output, so both patches must land in the final cassette.
func TestExecuteAll_ChainsScenarios(t *testing.T) {
	t.Parallel()
	const retryAfter = "30"
	dir := t.TempDir()
	base := saveCassette(t, dir, "base", llmvcr.GeminiInteraction(http.StatusOK, llmvcr.GeminiResponse(cannedText))) + cassetteExt
	mid := filepath.Join(dir, "mid"+cassetteExt)
	target := filepath.Join(dir, "target"+cassetteExt)
	match := llmvcr.URLContains(":generateContent")

	err := llmvcr.ExecuteAll([]llmvcr.Scenario{
		{BaseCassette: base, TargetCassette: mid, Patches: []llmvcr.Patch{
			{Match: match, Action: llmvcr.ReplaceStatusCode(http.StatusTooManyRequests)},
		}},
		{BaseCassette: mid, TargetCassette: target, Patches: []llmvcr.Patch{
			{Match: match, Action: llmvcr.ReplaceHeaders(map[string][]string{"Retry-After": {retryAfter}})},
		}},
	})
	if err != nil {
		t.Fatalf("ExecuteAll() = %v", err)
	}

	got := loadCassette(t, target).Interactions[0].Response
	if got.Code != http.StatusTooManyRequests {
		t.Errorf("ExecuteAll() target status = %d, want %d", got.Code, http.StatusTooManyRequests)
	}
	if diff := cmp.Diff([]string{retryAfter}, got.Headers["Retry-After"]); diff != "" {
		t.Errorf("ExecuteAll() target Retry-After mismatch (-want +got):\n%s", diff)
	}
}

func TestExecuteAll_StopsAtFirstError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := saveCassette(t, dir, "base", llmvcr.OpenAIInteraction(http.StatusOK, llmvcr.OpenAIResponse(cannedText))) + cassetteExt
	never := filepath.Join(dir, "never"+cassetteExt)

	err := llmvcr.ExecuteAll([]llmvcr.Scenario{
		{BaseCassette: base, TargetCassette: filepath.Join(dir, "first"+cassetteExt)},
		{BaseCassette: base, TargetCassette: filepath.Join(dir, "bad"+cassetteExt), Patches: []llmvcr.Patch{{}}},
		{BaseCassette: base, TargetCassette: never},
	})

	if diff := cmp.Diff(llmvcr.ErrInvalidPatch, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("ExecuteAll() error mismatch (-want +got):\n%s", diff)
	}
	if _, statErr := os.Stat(never); !os.IsNotExist(statErr) {
		t.Errorf("os.Stat(%q) = %v, want not-exist: scenarios after the failure must not run", never, statErr)
	}
}

func TestReplaceHeaders_NilHeaders(t *testing.T) {
	t.Parallel()
	in := llmvcr.Interaction{}
	want := map[string][]string{"X-Test": {"1"}}

	got, err := llmvcr.ReplaceHeaders(want).Apply(&in, nil)
	if err != nil {
		t.Fatalf("ReplaceHeaders(%v).Apply() = %v", want, err)
	}

	if len(got) != 1 {
		t.Fatalf("ReplaceHeaders(%v).Apply() returned %d interactions, want 1", want, len(got))
	}
	if diff := cmp.Diff(want, got[0].Response.Headers); diff != "" {
		t.Errorf("ReplaceHeaders(%v).Apply() headers mismatch (-want +got):\n%s", want, diff)
	}
}
