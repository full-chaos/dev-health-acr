package v1

import (
	"fmt"
	"strings"
)

func (r ContextFabricInvestigationRequest) Validate() error {
	if r.SchemaVersion != ContextFabricInvestigationRequestSchema {
		return fmt.Errorf("schema_version must be %q", ContextFabricInvestigationRequestSchema)
	}
	if !stringLengthBetween(r.RequestID, 8, 256) || !stringLengthBetween(strings.TrimSpace(r.Question), 1, 8000) {
		return fmt.Errorf("request_id or question violates v1 bounds")
	}
	if len(r.Conversation) > 50 || len(r.PriorSubjectReceipts) > 20 {
		return fmt.Errorf("conversation or prior subject receipts exceed v1 bounds")
	}
	seenTurns := make(map[string]struct{}, len(r.Conversation))
	for _, turn := range r.Conversation {
		if err := turn.Validate(); err != nil {
			return fmt.Errorf("conversation: %w", err)
		}
		if _, exists := seenTurns[turn.TurnID]; exists {
			return fmt.Errorf("conversation turn_id must be unique")
		}
		seenTurns[turn.TurnID] = struct{}{}
	}
	seenReceipts := make(map[string]struct{}, len(r.PriorSubjectReceipts))
	for _, receipt := range r.PriorSubjectReceipts {
		if err := receipt.Validate(); err != nil {
			return fmt.Errorf("prior_subject_receipts: %w", err)
		}
		key := receipt.ResultID + "\x00" + receipt.ReceiptID
		if _, exists := seenReceipts[key]; exists {
			return fmt.Errorf("prior_subject_receipts must be unique")
		}
		seenReceipts[key] = struct{}{}
	}
	if err := r.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("requested_scope: %w", err)
	}
	if err := r.TimeContext.Validate(); err != nil {
		return fmt.Errorf("time_context: %w", err)
	}
	if err := r.Options.Validate(); err != nil {
		return fmt.Errorf("options: %w", err)
	}
	if err := r.Consumer.Validate(); err != nil {
		return fmt.Errorf("consumer: %w", err)
	}
	return nil
}

func (t ContextFabricConversationTurn) Validate() error {
	if !stringLengthBetween(t.TurnID, 1, 256) || !stringLengthBetween(strings.TrimSpace(t.Content), 1, 12000) || t.CreatedAt.IsZero() {
		return fmt.Errorf("turn identity, content, or timestamp violates v1 bounds")
	}
	switch t.Role {
	case ContextFabricConversationUser, ContextFabricConversationAssistant:
		return nil
	default:
		return fmt.Errorf("role is invalid")
	}
}

func (r ContextFabricBoundSubjectReceipt) Validate() error {
	if !stringLengthBetween(r.ResultID, 8, 256) || !stringLengthBetween(r.ReceiptID, 8, 256) {
		return fmt.Errorf("bound subject receipt violates v1 bounds")
	}
	return nil
}

func (s ContextFabricRequestedScope) Validate() error {
	if len(s.RepositorySlugs) > 200 || len(s.ProjectIDs) > 200 || len(s.TeamIDs) > 200 || len(s.SubjectHints) > 50 {
		return fmt.Errorf("scope exceeds v1 bounds")
	}
	if !uniqueTrimmedStrings(s.RepositorySlugs, 512) || !uniqueTrimmedStrings(s.ProjectIDs, 256) || !uniqueTrimmedStrings(s.TeamIDs, 256) {
		return fmt.Errorf("scope identifiers violate v1 bounds")
	}
	for _, hint := range s.SubjectHints {
		if err := hint.Validate(); err != nil {
			return fmt.Errorf("subject_hints: %w", err)
		}
	}
	return nil
}

func (h ContextFabricSubjectHint) Validate() error {
	if !validContextFabricSubjectKind(h.Kind) || !stringLengthBetween(h.ID, 0, 256) || !stringLengthBetween(h.Label, 0, 512) || !stringLengthBetween(h.Source, 1, 64) {
		return fmt.Errorf("subject hint violates v1 bounds")
	}
	if strings.TrimSpace(h.ID) != h.ID || strings.TrimSpace(h.Label) != h.Label || strings.TrimSpace(h.Source) != h.Source || (h.ID == "" && h.Label == "") {
		return fmt.Errorf("subject hint identity is invalid")
	}
	return nil
}

func (t ContextFabricTimeContext) Validate() error {
	switch t.Axis {
	case ContextFabricTemporalCurrent:
		if t.AsOf != nil || t.Start != nil || t.End != nil {
			return fmt.Errorf("current time context cannot include explicit timestamps")
		}
	case ContextFabricTemporalValidTime, ContextFabricTemporalObservedTime:
		if t.AsOf == nil || t.AsOf.IsZero() || t.Start != nil || t.End != nil {
			return fmt.Errorf("point-in-time context requires only as_of")
		}
	case ContextFabricTemporalRange:
		if t.AsOf != nil || t.Start == nil || t.End == nil || t.Start.IsZero() || t.End.IsZero() || t.End.Before(*t.Start) {
			return fmt.Errorf("range context requires an ordered start and end")
		}
	default:
		return fmt.Errorf("time axis is invalid")
	}
	return nil
}

func (o ContextFabricInvestigationOptions) Validate() error {
	if o.MaxSubjectCandidates < 1 || o.MaxSubjectCandidates > 50 ||
		o.MaxCohortMembers < 1 || o.MaxCohortMembers > 250 ||
		o.MaxRelationshipPaths < 1 || o.MaxRelationshipPaths > 250 ||
		o.MaxDrivers < 1 || o.MaxDrivers > 50 ||
		o.MaxEvidenceRefs < 1 || o.MaxEvidenceRefs > 500 ||
		o.MaxSerializedBytes < 8192 || o.MaxSerializedBytes > 1<<20 {
		return fmt.Errorf("investigation options violate v1 bounds")
	}
	return nil
}

func (c ContextFabricConsumerInfo) Validate() error {
	if !stringLengthBetween(c.Name, 1, 200) || !stringLengthBetween(c.Version, 1, 200) || !stringLengthBetween(c.Surface, 1, 200) {
		return fmt.Errorf("consumer metadata violates v1 bounds")
	}
	if strings.TrimSpace(c.Name) != c.Name || strings.TrimSpace(c.Version) != c.Version || strings.TrimSpace(c.Surface) != c.Surface {
		return fmt.Errorf("consumer metadata must be trimmed")
	}
	return nil
}
