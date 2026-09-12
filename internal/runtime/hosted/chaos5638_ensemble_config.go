package hosted

import (
	"log/slog"
	"strconv"
	"strings"
)

// InterpretationEnsembleSizeEnv is the environment variable an operator sets
// to choose N for the question-family consensus (CHAOS-5638).
//
// NAMED AS A CONSTANT because it is a contract with something outside this
// repository: the corpus measurement that decides whether N>1 ships preflights
// on this exact string, and a rig that sets a variable nothing reads would
// report an N=1 result as though it were an N=3 one -- the worst possible
// failure for a measurement whose whole purpose is to compare the two.
const InterpretationEnsembleSizeEnv = "ACR_CONTEXT_FABRIC_INTERPRETATION_ENSEMBLE_SIZE"

// InterpretationEnsembleSizeFromEnv reads the ensemble size an operator
// configured, defaulting to 1.
//
// ONE means the pre-ensemble path exactly -- a single interpret call, the
// precedence table deciding alone -- so an unset variable, which is every
// deployment today, changes nothing.
//
// A MALFORMED VALUE IS A WARNING AND A DEFAULT, NEVER A STARTUP FAILURE. The
// two are a real choice and this takes the safer one: refusing to boot the API
// over an unparseable performance knob turns a typo in a deploy manifest into
// an outage, and the knob's own safe value is the one the deployment already
// runs. But it is not silent either -- an operator who set the variable
// believing it took effect learns otherwise from the log, which is the failure
// this whole seam is built to avoid at every other layer too.
//
// The value is bounded downstream by contextfabric.QuestionFamilyEnsembleMax;
// this function's job is only to turn an environment string into an integer
// the composition can carry.
func InterpretationEnsembleSizeFromEnv(lookup func(string) (string, bool), logger *slog.Logger) int {
	if lookup == nil {
		return 1
	}
	raw, ok := lookup(InterpretationEnsembleSizeEnv)
	trimmed := strings.TrimSpace(raw)
	if !ok || trimmed == "" {
		return 1
	}
	size, err := strconv.Atoi(trimmed)
	if err != nil || size < 1 {
		warnEnsembleSizeIgnored(logger, trimmed)
		return 1
	}
	return size
}

// warnEnsembleSizeIgnored emits the one line that keeps a rejected value from
// being indistinguishable from an unset one.
//
// The raw value is logged because it is what the operator typed and it is what
// they need to see to find their own typo; it is an operator-supplied
// configuration string, not request- or model-derived content.
func warnEnsembleSizeIgnored(logger *slog.Logger, raw string) {
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn("context fabric interpretation ensemble size is not a positive integer; using 1",
		"env", InterpretationEnsembleSizeEnv,
		"value", raw,
	)
}
