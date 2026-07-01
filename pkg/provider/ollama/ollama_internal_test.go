package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	provpkg "github.com/vmontagh/go-llm-pipeline/pkg/provider"
)

var errTest = errors.New("test sentinel")

func TestTransport_ClassifyError(t *testing.T) {
	t.Parallel()
	tr := &transport{model: "m"}
	tests := []struct {
		name string
		in   provpkg.ClassifyInput
		want provpkg.ErrorTier
	}{
		{name: "network error", in: provpkg.ClassifyInput{Err: errTest}, want: provpkg.TierShortTransient},
		{name: "429", in: provpkg.ClassifyInput{StatusCode: http.StatusTooManyRequests}, want: provpkg.TierShortTransient},
		{name: "500", in: provpkg.ClassifyInput{StatusCode: http.StatusInternalServerError}, want: provpkg.TierShortTransient},
		{name: "503", in: provpkg.ClassifyInput{StatusCode: http.StatusServiceUnavailable}, want: provpkg.TierShortTransient},
		{name: "400", in: provpkg.ClassifyInput{StatusCode: http.StatusBadRequest}, want: provpkg.TierTerminal},
		{name: "404", in: provpkg.ClassifyInput{StatusCode: http.StatusNotFound}, want: provpkg.TierTerminal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tt.want, tr.ClassifyError(tt.in, provpkg.RetryContext{})); diff != "" {
				t.Errorf("ClassifyError mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTransport(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "EnrichError/passthrough",
			run: func(t *testing.T) {
				tr := &transport{}
				raw := errTest
				got := tr.EnrichError(provpkg.TierTerminal, raw, nil)
				if diff := cmp.Diff(raw, got, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("EnrichError mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "String/representation",
			run: func(t *testing.T) {
				tr := &transport{host: "h", model: "m"}
				if diff := cmp.Diff("ollama.Analyzer{host=h, model=m}", tr.String()); diff != "" {
					t.Errorf("String mismatch (-want +got):\n%s", diff)
				}
			},
		},
		{
			name: "Provider/MetadataAndOptions",
			run: func(t *testing.T) {
				client := &http.Client{}
				p, ok := NewProvider(
					"http://host",
					"m1",
					WithKeepAlive("5m"),
					WithNumCtx(4096),
					WithHTTPClient(client),
					WithHTTPClientTimeout(3*time.Second),
				).(*transport)
				if !ok {
					t.Fatal("NewProvider did not return *transport")
				}

				if diff := cmp.Diff("ollama/m1", p.Name()); diff != "" {
					t.Fatalf("Name mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff("m1", p.Model()); diff != "" {
					t.Fatalf("Model mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(providerLabel, p.ProviderLabel()); diff != "" {
					t.Fatalf("ProviderLabel mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(traceRegionName, p.TraceRegionName()); diff != "" {
					t.Fatalf("TraceRegionName mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff("5m", p.keepAlive); diff != "" {
					t.Fatalf("keepAlive mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(4096, p.numCtx); diff != "" {
					t.Fatalf("numCtx mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(1, p.Keys().Len()); diff != "" {
					t.Fatalf("Keys length mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(3*time.Second, p.client.Timeout); diff != "" {
					t.Fatalf("client timeout mismatch (-want +got):\n%s", diff)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}

func TestBuildRequest_KeepAlive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		img            string
		keepAlive      string
		numCtx         int
		wantKeepAlive  string
		wantImages     []string
		wantNumCtx     int
		wantOmitInJSON bool
	}{
		{
			name:          "explicit keep_alive is preserved",
			img:           "img",
			keepAlive:     "5m",
			numCtx:        0,
			wantKeepAlive: "5m",
			wantImages:    []string{"img"},
			wantNumCtx:    8192,
		},
		{
			name:           "empty keep_alive is omitted from JSON",
			img:            "",
			keepAlive:      "",
			numCtx:         4096,
			wantOmitInJSON: true,
			wantNumCtx:     4096,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := buildRequest(&buildRequestParams{model: "m", prompt: "p", imageBase64: tt.img, keepAlive: tt.keepAlive, numCtx: tt.numCtx})
			if tt.wantOmitInJSON {
				b, err := json.Marshal(req)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if strings.Contains(string(b), "keep_alive") {
					t.Errorf("keep_alive should be omitted from JSON: %s", string(b))
				}
			} else {
				if diff := cmp.Diff(tt.wantKeepAlive, req.KeepAlive); diff != "" {
					t.Errorf("KeepAlive mismatch (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(tt.wantImages, req.Images); diff != "" {
					t.Errorf("Images mismatch (-want +got):\n%s", diff)
				}
			}
			gotNumCtx, ok := req.Options["num_ctx"].(int)
			if !ok {
				t.Errorf("num_ctx option missing or not an int")
			} else if gotNumCtx != tt.wantNumCtx {
				t.Errorf("num_ctx = %d, want %d", gotNumCtx, tt.wantNumCtx)
			}
		})
	}
}

func TestTransport_Call(t *testing.T) {
	t.Parallel()
	type testCase struct {
		name       string
		client     *http.Client
		numPredict int
		wantOut    provpkg.CallOutcome
		wantStatus int
		wantText   string
		wantErr    error
	}

	assertResult := func(t *testing.T, tc testCase, got provpkg.CallResult) {
		t.Helper()
		if diff := cmp.Diff(tc.wantOut, got.Outcome); diff != "" {
			t.Fatalf("Outcome mismatch (-want +got):\n%s", diff)
		}
		if tc.wantStatus != 0 {
			if diff := cmp.Diff(tc.wantStatus, got.StatusCode); diff != "" {
				t.Fatalf("StatusCode mismatch (-want +got):\n%s", diff)
			}
		}
		if tc.wantText != "" {
			if diff := cmp.Diff(tc.wantText, got.Text); diff != "" {
				t.Fatalf("Text mismatch (-want +got):\n%s", diff)
			}
		}
		if tc.wantErr != nil {
			if got.Err == nil {
				t.Fatalf("error = nil, want %v", tc.wantErr)
			}
			if !errors.Is(got.Err, tc.wantErr) && !strings.Contains(got.Err.Error(), tc.wantErr.Error()) {
				t.Fatalf("error = %v, want to contain or be %v", got.Err, tc.wantErr)
			}
		}
	}

	tests := []testCase{
		{
			name: "network error",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errTest
			})},
			wantOut: provpkg.OutcomeProviderError,
		},
		{
			name: "body read error keeps status code",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusInternalServerError, Body: errReadCloser{}}, nil
			})},
			wantOut:    provpkg.OutcomeProviderError,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "successful response returns text",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"response":"ok"}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut:  provpkg.OutcomeSuccess,
			wantText: "ok",
		},
		{
			name: "429 returns provider error",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(strings.NewReader(`busy`)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut:    provpkg.OutcomeProviderError,
			wantStatus: http.StatusTooManyRequests,
			wantErr:    provpkg.ErrUnexpectedStatus,
		},
		{
			name: "non-200 non-retryable is terminal",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader(`bad`)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut: provpkg.OutcomeTerminal,
			wantErr: provpkg.ErrUnexpectedStatus,
		},
		{
			name: "done_reason length (context exhaustion, short response)",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"response":"","done":true,"done_reason":"length","prompt_eval_count":4056,"eval_count":40}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut: provpkg.OutcomeTerminal,
			wantErr: ErrContextWindowExhausted,
		},
		{
			name: "done_reason length (context exhaustion, excessively long response)",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"response":"","done":true,"done_reason":"length","prompt_eval_count":100,"eval_count":1500}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})},
			numPredict: 2048,
			wantOut:    provpkg.OutcomeTerminal,
			wantErr:    ErrContextWindowExhausted,
		},
		{
			name: "done_reason length (output generation limit exhausted)",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"response":"","done":true,"done_reason":"length","prompt_eval_count":100,"eval_count":1024}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut: provpkg.OutcomeTerminal,
			wantErr: ErrOutputLimitExhausted,
		},
		{
			name: "done_reason stop returns success",
			client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				body := `{"response":"ok","done":true,"done_reason":"stop","prompt_eval_count":1000,"eval_count":500}`
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			})},
			wantOut:  provpkg.OutcomeSuccess,
			wantText: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tr := &transport{
				host:       "http://example.test",
				model:      "m",
				client:     tt.client,
				logger:     slog.New(slog.DiscardHandler),
				tracer:     noOpTracer{},
				numPredict: tt.numPredict,
			}
			got := tr.Call(context.Background(), &provpkg.Lease[struct{}]{}, "prompt", []byte("img"))
			assertResult(t, tt, got)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) { return 0, errTest }
func (errReadCloser) Close() error             { return nil }

type noOpRegion struct{}

func (noOpRegion) End() {}

type noOpTracer struct{}

func (noOpTracer) StartRegion(context.Context, string) interface{ End() } { return noOpRegion{} }

func TestBuildRequest_ResponseSchema(t *testing.T) {
	t.Parallel()
	schemaObj := map[string]any{"type": "object"}
	tests := []struct {
		name           string
		responseSchema any
		wantFormat     any
	}{
		{
			name:           "nil schema falls back to json sentinel",
			responseSchema: nil,
			wantFormat:     any(formatJSONSentinel),
		},
		{
			name:           "schema map is passed through verbatim",
			responseSchema: schemaObj,
			wantFormat:     any(schemaObj),
		},
		{
			name:           "non-map schema (string) is still passed through",
			responseSchema: "object",
			wantFormat:     any("object"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := buildRequest(&buildRequestParams{model: "m", prompt: "p", responseSchema: tt.responseSchema})
			if diff := cmp.Diff(tt.wantFormat, req.Format); diff != "" {
				t.Errorf("Format mismatch (-want +got):\n%s", diff)
			}
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var roundTrip struct {
				Format json.RawMessage `json:"format"`
			}
			if err := json.Unmarshal(b, &roundTrip); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			wantRaw, err := json.Marshal(tt.wantFormat)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			if diff := cmp.Diff(string(wantRaw), string(roundTrip.Format)); diff != "" {
				t.Errorf("wire format mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestBuildRequest_NumPredict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		numPredict     int
		wantNumPredict int
	}{
		{
			name:           "zero falls back to default",
			numPredict:     0,
			wantNumPredict: defaultNumPredict,
		},
		{
			name:           "non-zero overrides default",
			numPredict:     256,
			wantNumPredict: 256,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := buildRequest(&buildRequestParams{model: "m", prompt: "p", numPredict: tt.numPredict})
			gotNumPredict, ok := req.Options["num_predict"].(int)
			if !ok {
				t.Fatalf("num_predict missing or not an int: %v", req.Options["num_predict"])
			}
			if gotNumPredict != tt.wantNumPredict {
				t.Errorf("num_predict mismatch: want %d, got %d", tt.wantNumPredict, gotNumPredict)
			}
			b, err := json.Marshal(req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var roundTrip struct {
				Options struct {
					NumPredict int `json:"num_predict"`
				} `json:"options"`
			}
			if err := json.Unmarshal(b, &roundTrip); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if roundTrip.Options.NumPredict != tt.wantNumPredict {
				t.Errorf("wire num_predict mismatch: want %d, got %d", tt.wantNumPredict, roundTrip.Options.NumPredict)
			}
		})
	}
}
