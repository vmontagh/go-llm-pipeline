package llmtest

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vmontagh/go-llm-pipeline/pkg/vcrpatch"
	"gopkg.in/dnaeon/go-vcr.v3/recorder"
)

func TestSetupRecorder(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	cassettePath := filepath.Join(tempDir, "test_recorder")

	rec, client, err := SetupRecorder(cassettePath, recorder.ModeRecordOnce)
	if err != nil {
		t.Fatalf("SetupRecorder failed: %v", err)
	}

	if rec == nil {
		t.Error("expected non-nil recorder")
	}
	if client == nil {
		t.Error("expected non-nil client")
	}

	if err := rec.Stop(); err != nil {
		t.Errorf("stopping recorder failed: %v", err)
	}
}

func TestBeforeSaveHook_PrettyPrint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		contentType       string
		requestBody       string
		responseBody      string
		requestHasHeader  bool
		responseHasHeader bool
		wantRequestBody   string
		wantResponseBody  string
	}{
		{
			name:              "formats valid json request and response",
			contentType:       "application/json",
			requestBody:       `{"request":"compact"}`,
			responseBody:      `{"response":"compact"}`,
			requestHasHeader:  true,
			responseHasHeader: true,
			wantRequestBody:   "{\n  \"request\": \"compact\"\n}",
			wantResponseBody:  "{\n  \"response\": \"compact\"\n}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.responseHasHeader {
					w.Header().Set("Content-Type", tt.contentType)
				}
				if _, err := w.Write([]byte(tt.responseBody)); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
				}
			}))
			defer ts.Close()

			tempDir := t.TempDir()
			cassettePath := filepath.Join(tempDir, "test_hook")

			rec, client, err := SetupRecorder(cassettePath, recorder.ModeRecordOnce)
			if err != nil {
				t.Fatalf("failed to setup VCR: %v", err)
			}

			req, err := http.NewRequestWithContext(context.Background(), "POST", ts.URL, bytes.NewBuffer([]byte(tt.requestBody)))
			if err != nil {
				t.Fatalf("failed to create request: %v", err)
			}
			if tt.requestHasHeader {
				req.Header.Set("Content-Type", tt.contentType)
			}

			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("request failed: %v", err)
			}
			defer resp.Body.Close()
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatalf("failed to read body: %v", err)
			}

			if err := rec.Stop(); err != nil {
				t.Fatalf("failed to stop recorder: %v", err)
			}

			c, err := vcrpatch.LoadCassette(cassettePath + ".yaml")
			if err != nil {
				t.Fatalf("failed to read cassette: %v", err)
			}
			if len(c.Interactions) == 0 {
				t.Fatalf("no interactions found in cassette")
			}

			gotRequestBody := c.Interactions[0].Request.Body
			if diff := cmp.Diff(tt.wantRequestBody, gotRequestBody); diff != "" {
				t.Errorf("request body mismatch (-want +got):\n%s", diff)
			}

			gotResponseBody := c.Interactions[0].Response.Body
			if diff := cmp.Diff(tt.wantResponseBody, gotResponseBody); diff != "" {
				t.Errorf("response body mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
