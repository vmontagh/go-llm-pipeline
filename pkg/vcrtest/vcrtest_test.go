package vcrtest_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/vmontagh/go-llm-pipeline/pkg/vcrtest"
)

const (
	// replayFixture is committed under testdata/cassettes and drives the
	// offline replay test. Re-record it with VCR_MODE=record (it is captured
	// against the in-process stub server started below).
	replayFixture = "replay_fixture"
	echoPath      = "/v1/echo"
	echoBody      = `{"reply":"deterministic"}`
	// replayHost is an unreachable placeholder used on replay; the default
	// matcher tolerates the changed host, so only method+path+query must match.
	replayHost = "https://vcrtest.invalid"
)

// newJSONServer returns a stub server that responds with body as JSON and is
// closed via t.Cleanup.
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

// doPOST issues a JSON POST through client and returns the trimmed response body.
func doPOST(t *testing.T, client *http.Client, url, body string, header http.Header) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewBufferString(body))
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
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("io.ReadAll() = %v", err)
	}
	return strings.TrimSpace(string(got))
}

// TestClient_ReplayFromCassette proves the harness replays a committed cassette
// fully offline: the request targets an unreachable host and only succeeds
// because the recorded interaction is served from disk.
func TestClient_ReplayFromCassette(t *testing.T) {
	t.Parallel()

	// On replay the host is unreachable; when re-recording, capture against a
	// live in-process stub instead.
	host := replayHost
	if vcrtest.Recording() {
		host = newJSONServer(t, echoBody).URL
	}

	client := vcrtest.Client(t, replayFixture)
	got := doPOST(t, client, host+echoPath, `{"prompt":"hi"}`, nil)
	// The recorder pretty-prints JSON bodies by default, so compare the decoded
	// payload rather than the exact bytes.
	if diff := cmp.Diff(decodeJSON(t, echoBody), decodeJSON(t, got)); diff != "" {
		t.Errorf("replayed body mismatch (-want +got):\n%s", diff)
	}
}

// decodeJSON unmarshals a JSON object into a map for whitespace-insensitive
// comparison.
func decodeJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("json.Unmarshal(%q) = %v", s, err)
	}
	return m
}

// TestClient_RecordRedactsSecrets records a cassette carrying a bearer token and
// an OpenAI-style key, then asserts the written YAML leaks neither and shows the
// REDACTED placeholder. It runs in record mode against an in-process stub with
// its working directory redirected to a temp dir, so it neither reaches the
// network nor touches committed fixtures. The recording subtest scopes the
// recorder's t.Cleanup so the cassette is flushed to disk before it is read.
func TestClient_RecordRedactsSecrets(t *testing.T) {
	// t.Setenv and t.Chdir forbid t.Parallel; that is fine, this test is cheap.
	t.Setenv(vcrtest.EnvVCRMode, "record")
	dir := t.TempDir()
	t.Chdir(dir)

	const (
		bearerToken = "Bearer sk-secretBEARER1234567890abcdef"
		openAIKey   = "sk-bodySECRET1234567890abcdefghij" // gitleaks:allow — fake fixture for redaction tests
	)
	srv := newJSONServer(t, `{"ok":true,"echo":"`+openAIKey+`"}`)

	// The recorder flushes on Stop, which the harness registers via t.Cleanup.
	// Scoping the recording in a subtest runs that cleanup when the subtest
	// returns, so the parent can read the flushed cassette below.
	t.Run("record", func(t *testing.T) {
		client := vcrtest.Client(t, "redact_proof")
		header := http.Header{}
		header.Set("Authorization", bearerToken)
		reqBody := `{"api_key":"` + openAIKey + `","prompt":"hi"}`
		doPOST(t, client, srv.URL+echoPath, reqBody, header)
	})

	path := filepath.Join(dir, "testdata", "cassettes", "redact_proof.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) = %v", path, err)
	}
	got := string(raw)
	for _, secret := range []string{bearerToken, openAIKey} {
		if strings.Contains(got, secret) {
			t.Errorf("recorded cassette leaked secret %q:\n%s", secret, got)
		}
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("recorded cassette missing REDACTED placeholder:\n%s", got)
	}
}
