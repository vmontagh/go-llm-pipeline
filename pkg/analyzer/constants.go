package analyzer

// Default analysis parameters shared by all providers.
const (
	DefaultMinTagsRequired = 20
	DefaultLeniencyPercent = 50
)

// Event type constants for the publish callback.
const (
	EventLLMRetrying = "LLM_RETRYING"
)
