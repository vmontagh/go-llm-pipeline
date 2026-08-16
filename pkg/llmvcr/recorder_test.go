package llmvcr_test

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/llmvcr"
	"github.com/vmontagh/go-llm-pipeline/pkg/scrub"
)

const (
	fakeBearerToken = "Bearer sk-secret1234567890ABCDEFGHIJ"
	fakeOpenAIKey   = "sk-secret1234567890ABCDEFGHIJ"
	fakeGoogleKey   = "AIzaSyA1234567890abcdefghijklmnopqrstuvw"
)

// doPOST issues a JSON POST through client and drains the response body.
func doPOST(t *testing.T, client *http.Client, u, body string, header http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, u, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequestWithContext() = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do() = %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("resp.Body.Close() = %v", cerr)
		}
	}()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("io.ReadAll() = %v", err)
	}
}

func newJSONServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("w.Write() = %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readCassette(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) = %v", path, err)
	}
	return string(b)
}

// stopRec stops a recorder and reports a failure if Stop errors. Suitable for
// use inside t.Cleanup.
func stopRec(t *testing.T, rec *llmvcr.Recorder) {
	t.Helper()
	if err := rec.Stop(); err != nil {
		t.Errorf("Stop() = %v", err)
	}
}

// closeBody closes a response body and reports a failure if Close errors.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("resp.Body.Close() = %v", err)
	}
}

func TestNew_UnknownMode(t *testing.T) {
	t.Parallel()
	_, err := llmvcr.New("cassette", llmvcr.Mode(99))
	if !errors.Is(err, llmvcr.ErrUnknownMode) {
		t.Fatalf("New() with unknown mode error = %v, want errors.Is(_, %v)", err, llmvcr.ErrUnknownMode)
	}
}

func TestRecord_RedactsSecretsBeforeWrite(t *testing.T) {
	t.Parallel()
	srv := newJSONServer(t, `{"ok":true,"echo":"`+fakeGoogleKey+`"}`)
	cassettePath := filepath.Join(t.TempDir(), "secrets")

	rec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}

	reqBody := `{"api_key":"` + fakeOpenAIKey + `","prompt":"hi"}`
	header := http.Header{}
	header.Set("Authorization", fakeBearerToken)
	header.Set("X-Goog-Api-Key", fakeGoogleKey)
	doPOST(t, rec.HTTPClient(), srv.URL+"/v1/chat?key="+fakeGoogleKey, reqBody, header)

	if err := rec.Stop(); err != nil {
		t.Fatalf("Stop() = %v", err)
	}

	got := readCassette(t, cassettePath+".yaml")
	for _, secret := range []string{fakeBearerToken, fakeOpenAIKey, fakeGoogleKey} {
		if strings.Contains(got, secret) {
			t.Errorf("cassette contains unredacted secret %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("cassette missing REDACTED placeholder:\n%s", got)
	}
}

func TestRecord_PrettyJSON(t *testing.T) {
	t.Parallel()
	const prettyMarker = "\"response\": \"compact\""
	tests := []struct {
		name       string
		prettyJSON bool
		wantPretty bool
	}{
		{name: "enabled by default", prettyJSON: true, wantPretty: true},
		{name: "disabled", prettyJSON: false, wantPretty: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := newJSONServer(t, `{"response":"compact"}`)
			cassettePath := filepath.Join(t.TempDir(), "pretty")

			rec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce, llmvcr.WithPrettyJSON(tt.prettyJSON))
			if err != nil {
				t.Fatalf("New() = %v", err)
			}
			doPOST(t, rec.HTTPClient(), srv.URL, `{"request":"compact"}`, nil)
			if err := rec.Stop(); err != nil {
				t.Fatalf("Stop() = %v", err)
			}

			gotPretty := strings.Contains(readCassette(t, cassettePath+".yaml"), prettyMarker)
			if diff := cmp.Diff(tt.wantPretty, gotPretty); diff != "" {
				t.Errorf("pretty-printed mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// recordKnownPath records a single interaction at /v1/known so a cassette exists
// on disk, then returns the cassette path (without extension).
func recordKnownPath(t *testing.T) string {
	t.Helper()
	srv := newJSONServer(t, `{"ok":true}`)
	cassettePath := filepath.Join(t.TempDir(), "known")
	rec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce)
	if err != nil {
		t.Fatalf("New(record) = %v", err)
	}
	doPOST(t, rec.HTTPClient(), srv.URL+"/v1/known", `{"prompt":"hi"}`, nil)
	if err := rec.Stop(); err != nil {
		t.Fatalf("Stop(record) = %v", err)
	}
	return cassettePath
}

func TestReplayOnly_FailFastOnMiss(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		failFast     bool
		wantErrIs    error
		wantLiveCall bool
	}{
		{name: "fail-fast returns ErrCassetteMiss", failFast: true, wantErrIs: llmvcr.ErrCassetteMiss},
		{name: "disabled performs live call", failFast: false, wantLiveCall: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cassettePath := recordKnownPath(t)

			// A live server whose handler must be reached only when fail-fast
			// is disabled and the recorder falls back to a real network call.
			var hits atomic.Int32
			const liveBody = `{"live":true}`
			live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(liveBody)); err != nil {
					t.Errorf("w.Write() = %v", err)
				}
			}))
			t.Cleanup(live.Close)

			rec, err := llmvcr.New(cassettePath, llmvcr.ModeReplayOnly, llmvcr.WithFailFastOnMiss(tt.failFast))
			if err != nil {
				t.Fatalf("New() = %v", err)
			}
			t.Cleanup(func() { stopRec(t, rec) })

			// Request a path not present in the cassette -> miss.
			missURL := "https://example.invalid/v1/unknown"
			if tt.wantLiveCall {
				missURL = live.URL + "/v1/unknown"
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, missURL, http.NoBody)
			if err != nil {
				t.Fatalf("NewRequestWithContext() = %v", err)
			}
			resp, err := rec.HTTPClient().Do(req)

			if tt.wantErrIs != nil {
				if err == nil {
					closeBody(t, resp)
				}
				if !errors.Is(err, tt.wantErrIs) {
					t.Errorf("Do() error = %v, want errors.Is(_, %v)", err, tt.wantErrIs)
				}
				if hits.Load() != 0 {
					t.Errorf("live server was reached %d time(s) despite fail-fast", hits.Load())
				}
				return
			}

			// Fail-fast disabled: the recorder must fall back to a live call.
			if err != nil {
				t.Fatalf("Do() = %v, want live response", err)
			}
			defer closeBody(t, resp)
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("io.ReadAll() = %v", err)
			}
			if hits.Load() != 1 {
				t.Errorf("live server reached %d time(s), want 1", hits.Load())
			}
			if diff := cmp.Diff(liveBody, strings.TrimSpace(string(body))); diff != "" {
				t.Errorf("live response body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRecordThenReplay_RoundTrip(t *testing.T) {
	t.Parallel()
	const wantBody = `{"reply":"deterministic"}`
	cassettePath := filepath.Join(t.TempDir(), "roundtrip")

	// Record against a live server.
	srv := newJSONServer(t, wantBody)
	recRec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce, llmvcr.WithPrettyJSON(false))
	if err != nil {
		t.Fatalf("New(record) = %v", err)
	}
	doPOST(t, recRec.HTTPClient(), srv.URL+"/v1/chat/completions", `{"prompt":"hi"}`, nil)
	if err := recRec.Stop(); err != nil {
		t.Fatalf("Stop(record) = %v", err)
	}

	// Replay offline against a different (closed) URL but matching path.
	replayRec, err := llmvcr.New(cassettePath, llmvcr.ModeReplayOnly)
	if err != nil {
		t.Fatalf("New(replay) = %v", err)
	}
	t.Cleanup(func() { stopRec(t, replayRec) })

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.invalid/v1/chat/completions", bytes.NewBufferString(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext() = %v", err)
	}
	resp, err := replayRec.HTTPClient().Do(req)
	if err != nil {
		t.Fatalf("Do(replay) = %v", err)
	}
	defer closeBody(t, resp)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(replay) = %v", err)
	}
	if diff := cmp.Diff(wantBody, strings.TrimSpace(string(body))); diff != "" {
		t.Errorf("replayed body mismatch (-want +got):\n%s", diff)
	}
}

func TestWithAdditionalScrubRules(t *testing.T) {
	t.Parallel()
	srv := newJSONServer(t, `{"a":"topsecret-value","b":"classified-value"}`)
	cassettePath := filepath.Join(t.TempDir(), "extra")

	// Two separate calls must both take effect (they accumulate rather than the
	// later call replacing the earlier one).
	ruleA := scrub.MustRegexpRule(`topsecret-\w+`, "REDACTED")
	ruleB := scrub.MustRegexpRule(`classified-\w+`, "REDACTED")
	rec, err := llmvcr.New(cassettePath, llmvcr.ModeRecordOnce,
		llmvcr.WithAdditionalScrubRules(ruleA),
		llmvcr.WithAdditionalScrubRules(ruleB),
	)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	doPOST(t, rec.HTTPClient(), srv.URL, `{"prompt":"hi"}`, nil)
	if err := rec.Stop(); err != nil {
		t.Fatalf("Stop() = %v", err)
	}

	got := readCassette(t, cassettePath+".yaml")
	for _, secret := range []string{"topsecret-value", "classified-value"} {
		if strings.Contains(got, secret) {
			t.Errorf("custom scrub rule not applied for %q:\n%s", secret, got)
		}
	}
}
