// Package analyzer provides the Template Method and Builder patterns for LLM-based analysis.
package analyzer

import (
	"google.golang.org/grpc/codes"

	"github.com/vmontagh/go-llm-pipeline/pkg/policy"
)

const eventLLMRetrying = "LLM_RETRYING"

// isAnalyzerQuotaClass reports whether err is in a quota or rate-limit family.
func isAnalyzerQuotaClass(err error) bool {
	c := policy.Code(err)
	return c == codes.ResourceExhausted || c == codes.FailedPrecondition
}
