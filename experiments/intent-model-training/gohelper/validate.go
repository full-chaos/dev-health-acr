package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/xeipuuv/gojsonschema"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// Error stages. INTERFACES.md section 6 lists json|decode|domain|frame|
// family|window; this helper adds strict, schema, sanitize, interpreter
// and request.
const (
	stageJSON        = "json"
	stageStrict      = "strict"
	stageSchema      = "schema"
	stageDecode      = "decode"
	stageDomain      = "domain"
	stageInterpreter = "interpreter"
	stageSanitize    = "sanitize"
	stageFrame       = "frame"
	stageFamily      = "family"
	stageWindow      = "window"
	stageRequest     = "request"
	stageTransport   = "transport"
)

type helperError struct {
	Stage   string `json:"stage"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type frameFailure struct {
	Invariant string `json:"invariant"`
	Phase     string `json:"phase"`
	Detail    string `json:"detail"`
}

type frameRepair struct {
	Decision  string `json:"decision"`
	Name      string `json:"name"`
	Invariant string `json:"invariant"`
}

type frameReport struct {
	Present     bool          `json:"present"`
	Outcome     string        `json:"outcome"`
	A1OK        *bool         `json:"a1_ok"`
	A2OK        *bool         `json:"a2_ok"`
	Failure     *frameFailure `json:"failure"`
	Repair      *frameRepair  `json:"repair"`
	Obligations []string      `json:"obligations"`
	Sanitize    frameSanitize `json:"sanitize"`
	Gate        gateReport    `json:"gate"`
}

type familyReport struct {
	Family           string `json:"family"`
	Source           string `json:"source"`
	PrecedenceRow    string `json:"precedence_row"`
	AttemptedFamily  string `json:"attempted_family"`
	Downgraded       bool   `json:"downgraded"`
	RouteFamily      string `json:"route_family"`
	RouteSource      string `json:"route_source"`
	RouteClass       string `json:"route_class"`
	RouteDisposition string `json:"route_disposition"`
	RouteSwitched    bool   `json:"route_switched"`
}

// Envelope is the validate output (INTERFACES.md section 6). Fields after
// Semantic are additions this helper documents in TARGET-CONTRACT.md.
type Envelope struct {
	JSONOK        bool                               `json:"json_ok"`
	DuplicateKeys []string                           `json:"duplicate_keys"`
	UnknownFields []string                           `json:"unknown_fields"`
	DecodeOK      bool                               `json:"decode_ok"`
	DomainOK      bool                               `json:"domain_ok"`
	Errors        []helperError                      `json:"errors"`
	CanonicalText *string                            `json:"canonical_text"`
	Interpreted   *contextfabric.InterpretedQuestion `json:"interpreted"`
	Frame         *frameReport                       `json:"frame"`
	Family        *familyReport                      `json:"family"`
	Semantic      *semanticProjection                `json:"semantic"`

	Transport     string  `json:"transport"`
	ExtractedText *string `json:"extracted_text"`

	CaseVariantKeys   []string `json:"case_variant_keys"`
	NullPaths         []string `json:"null_paths"`
	UntrimmedPaths    []string `json:"untrimmed_paths"`
	MarkdownFenced    bool     `json:"markdown_fenced"`
	SchemaOK          bool     `json:"schema_ok"`
	InterpreterOK     bool     `json:"interpreter_ok"`
	ProductionAccepts bool     `json:"production_accepts"`
	ExchangeAccepts   bool     `json:"exchange_accepts"`
	TurnAdmitted      bool     `json:"turn_admitted"`
	SanitizeClean     bool     `json:"sanitize_clean"`
	StrictOK          bool     `json:"strict_ok"`
	RejectionReason   string   `json:"rejection_reason"`
	RawSHA256         string   `json:"raw_sha256"`
	RequestSHA256     string   `json:"request_sha256"`
	RequestOK         bool     `json:"request_ok"`
	UserPayloadSHA256 string   `json:"user_payload_sha256"`
}

var fencedJSON = regexp.MustCompile("(?s)^```(?:json)?\\s*\\n?(.*?)\\n?```$")

func newEnvelope() Envelope {
	return Envelope{
		DuplicateKeys: []string{}, UnknownFields: []string{}, Errors: []helperError{},
		CaseVariantKeys: []string{}, NullPaths: []string{}, UntrimmedPaths: []string{},
	}
}

func (e *Envelope) fail(stage, code, message string) {
	e.Errors = append(e.Errors, helperError{Stage: stage, Code: code, Message: message})
}

// Transports. genkit is the ruled serve path (production genkit runtime
// behind a BYO OpenAI-compatible base URL): genkit's JSON format handler
// extracts JSON from a markdown fence before parsing. exchange is the
// file-exchange transport, which json.Unmarshals the raw text as is.
const (
	transportGenkit   = "genkit"
	transportExchange = "exchange"
)

func normalizeTransport(transport string) (string, error) {
	switch transport {
	case "", transportGenkit:
		return transportGenkit, nil
	case transportExchange:
		return transportExchange, nil
	default:
		return "", fmt.Errorf("unknown transport %q (want %q or %q)", transport, transportGenkit, transportExchange)
	}
}

// validate runs one raw model output through the production path of the
// chosen transport and reports it as data. It returns an error only for a
// malformed request or an unknown transport.
//
// The production verdict (interpreter_ok, production_accepts,
// turn_admitted) always comes from real production code running on the raw
// text: for genkit, the production genkitruntime.Runtime over the in-process
// recorder model (genkit's format handler, schema check, decode, toDomain,
// sanitizers); for exchange, the file-exchange parse path. Then
// RuntimeQuestionInterpreter.Interpret adds frame validation, bounded
// repair, the gate and family resolution.
//
// The helper's own analysis (strict shape, schema, decode, canonical text,
// semantic projection) runs on the text the transport parses: genkit's
// extracted JSON text, or the trimmed raw text for exchange. When that
// analysis and the production verdict disagree, the envelope carries a
// transport-stage error instead of hiding it.
func validate(raw string, requestRaw json.RawMessage, transport string) (Envelope, error) {
	transport, err := normalizeTransport(transport)
	if err != nil {
		return Envelope{}, err
	}
	decoded, err := decodeRequest(requestRaw)
	if err != nil {
		return Envelope{}, err
	}
	payload, err := genkitruntime.BuildInterpretationPrompt(decoded.Request, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		return Envelope{}, fmt.Errorf("render payload: %w", err)
	}
	env := newEnvelope()
	env.Transport = transport
	env.RawSHA256 = sha256Hex([]byte(raw))
	env.RequestSHA256 = decoded.RequestSHA
	env.RequestOK = decoded.ValidateErr == nil
	env.UserPayloadSHA256 = sha256Hex([]byte(payload))
	if decoded.ValidateErr != nil {
		env.fail(stageRequest, "request_invalid", decoded.ValidateErr.Error())
	}

	// The text the transport parses.
	var body []byte
	var bodyErr error
	switch transport {
	case transportGenkit:
		extracted, extractErr := genkitExtract(raw)
		if extractErr != nil {
			bodyErr = extractErr
		} else {
			env.ExtractedText = extracted
			body = []byte(*extracted)
			// genkit accepts a fenced answer; it is still not a clean
			// training target, so it is flagged (and excluded from
			// strict_ok) without being an error.
			env.MarkdownFenced = strings.TrimSpace(*extracted) != strings.TrimSpace(raw)
		}
	default:
		body = bytes.TrimSpace([]byte(raw))
	}

	// Production verdict first, independent of the helper's own parsing.
	var replay replayResult
	switch transport {
	case transportGenkit:
		replay, err = replayGenkit(raw, decoded.Request)
		if err != nil {
			return Envelope{}, err
		}
	default:
		replay = replayInterpretation(replayRuntime{raw: bytes.TrimSpace([]byte(raw)), prompt: payload}, decoded.Request)
	}
	env.InterpreterOK = replay.Err == nil
	if env.InterpreterOK {
		value := replay.Interpreted
		env.Interpreted = &value
	}
	env.ExchangeAccepts = env.InterpreterOK && decoded.ValidateErr == nil

	var tree *node
	if bodyErr != nil {
		env.fail(stageJSON, "genkit_no_json", bodyErr.Error())
	} else {
		parsed, parseErr := parseOrdered(body)
		switch {
		case parseErr != nil:
			code := "json_invalid"
			if transport == transportExchange && fencedJSON.Match(body) {
				// The file-exchange transport does not strip a markdown
				// fence; genkit does (see the genkit transport).
				env.MarkdownFenced = true
				code = "json_markdown_fenced"
			}
			env.fail(stageJSON, code, parseErr.Error())
		case parsed.Kind != nodeObject:
			env.fail(stageJSON, "json_not_object", "top-level JSON value is not an object")
		default:
			tree = parsed
			env.JSONOK = true
		}
	}

	var interpreted contextfabric.InterpretedQuestion
	var capture genkitruntime.InterpretationOutputCapture
	if tree != nil {
		env.DuplicateKeys = nonNil(tree.duplicatePaths())
		findings := inspectShape(tree)
		env.UnknownFields = nonNil(findings.UnknownFields)
		env.CaseVariantKeys = nonNil(findings.CaseVariantKeys)
		env.NullPaths = nonNil(findings.NullPaths)
		env.UntrimmedPaths = nonNil(findings.UntrimmedPaths)
		for _, path := range env.DuplicateKeys {
			env.fail(stageStrict, "duplicate_key", path)
		}
		for _, path := range env.UnknownFields {
			env.fail(stageStrict, "unknown_field", path)
		}
		for _, path := range env.CaseVariantKeys {
			env.fail(stageStrict, "case_variant_key", path)
		}
		for _, path := range env.NullPaths {
			env.fail(stageStrict, "null_value", path)
		}
		for _, path := range env.UntrimmedPaths {
			env.fail(stageStrict, "untrimmed_string", path)
		}

		env.SchemaOK = checkSchema(body, &env)

		var parseOutErr error
		interpreted, capture, parseOutErr = genkitruntime.ParseInterpretationOutputSignals(body, decoded.Request.TimeContext)
		switch {
		case parseOutErr == nil:
			env.DecodeOK, env.DomainOK = true, true
		case interpreted.Validate() != nil && decodedSomething(interpreted):
			// toDomain returns the rejected interpretation with its Validate
			// error, so a populated, invalid value means the JSON decoded and
			// the domain validator refused it.
			env.DecodeOK = true
			reason := contextfabric.InterpretationRejectionReasonOf(contextfabric.ClassifyInterpretationRejection(interpreted, parseOutErr, nil))
			env.RejectionReason = string(reason)
			env.fail(stageDomain, nonEmpty(string(reason), "domain_invalid"), parseOutErr.Error())
		default:
			env.fail(stageDecode, "decode_error", parseOutErr.Error())
		}

		if env.DecodeOK && len(env.DuplicateKeys) == 0 && len(env.UnknownFields) == 0 && len(env.CaseVariantKeys) == 0 {
			canonical, err := canonicalize(tree)
			if err != nil {
				env.fail(stageStrict, "canonicalize_failed", err.Error())
			} else {
				env.CanonicalText = &canonical
			}
		}
	}

	if replay.Err != nil && env.DomainOK && (transport == transportExchange || env.SchemaOK) {
		env.fail(stageInterpreter, "interpreter_rejected", replay.Err.Error())
	}
	switch transport {
	case transportGenkit:
		// genkit's own format handler already applied the schema.
		env.ProductionAccepts = env.ExchangeAccepts
	default:
		env.ProductionAccepts = env.ExchangeAccepts && env.SchemaOK
	}
	env.TurnAdmitted = env.ProductionAccepts && !replay.Outcome.Gate.Refuses()
	checkTransportAgreement(&env, transport, replay, interpreted, decoded.ValidateErr == nil)

	if env.DomainOK {
		env.Frame = buildFrameReport(capture, replay)
		env.Family = buildFamilyReport(replay)
		env.SanitizeClean = reportSanitize(capture, &env)
		if !reportSilentNormalization(tree, interpreted, capture, &env) {
			env.SanitizeClean = false
		}
		if env.Frame.Present && env.Frame.Gate.Refuses {
			env.fail(stageFrame, "frame_gate_refused", fmt.Sprintf("gate=%s invariant=%s basis=%s", env.Frame.Gate.Outcome, env.Frame.Gate.FailedInvariant, env.Frame.Gate.RefuseBasis))
		}
		if !env.Frame.Present {
			env.fail(stageFrame, "frame_absent", "question_frame was not emitted")
		}
	}
	if tree != nil {
		env.Semantic = buildSemantic(tree, interpreted, capture, replay, env)
	}
	env.StrictOK = env.ProductionAccepts && env.TurnAdmitted && env.SanitizeClean && env.CanonicalText != nil &&
		!env.MarkdownFenced &&
		len(env.DuplicateKeys) == 0 && len(env.UnknownFields) == 0 && len(env.CaseVariantKeys) == 0 &&
		len(env.NullPaths) == 0 && len(env.UntrimmedPaths) == 0 && env.Frame != nil && env.Frame.Present
	return env, nil
}

// checkTransportAgreement compares the helper's own reading of the parsed
// text with what the production runtime call decided on the same answer.
// A disagreement means the helper's analysis does not describe what
// production did; it is reported loudly, never reconciled.
func checkTransportAgreement(env *Envelope, transport string, replay replayResult, helperParsed contextfabric.InterpretedQuestion, requestOK bool) {
	if !requestOK || !replay.DirectCalled {
		return
	}
	helperAccepts := env.JSONOK && env.DomainOK
	if transport == transportGenkit {
		helperAccepts = helperAccepts && env.SchemaOK
	}
	productionAccepts := replay.DirectErr == nil
	if helperAccepts != productionAccepts {
		env.fail(stageTransport, "transport_divergence",
			fmt.Sprintf("helper analysis accepts=%v but the %s production call accepts=%v", helperAccepts, transport, productionAccepts))
		return
	}
	if productionAccepts && !reflect.DeepEqual(helperParsed, replay.DirectInterpreted) {
		env.fail(stageTransport, "transport_decode_divergence",
			"the production call decoded a different interpretation than the helper parsed from the same text")
	}
}

// checkSchema validates the raw bytes against the production output schema
// with the same library and loaders genkit's JSON format handler uses
// (genkit internal/base.ValidateRaw -> gojsonschema.Validate).
func checkSchema(body []byte, env *Envelope) bool {
	schema, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		env.fail(stageSchema, "schema_unavailable", err.Error())
		return false
	}
	result, err := gojsonschema.Validate(gojsonschema.NewBytesLoader(schema), gojsonschema.NewBytesLoader(body))
	if err != nil {
		env.fail(stageSchema, "schema_error", err.Error())
		return false
	}
	if result.Valid() {
		return true
	}
	for _, violation := range result.Errors() {
		env.fail(stageSchema, "schema_violation", violation.String())
	}
	return false
}

func decodedSomething(q contextfabric.InterpretedQuestion) bool {
	return q.Shape != "" || q.RequestedJudgment != "" || len(q.SubjectTerms) > 0 || len(q.ComparisonTerms) > 0 ||
		len(q.FactRequirements) > 0 || q.ClarificationNeeded || q.ClarificationReason != "" || q.TimeContext.Axis != ""
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func boolPtr(v bool) *bool { return &v }

func buildFrameReport(capture genkitruntime.InterpretationOutputCapture, replay replayResult) *frameReport {
	report := &frameReport{
		Present:     capture.Frame.Present,
		Obligations: []string{},
		Sanitize:    sanitizeCounters(capture.Frame),
		Gate:        gateFrom(replay.Outcome.Gate),
	}
	if replay.Receipt != nil {
		report.Outcome = string(replay.Receipt.FrameOutcome)
	}
	if event := replay.FrameEvent; event != nil {
		accepted := event.Outcome.Accepted()
		switch {
		case accepted:
			report.A1OK, report.A2OK = boolPtr(true), boolPtr(true)
		case event.FailedPhase == contextfabric.FrameValidationPhaseA1:
			report.A1OK = boolPtr(false)
		case event.FailedPhase == contextfabric.FrameValidationPhaseA2:
			report.A1OK, report.A2OK = boolPtr(true), boolPtr(false)
		}
		if !accepted && event.FailedInvariant != "" {
			report.Failure = &frameFailure{Invariant: string(event.FailedInvariant), Phase: string(event.FailedPhase), Detail: string(event.FailureDetail)}
		}
		if event.Repair.Decision != "" {
			report.Repair = &frameRepair{Decision: string(event.Repair.Decision), Name: string(event.Repair.Name), Invariant: string(event.Repair.Invariant)}
		}
	}
	if replay.Outcome.Frame != nil {
		for _, obligation := range replay.Outcome.FrameObligations {
			report.Obligations = append(report.Obligations, string(obligation))
		}
	}
	return report
}

func buildFamilyReport(replay replayResult) *familyReport {
	outcome := replay.Outcome
	return &familyReport{
		Family:           string(outcome.Family),
		Source:           string(outcome.Source),
		PrecedenceRow:    string(outcome.Winner.Row),
		AttemptedFamily:  string(outcome.Winner.AttemptedFamily),
		Downgraded:       outcome.Winner.Downgraded,
		RouteFamily:      string(outcome.Route.Family),
		RouteSource:      string(outcome.Route.Source),
		RouteClass:       string(outcome.Route.Class),
		RouteDisposition: string(outcome.Route.Disposition),
		RouteSwitched:    outcome.Route.Switched,
	}
}

// reportSanitize records every value the production sanitizers silently
// dropped or truncated. Production accepts these; a clean training target
// must not rely on them.
func reportSanitize(capture genkitruntime.InterpretationOutputCapture, env *Envelope) bool {
	clean := true
	flag := func(condition bool, stage, code string) {
		if condition {
			clean = false
			env.fail(stage, code, "production sanitizer dropped or truncated this value")
		}
	}
	frame := capture.Frame
	flag(frame.GoalsDropped > 0, stageSanitize, "frame_goals_dropped")
	flag(frame.TermsTruncated > 0, stageSanitize, "frame_terms_truncated")
	flag(frame.KindUnrecognized, stageSanitize, "frame_kind_unrecognized")
	flag(frame.TemporalUnrecognized, stageSanitize, "frame_temporal_unrecognized")
	flag(frame.EmphasisDropped > 0, stageSanitize, "frame_emphasis_dropped")
	flag(frame.DimensionsDropped > 0, stageSanitize, "frame_dimensions_dropped")
	flag(frame.MemberKindUnrecognized, stageSanitize, "frame_member_kind_unrecognized")
	flag(frame.GroupKindUnrecognized, stageSanitize, "frame_group_kind_unrecognized")
	flag(frame.MemberQualifierUnrecognized, stageSanitize, "frame_member_qualifier_unrecognized")
	family := capture.Family
	flag(family.FamilyUnrecognized, stageFamily, "question_family_unrecognized")
	flag(family.GroupKindUnrecognized, stageFamily, "group_kind_unrecognized")
	flag(family.ScopeAnchorTermTruncated, stageFamily, "scope_anchor_term_truncated")
	flag(family.ScopeAnchorKindUnrecognized, stageFamily, "scope_anchor_kind_unrecognized")
	flag(family.RequestedKindUnrecognized, stageFamily, "requested_subject_kind_unrecognized")
	flag(capture.Window.ClassUnrecognized, stageWindow, "window_class_unrecognized")
	return clean
}

// reportSilentNormalization records the toDomain/sanitizer normalizations
// that change a value without any receipt counter: an out-of-set
// requested_judgment_kind or window_confidence degrades to "", and blank or
// duplicate terms and duplicate fact kinds are dropped (trimmedUnique and
// the first-occurrence fact-kind loop in runtime.go toDomain).
func reportSilentNormalization(tree *node, q contextfabric.InterpretedQuestion, capture genkitruntime.InterpretationOutputCapture, env *Envelope) bool {
	clean := true
	flag := func(condition bool, code, message string) {
		if condition {
			clean = false
			env.fail(stageSanitize, code, message)
		}
	}
	flag(strings.TrimSpace(rawString(tree, "requested_judgment_kind")) != "" && q.RequestedJudgmentKind == "",
		"requested_judgment_kind_unrecognized", "out-of-set requested_judgment_kind degraded to no pick")
	flag(strings.TrimSpace(rawString(tree, "window_confidence")) != "" && capture.Window.Confidence == "",
		"window_confidence_unrecognized", "out-of-set window_confidence degraded to no pick")
	flag(rawListLen(tree, "subject_terms") != len(q.SubjectTerms),
		"subject_terms_normalized", "blank or duplicate subject_terms were dropped")
	flag(rawListLen(tree, "comparison_terms") != len(q.ComparisonTerms),
		"comparison_terms_normalized", "blank or duplicate comparison_terms were dropped")
	flag(rawListLen(tree, "fact_requirements") != len(q.FactRequirements),
		"fact_requirements_deduplicated", "duplicate fact_requirements kinds were dropped")
	return clean
}
