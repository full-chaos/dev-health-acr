package v1

import (
	"strconv"
	"strings"
)

const (
	// ContextFabricWaitBudgetHeader carries the longest the caller will wait
	// for one investigation, in whole seconds. The hosted API ends the request
	// by then, serving the degraded answer (with its result_id) its reserve
	// already composes when the model call has not finished.
	ContextFabricWaitBudgetHeader = "X-ACR-Wait-Budget-Seconds"

	// ContextFabricSynthesisCompositionReserveSeconds is the slice of a request
	// deadline the synthesis model call never uses; the degraded answer is
	// composed and stored inside it.
	ContextFabricSynthesisCompositionReserveSeconds = 5

	// ContextFabricWaitBudgetMinSeconds is the smallest budget the hosted API
	// honours: twice the reserve, so a model call still has a real slice.
	ContextFabricWaitBudgetMinSeconds = 2 * ContextFabricSynthesisCompositionReserveSeconds
)

// ParseContextFabricWaitBudgetSeconds reads the header value. ok is false for
// an empty value; valid is false for a value that is not a whole number of
// seconds of at least ContextFabricWaitBudgetMinSeconds.
func ParseContextFabricWaitBudgetSeconds(raw string) (seconds int, ok bool, valid bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false, false
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed < ContextFabricWaitBudgetMinSeconds {
		return 0, true, false
	}
	return parsed, true, true
}
