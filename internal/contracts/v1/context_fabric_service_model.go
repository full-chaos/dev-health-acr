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

// ContextFabricAIFeaturesTurnedOffReason is details.reason on the 403
// feature_not_enabled refusal of an investigation for an organization that has
// a saved model configuration but acr_service_model OFF (its own override):
// ACR AI features are turned off for it. No model is used.
const ContextFabricAIFeaturesTurnedOffReason = "ai_features_turned_off"

// ContextFabricNoInvestigateConfigReason is details.reason on the 403
// feature_not_enabled refusal of an investigation for an organization whose
// saved BYO model keys are for other purposes only: none is set for
// investigations. It is a BYO organization, so the service's own model is
// never used in its place; an organization admin saves an investigate-capable
// key or turns investigations off.
const ContextFabricNoInvestigateConfigReason = "byo_no_investigate_config"
