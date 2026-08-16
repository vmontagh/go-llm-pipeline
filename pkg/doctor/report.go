package doctor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Check identifiers are stable, machine-readable IDs for each diagnostic
// check. Unlike Result.Name (a human-readable label that may change), these
// values are part of the API contract and safe for consumers to switch on.
const (
	// CheckFileDescriptors is the OS open-file limit check.
	CheckFileDescriptors = "file_descriptors"
	// CheckOpenAILatency is the OpenAI reachability check.
	CheckOpenAILatency = "openai_latency"
	// CheckGeminiLatency is the Gemini reachability check.
	CheckGeminiLatency = "gemini_latency"
	// CheckOpenAIKey is the OpenAI credential-validity check.
	CheckOpenAIKey = "openai_key"
	// CheckGeminiKey is the Gemini credential-validity check.
	CheckGeminiKey = "gemini_key"
	// CheckOllama is the local Ollama reachability and model check.
	CheckOllama = "ollama"
)

// Status classifies the outcome of a single diagnostic check. The zero value
// is StatusUnknown so an unset Result is never mistaken for a pass.
type Status int

const (
	// StatusUnknown is the zero value for an uninitialized Result.
	StatusUnknown Status = iota
	// StatusPass indicates the check succeeded.
	StatusPass
	// StatusWarn indicates a borderline condition that does not block jobs.
	StatusWarn
	// StatusFail indicates a hard failure that should block batch jobs.
	StatusFail
	// StatusSkip indicates the check did not run (for example, a missing
	// credential or an unsupported platform).
	StatusSkip
)

// String returns the uppercase label for a Status.
func (s Status) String() string {
	switch s {
	case StatusPass:
		return "PASS"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	case StatusSkip:
		return "SKIP"
	default:
		return "UNKNOWN"
	}
}

// Result is the outcome of one diagnostic check.
type Result struct {
	// Check is the stable, machine-readable identifier (for example,
	// CheckOpenAIKey). Consumers should switch on this rather than Name.
	Check string
	// Name identifies the check for humans (for example, "openai api key").
	Name string
	// Status is the check outcome.
	Status Status
	// Detail is a short human-readable description of what was observed.
	Detail string
	// Remediation optionally suggests how to resolve a warning or failure.
	Remediation string
	// Duration is how long the check took, when meaningful.
	Duration time.Duration
	// Attrs carries structured fields surfaced by Report.Log.
	Attrs []slog.Attr
}

// Report aggregates the Results of a diagnostic run.
type Report struct {
	Results []Result
}

// HasFailure reports whether any Result hard-failed.
func (r Report) HasFailure() bool {
	for _, res := range r.Results {
		if res.Status == StatusFail {
			return true
		}
	}
	return false
}

// Summary counts Results by Status.
type Summary struct {
	Pass    int
	Warn    int
	Fail    int
	Skip    int
	Unknown int
}

// Summary tallies the Results by Status.
func (r Report) Summary() Summary {
	var s Summary
	for _, res := range r.Results {
		switch res.Status {
		case StatusPass:
			s.Pass++
		case StatusWarn:
			s.Warn++
		case StatusFail:
			s.Fail++
		case StatusSkip:
			s.Skip++
		case StatusUnknown:
			s.Unknown++
		}
	}
	return s
}

// String renders the Report as a plain-text pass/fail report, one line per
// Result plus a trailing summary. It is intended for consumers that want to
// print a human-readable report in addition to (or instead of) structured
// logging via Log.
func (r Report) String() string {
	var b strings.Builder
	for _, res := range r.Results {
		fmt.Fprintf(&b, "[%s] %s: %s\n", res.Status, res.Name, res.Detail)
		if res.Remediation != "" {
			fmt.Fprintf(&b, "       remediation: %s\n", res.Remediation)
		}
	}
	s := r.Summary()
	fmt.Fprintf(&b, "summary: %d pass, %d warn, %d fail, %d skip", s.Pass, s.Warn, s.Fail, s.Skip)
	if s.Unknown > 0 {
		fmt.Fprintf(&b, ", %d unknown", s.Unknown)
	}
	return b.String()
}

// Log writes every Result and a final summary through logger. The log level
// mirrors the Status: pass and skip at info, warn at warn, fail at error.
func (r Report) Log(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		return
	}
	for _, res := range r.Results {
		attrs := []slog.Attr{slog.String("check", res.Name)}
		if res.Check != "" {
			attrs = append(attrs, slog.String("check_id", res.Check))
		}
		if res.Detail != "" {
			attrs = append(attrs, slog.String("detail", res.Detail))
		}
		if res.Remediation != "" {
			attrs = append(attrs, slog.String("remediation", res.Remediation))
		}
		if res.Duration > 0 {
			attrs = append(attrs, slog.Duration("duration", res.Duration))
		}
		attrs = append(attrs, res.Attrs...)
		logger.LogAttrs(ctx, levelFor(res.Status), res.Status.String()+" "+res.Name, attrs...)
	}

	s := r.Summary()
	logger.LogAttrs(ctx, levelFor(summaryStatus(s)), "doctor summary",
		slog.Int("pass", s.Pass),
		slog.Int("warn", s.Warn),
		slog.Int("fail", s.Fail),
		slog.Int("skip", s.Skip),
		slog.Int("unknown", s.Unknown),
	)
}

// levelFor maps a Status to the slog level used when logging it.
func levelFor(s Status) slog.Level {
	switch s {
	case StatusFail:
		return slog.LevelError
	case StatusWarn:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// summaryStatus returns the most severe Status represented in a Summary so the
// summary line is logged at a matching level. Unknown results are treated as a
// warning because an unset Status signals a check that never ran to completion.
func summaryStatus(s Summary) Status {
	switch {
	case s.Fail > 0:
		return StatusFail
	case s.Warn > 0, s.Unknown > 0:
		return StatusWarn
	default:
		return StatusPass
	}
}
