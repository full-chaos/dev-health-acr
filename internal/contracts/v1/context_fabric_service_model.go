package v1

// ContextFabricServiceModelNotEntitledReason is details.reason on the 403
// feature_not_enabled refusal of an investigation that would run on the
// service's own model for an organization that has no model configuration of
// its own and does not hold both acr_service_model and
// platform_model_fallback.
const ContextFabricServiceModelNotEntitledReason = "service_model_not_entitled"

// ContextFabricOwnModelGateClosedReason is details.reason on the 403
// feature_not_enabled refusal of an investigation for an organization whose
// saved model configuration is behind a closed byo_llm gate (for example a
// lapsed plan). The service's own model is never used in its place.
const ContextFabricOwnModelGateClosedReason = "own_model_gate_closed"
