package eventspec

// OTel export: the hosted binaries (acr-api, acr-mcp over HTTP,
// acr-projector) may export their own HTTP spans, HTTP metrics and their
// structured log lines over OTLP. Whether a process exports, and under which
// service name, is one line at process start, so a dark telemetry backend can
// be told apart from a process that was never configured to send to it.

// OTelExportLogMessage is the message of the one process-start line.
const OTelExportLogMessage = "acr otel export"

const (
	// OTelExportStateEnabled: OTEL_ENABLED is true and OTLP exporters for
	// traces, metrics and logs were built.
	OTelExportStateEnabled = "enabled"
	// OTelExportStateDisabled: OTEL_ENABLED is unset or false; nothing is
	// exported and the process logs to its own stream only.
	OTelExportStateDisabled = "disabled"
)

// OTelExportStateVocabulary lists every state.
func OTelExportStateVocabulary() []string {
	return []string{OTelExportStateEnabled, OTelExportStateDisabled}
}

const (
	// OTelExportProtocolGRPC is OTLP over gRPC.
	OTelExportProtocolGRPC = "grpc"
	// OTelExportProtocolNone is written when the state is disabled.
	OTelExportProtocolNone = "none"
)

// OTelExportProtocolVocabulary lists every protocol value.
func OTelExportProtocolVocabulary() []string {
	return []string{OTelExportProtocolGRPC, OTelExportProtocolNone}
}

// OTelExport is the one line each hosted process writes at start about its
// own OTLP export: whether it exports, as which service, at which build
// commit, and over which protocol. The collector endpoint is not written: it
// is deployment configuration and the process cannot vouch for it.
var OTelExport = Event{
	ID:                 "process.otel_export",
	Msg:                OTelExportLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"service_name"},
	BoundedAggregation: "exactly one line per process start, before the listener is bound",
	Fields: []Field{
		{Key: "service_name", Type: FieldString, Presence: PresenceRequired},
		{Key: "service_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OTelExportStateVocabulary()},
		{Key: "protocol", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OTelExportProtocolVocabulary()},
	},
}
