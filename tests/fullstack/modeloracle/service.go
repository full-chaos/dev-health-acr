package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Service mode answers the engine's own model calls (interpretation, synthesis and offer
// phrasing). Those calls offer no tools, so they never reach the OpenCode script. A plan
// that holds a "service" section opts in; a plan without one behaves exactly as before.
//
// Replies come from the plan, keyed on the exact question text. The one thing the oracle
// never invents is evidence: synthesis cites only what the real request carried, and a plan
// that asks for more than the request holds fails loudly.

const (
	opInterpret   = "interpret"
	opSynthesize  = "synthesize"
	opPhrase      = "phrase"
	opUnknown     = "unknown"
	serviceFailed = "fullstack_model_service_failure.v1"

	stateInFlight  = "in_flight"
	stateAnswered  = "answered"
	stateFailed    = "failed"
	stateCancelled = "cancelled"

	maxCitedEvidence = 20
)

// operationMarkers tell the three operations apart. The engine appends the output schema of
// each operation to its system message (genkitruntime.OutputFormatInstructions), and the
// schema is generated from a Go type, so a property that only one schema holds is a stable
// marker that a prompt edit cannot move. A schema change that breaks a marker makes every
// call fail with an unclassifiable-operation envelope, which the tests pin.
var operationMarkers = map[string]string{
	opInterpret:  "fact_requirements",
	opSynthesize: "deterministic_answer",
	opPhrase:     "phrasings",
}

const schemaFence = "conform to the following schema:\n\n```"

// classifyOperation reads the output schema the engine put in the system message.
func classifyOperation(system string) (string, error) {
	start := strings.Index(system, schemaFence)
	if start < 0 {
		return opUnknown, errors.New("the system message carries no output schema block")
	}
	block := system[start+len(schemaFence):]
	end := strings.LastIndex(block, "```")
	if end < 0 {
		return opUnknown, errors.New("the output schema block is not closed")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal([]byte(block[:end]), &schema); err != nil {
		return opUnknown, fmt.Errorf("the output schema block is not JSON: %w", err)
	}
	var matched []string
	for op, marker := range operationMarkers {
		if _, ok := schema.Properties[marker]; ok {
			matched = append(matched, op)
		}
	}
	if len(matched) != 1 {
		sort.Strings(matched)
		return opUnknown, fmt.Errorf("the output schema matches %d operations %v, want exactly one", len(matched), matched)
	}
	return matched[0], nil
}

// ServicePlan is the "service" section of a fullstack_model_plan.v1 document.
type ServicePlan struct {
	Questions []ServiceQuestion `json:"questions"`
}

// ServiceDelay is how long the oracle holds each kind of call, in milliseconds.
type ServiceDelay struct {
	Interpret  int `json:"interpret"`
	Synthesize int `json:"synthesize"`
	Phrase     int `json:"phrase"`
}

func (d ServiceDelay) forOperation(op string) time.Duration {
	switch op {
	case opInterpret:
		return time.Duration(d.Interpret) * time.Millisecond
	case opSynthesize:
		return time.Duration(d.Synthesize) * time.Millisecond
	case opPhrase:
		return time.Duration(d.Phrase) * time.Millisecond
	}
	return 0
}

// ServiceQuestion is the scripted answer to one exact question.
type ServiceQuestion struct {
	Question string       `json:"question"`
	DelayMS  ServiceDelay `json:"delay_ms"`
	// Interpretation is the interpretation model output, verbatim.
	Interpretation json.RawMessage `json:"interpretation"`
	// Synthesis describes the answer; evidence and subjects come from the real request.
	Synthesis *ServiceSynthesis `json:"synthesis,omitempty"`
	// OfferPhrasing is optional. The phrasing call carries no question, so the entry whose
	// labels cover every offered option answers it.
	OfferPhrasing []ServicePhrasing `json:"offer_phrasing,omitempty"`
}

type ServiceSynthesis struct {
	Status              string        `json:"status"`
	DirectJudgment      string        `json:"direct_judgment"`
	CurrentState        string        `json:"current_state"`
	StrongestPressures  []string      `json:"strongest_pressures"`
	Limitations         []string      `json:"limitations"`
	Driver              ServiceDriver `json:"driver"`
	DeterministicAnswer string        `json:"deterministic_answer"`
	// MinEvidenceRefs is how many evidence refs the request must hold. Less fails loudly.
	MinEvidenceRefs int `json:"min_evidence_refs"`
}

type ServiceDriver struct {
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	Category string `json:"category"`
}

type ServicePhrasing struct {
	Label    string `json:"label"`
	Phrasing string `json:"phrasing"`
}

func (p *ServicePlan) validate() error {
	if len(p.Questions) == 0 {
		return errors.New("service plan needs at least one question")
	}
	seen := map[string]bool{}
	for i, q := range p.Questions {
		if q.Question == "" {
			return fmt.Errorf("service question %d has no question text", i)
		}
		if seen[q.Question] {
			return fmt.Errorf("service question %q is listed twice", q.Question)
		}
		seen[q.Question] = true
		if q.DelayMS.Interpret < 0 || q.DelayMS.Synthesize < 0 || q.DelayMS.Phrase < 0 {
			return fmt.Errorf("service question %q has a negative delay", q.Question)
		}
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(q.Interpretation, &probe); err != nil || len(probe) == 0 {
			return fmt.Errorf("service question %q needs an interpretation object", q.Question)
		}
		if s := q.Synthesis; s != nil {
			switch s.Status {
			case "complete", "partial", "degraded", "clarification_required", "no_match":
			default:
				return fmt.Errorf("service question %q has unknown synthesis status %q", q.Question, s.Status)
			}
			if s.MinEvidenceRefs < 0 || s.MinEvidenceRefs > maxCitedEvidence {
				return fmt.Errorf("service question %q min_evidence_refs must be 0..%d", q.Question, maxCitedEvidence)
			}
		}
		for _, ph := range q.OfferPhrasing {
			if ph.Label == "" || ph.Phrasing == "" {
				return fmt.Errorf("service question %q has an offer_phrasing entry without label and phrasing", q.Question)
			}
		}
	}
	return nil
}

func (p *ServicePlan) find(question string) *ServiceQuestion {
	for i := range p.Questions {
		if p.Questions[i].Question == question {
			return &p.Questions[i]
		}
	}
	return nil
}

// findPhrasing returns the one entry whose labels cover every offered label. None or more
// than one is a failure: a guess here would answer one question's offer with another's.
func (p *ServicePlan) findPhrasing(labels []string) (*ServiceQuestion, error) {
	var found *ServiceQuestion
	for i := range p.Questions {
		q := &p.Questions[i]
		if len(q.OfferPhrasing) == 0 {
			continue
		}
		have := map[string]bool{}
		for _, ph := range q.OfferPhrasing {
			have[ph.Label] = true
		}
		covers := true
		for _, label := range labels {
			if !have[label] {
				covers = false
			}
		}
		if !covers {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("more than one plan entry holds phrasing for the offered labels %q", labels)
		}
		found = q
	}
	if found == nil {
		return nil, fmt.Errorf("no plan entry holds phrasing for the offered labels %q", labels)
	}
	return found, nil
}

// serviceCall is one observed service call.
type serviceCall struct {
	Sequence  int    `json:"sequence"`
	Operation string `json:"operation"`
	Question  string `json:"question"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

type serviceCounts struct {
	Started   int `json:"started"`
	Answered  int `json:"answered"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// serviceSnapshot is what GET /service/calls and the observations file report.
type serviceSnapshot struct {
	Started    int                                 `json:"started"`
	InFlight   int                                 `json:"in_flight"`
	Answered   int                                 `json:"answered"`
	Failed     int                                 `json:"failed"`
	Cancelled  int                                 `json:"cancelled"`
	ByQuestion map[string]map[string]serviceCounts `json:"by_question"`
	Calls      []serviceCall                       `json:"calls"`
}

func (s *server) snapshotService() serviceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := serviceSnapshot{ByQuestion: map[string]map[string]serviceCounts{}, Calls: []serviceCall{}}
	for _, c := range s.serviceCalls {
		snap.Calls = append(snap.Calls, *c)
		snap.Started++
		byOp := snap.ByQuestion[c.Question]
		if byOp == nil {
			byOp = map[string]serviceCounts{}
			snap.ByQuestion[c.Question] = byOp
		}
		n := byOp[c.Operation]
		n.Started++
		switch c.State {
		case stateInFlight:
			snap.InFlight++
		case stateAnswered:
			snap.Answered++
			n.Answered++
		case stateFailed:
			snap.Failed++
			n.Failed++
		case stateCancelled:
			snap.Cancelled++
			n.Cancelled++
		}
		byOp[c.Operation] = n
	}
	return snap
}

func (s *server) handleServiceCalls(w http.ResponseWriter, _ *http.Request) {
	s.touch()
	writeJSON(w, http.StatusOK, s.snapshotService())
}

func (s *server) openServiceCall(sequence int, op, question string) *serviceCall {
	call := &serviceCall{Sequence: sequence, Operation: op, Question: question, State: stateInFlight}
	s.mu.Lock()
	s.serviceCalls = append(s.serviceCalls, call)
	s.mu.Unlock()
	return call
}

func (s *server) closeServiceCall(call *serviceCall, state, reason string) {
	s.mu.Lock()
	call.State, call.Reason = state, reason
	s.mu.Unlock()
}

type serviceMessages struct {
	system, user string
}

func splitMessages(messages []chatMessage) serviceMessages {
	var out serviceMessages
	for _, m := range messages {
		switch m.Role {
		case "system":
			if out.system == "" {
				out.system = textContent(m.Content)
			}
		case "user":
			out.user = textContent(m.Content)
		}
	}
	return out
}

// handleService answers one no-tools call. It holds no lock while it waits, so concurrent
// calls hold their delays side by side.
func (s *server) handleService(w http.ResponseWriter, r *http.Request, request chatRequest, sequence int) {
	responseID := fmt.Sprintf("chatcmpl-context-fabric-%03d", sequence)
	plan := s.plan.Service
	msgs := splitMessages(request.Messages)

	op, opErr := classifyOperation(msgs.system)
	if opErr != nil {
		call := s.openServiceCall(sequence, opUnknown, "")
		s.failService(w, request, responseID, call, opUnknown, "", opErr.Error())
		return
	}

	var (
		entry    *ServiceQuestion
		question string
		lookup   error
	)
	switch op {
	case opPhrase:
		var in struct {
			Options []struct {
				Label string `json:"label"`
			} `json:"options"`
		}
		if err := json.Unmarshal([]byte(msgs.user), &in); err != nil || len(in.Options) == 0 {
			lookup = errors.New("the phrasing request holds no options")
			break
		}
		labels := make([]string, 0, len(in.Options))
		for _, o := range in.Options {
			labels = append(labels, o.Label)
		}
		entry, lookup = plan.findPhrasing(labels)
		if entry != nil {
			question = entry.Question
		}
	default:
		var in struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal([]byte(msgs.user), &in); err != nil || in.Question == "" {
			lookup = errors.New("the user message holds no question")
			break
		}
		question = in.Question
		if entry = plan.find(question); entry == nil {
			lookup = fmt.Errorf("no service plan entry for this question (operation %s)", op)
		}
	}
	call := s.openServiceCall(sequence, op, question)
	if lookup != nil {
		s.failService(w, request, responseID, call, op, question, lookup.Error())
		return
	}

	if delay := entry.DelayMS.forOperation(op); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-r.Context().Done():
			timer.Stop()
			s.closeServiceCall(call, stateCancelled, "request context ended during the delay")
			return
		}
	}

	var (
		reply string
		err   error
	)
	switch op {
	case opInterpret:
		reply = string(compactJSON(entry.Interpretation))
	case opSynthesize:
		reply, err = buildSynthesisReply(entry, msgs.user)
	case opPhrase:
		reply, err = buildPhraseReply(entry, msgs.user)
	}
	if err != nil {
		s.failService(w, request, responseID, call, op, question, err.Error())
		return
	}
	s.closeServiceCall(call, stateAnswered, "")
	s.emitText(w, request, responseID, reply)
}

func (s *server) failService(w http.ResponseWriter, request chatRequest, responseID string, call *serviceCall, op, question, reason string) {
	s.closeServiceCall(call, stateFailed, reason)
	envelope, err := json.Marshal(serviceFailure{SchemaVersion: serviceFailed, Operation: op, Question: question, Reason: reason})
	if err != nil {
		// A string-only struct cannot fail to marshal; if it ever does, say so loudly.
		http.Error(w, "encode service failure envelope: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.emitText(w, request, responseID, string(envelope))
}

// serviceFailure is the named envelope a refused service call carries in place of a reply.
type serviceFailure struct {
	SchemaVersion string `json:"schema_version"`
	Operation     string `json:"operation"`
	Question      string `json:"question"`
	Reason        string `json:"reason"`
}

func compactJSON(raw json.RawMessage) []byte {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return out
}

func appendUnique(list []string, seen map[string]bool, values []string) []string {
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			list = append(list, v)
		}
	}
	return list
}

// buildSynthesisReply builds the synthesis output. The subject and every evidence ref are
// read from the request the engine sent; nothing about them comes from the plan.
func buildSynthesisReply(entry *ServiceQuestion, userMessage string) (string, error) {
	plan := entry.Synthesis
	if plan == nil {
		return "", errors.New("the plan entry has no synthesis section")
	}
	var in struct {
		Resolution struct {
			Committed []json.RawMessage `json:"committed"`
		} `json:"subject_resolution"`
		Facts []struct {
			ID             string                     `json:"id"`
			Kind           string                     `json:"kind"`
			Subject        json.RawMessage            `json:"subject"`
			Fields         map[string]json.RawMessage `json:"fields"`
			EvidenceRefIDs []string                   `json:"evidence_ref_ids"`
		} `json:"canonical_facts"`
		DriverCandidates []struct {
			EvidenceRefIDs []string `json:"evidence_ref_ids"`
		} `json:"driver_candidates"`
		Paths []struct {
			EvidenceRefIDs []string `json:"evidence_ref_ids"`
		} `json:"paths"`
	}
	if err := json.Unmarshal([]byte(userMessage), &in); err != nil {
		return "", fmt.Errorf("the synthesis request is not JSON: %w", err)
	}
	if len(in.Resolution.Committed) == 0 {
		return "", errors.New("the synthesis request holds no committed subject to attribute the driver to")
	}
	var evidence []string
	seen := map[string]bool{}
	for _, f := range in.Facts {
		evidence = appendUnique(evidence, seen, f.EvidenceRefIDs)
	}
	for _, d := range in.DriverCandidates {
		evidence = appendUnique(evidence, seen, d.EvidenceRefIDs)
	}
	for _, p := range in.Paths {
		evidence = appendUnique(evidence, seen, p.EvidenceRefIDs)
	}
	if len(evidence) < plan.MinEvidenceRefs {
		return "", fmt.Errorf("the plan needs %d evidence refs and the request holds %d", plan.MinEvidenceRefs, len(evidence))
	}
	if len(evidence) > maxCitedEvidence {
		evidence = evidence[:maxCitedEvidence]
	}
	if evidence == nil {
		evidence = []string{}
	}
	category := plan.Driver.Category
	if category == "" {
		category = "narrative"
	}
	driver := map[string]any{
		"driver_id": "driver_oracle_01", "standing": "principal", "category": category,
		"title": plan.Driver.Title, "summary": plan.Driver.Summary,
		"affected_subjects": []json.RawMessage{in.Resolution.Committed[0]},
		"evidence_ref_ids":  evidence,
		"derivation":        "graph_associated", "epistemic_status": "inferred", "confidence": 0.5, "current": true,
	}
	drivers := []any{}
	if plan.Driver.Title != "" || plan.Driver.Summary != "" {
		drivers = append(drivers, driver)
	}
	// A model that read canonical facts restates one it relies on. Without a claim the
	// engine redraws once (zero-claim redraw), which would double the synthesis call count.
	// The claim is copied from the request, never written by the plan.
	claims := []any{}
	if claim := firstScalarClaim(in.Facts); claim != nil {
		claims = append(claims, claim)
	}
	reply := map[string]any{
		"status": plan.Status, "direct_judgment": plan.DirectJudgment, "current_state": plan.CurrentState,
		"strongest_pressures": nonNil(plan.StrongestPressures), "drivers": drivers,
		"remaining_work": []any{}, "readiness_gaps": []any{}, "conflicts": []any{},
		"limitations": nonNil(plan.Limitations), "evidence_ref_ids": evidence,
		"claimed_facts": claims, "deterministic_answer": plan.DeterministicAnswer, "warnings": []string{},
	}
	encoded, err := json.Marshal(reply)
	return string(encoded), err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// buildPhraseReply phrases every offered option whose label the plan holds, and no other.
func buildPhraseReply(entry *ServiceQuestion, userMessage string) (string, error) {
	var in struct {
		Options []struct {
			OptionID string `json:"option_id"`
			Label    string `json:"label"`
		} `json:"options"`
	}
	if err := json.Unmarshal([]byte(userMessage), &in); err != nil {
		return "", fmt.Errorf("the phrasing request is not JSON: %w", err)
	}
	byLabel := map[string]string{}
	for _, ph := range entry.OfferPhrasing {
		byLabel[ph.Label] = ph.Phrasing
	}
	type phrasing struct {
		OptionID string `json:"option_id"`
		Phrasing string `json:"phrasing"`
	}
	out := []phrasing{}
	for _, o := range in.Options {
		text, ok := byLabel[o.Label]
		if !ok {
			return "", fmt.Errorf("no phrasing for the offered label %q", o.Label)
		}
		out = append(out, phrasing{OptionID: o.OptionID, Phrasing: text})
	}
	encoded, err := json.Marshal(map[string]any{"phrasings": out})
	return string(encoded), err
}

// firstScalarClaim restates the first scalar field of the first canonical fact that has one,
// in field-name order so the same request always gets the same claim.
func firstScalarClaim[F any](facts []F) map[string]any {
	for _, raw := range facts {
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var f struct {
			ID      string                     `json:"id"`
			Kind    string                     `json:"kind"`
			Subject json.RawMessage            `json:"subject"`
			Fields  map[string]json.RawMessage `json:"fields"`
		}
		if json.Unmarshal(encoded, &f) != nil {
			continue
		}
		names := make([]string, 0, len(f.Fields))
		for name := range f.Fields {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			var value struct {
				String  *string  `json:"string"`
				Integer *int64   `json:"integer"`
				Number  *float64 `json:"number"`
				Boolean *bool    `json:"boolean"`
				Rows    []any    `json:"rows"`
			}
			if json.Unmarshal(f.Fields[name], &value) != nil || len(value.Rows) > 0 {
				continue
			}
			if value.String == nil && value.Integer == nil && value.Number == nil && value.Boolean == nil {
				continue
			}
			claim := map[string]any{"claim_id": "claim_oracle_01", "kind": f.Kind, "subject": f.Subject, "field": name, "value": f.Fields[name]}
			if f.ID != "" {
				claim["fact_id"] = f.ID
			}
			return claim
		}
	}
	return nil
}
