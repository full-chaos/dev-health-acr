package genkitruntime

// modelStatus is a gRPC-style status name. The values are the strings the
// Genkit error type carried, so "provider status <name>" messages are unchanged.
type modelStatus string

const (
	statusResourceExhausted modelStatus = "RESOURCE_EXHAUSTED"
	statusInvalidArgument   modelStatus = "INVALID_ARGUMENT"
	statusInternal          modelStatus = "INTERNAL"
	statusUnavailable       modelStatus = "UNAVAILABLE"
	statusDeadlineExceeded  modelStatus = "DEADLINE_EXCEEDED"
	statusAborted           modelStatus = "ABORTED"
)

// modelStatusError is a structured model failure that classifyModelError and
// retryable read by status, never by message text.
type modelStatusError struct {
	Status  modelStatus
	Message string
}

func (e *modelStatusError) Error() string { return e.Message }
