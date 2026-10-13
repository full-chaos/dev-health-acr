package v1

import "testing"

func TestOutcomeRowReadKeepsPairingWithAnAddedCause(t *testing.T) {
	r := loadFixture[ContextFabricInvestigationResult](t, resultStatusCorrect)
	row := r.Completeness.Outcomes[0]
	row.Outcome = ContextFabricRequirementNarrowed
	row.Impact = ContextFabricAnswerImpactNone
	row.CauseCoverage = ContextFabricCoverageDetailCode(addedVocabularyValue)
	if err := validatePlanRequirementOutcomeRow(row, contextFabricLegacyBounds); err == nil {
		t.Fatal("a narrowed row with impact none is accepted on a read because its cause is an added token")
	}
}

func TestWindowOptionReadRequiresBoundsForAnUnknownRelativeID(t *testing.T) {
	for _, id := range []ContextFabricRelativeWindowID{ContextFabricRelativeWindowID(addedVocabularyValue), ContextFabricRelativeWindowTrailing90D} {
		r := loadFixture[ContextFabricInvestigationResult](t, resultV2)
		r.StructureNeeds.WindowOptions = []ContextFabricWindowOption{{ReceiptID: "winr_added_0001", OptionID: "opt_added", Label: "added", RelativeID: id}}
		if err := ValidateStoredResult(r); err == nil {
			t.Fatalf("a stored window option with relative_id %q and no frozen bounds is accepted on a read", id)
		}
	}
}

func TestCommitDigestReadRequiresAGateForAnAddedLookup(t *testing.T) {
	r := loadFixture[ContextFabricInvestigationResult](t, resultV1)
	digest := r.SubjectResolution.CommitDecisionDigests[0]
	digest.IdentityLookup = addedVocabularyValue
	digest.CommitGate = ""
	digest.IdentityProven = false
	if err := digest.validate(contextFabricLegacyBounds); err == nil {
		t.Fatal("an added identity_lookup without a commit gate is accepted on a read")
	}
}

func TestProjectionRejectsAnUnknownCompletenessState(t *testing.T) {
	p := loadFixture[ContextFabricAnswerProjection](t, "context_fabric_answer_projection.v1.json")
	if err := p.Validate(); err != nil {
		t.Fatalf("the golden projection must be valid: %v", err)
	}
	p.Completeness.State = "future_state"
	if err := p.Validate(); err == nil {
		t.Fatal("the projection accepts an unknown completeness state on the write path")
	}
	if err := p.validateRead(); err == nil {
		t.Fatal("the projection accepts an unknown completeness state on a read")
	}
}

func TestSharedValidatorsStayClosedForProducers(t *testing.T) {
	cases := []struct {
		name     string
		validate func() error
	}{
		{"evidence_ref.provenance", func() error {
			e := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
			e.Provenance = addedVocabularyValue
			return e.Validate()
		}},
		{"evidence_ref.availability", func() error {
			e := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
			e.Availability = EvidenceAvailability(addedVocabularyValue)
			return e.Validate()
		}},
		{"expanded_evidence.availability", func() error {
			e := loadFixture[ExpandedEvidence](t, "expanded_evidence.v1.json")
			e.Availability = EvidenceAvailability(addedVocabularyValue)
			return e.Validate()
		}},
		{"context_packet.resolved_scope.resolution", func() error {
			p := loadFixture[ContextPacket](t, "context_packet.v1.json")
			p.ResolvedScope.Resolution = ScopeResolution(addedVocabularyValue)
			return p.Validate()
		}},
		{"context_packet.freshness.watermarks.status", func() error {
			p := loadFixture[ContextPacket](t, "context_packet.v1.json")
			p.Freshness.Watermarks[0].Status = addedVocabularyValue
			return p.Validate()
		}},
		{"context_packet_item.category", func() error {
			i := loadFixture[ContextPacketItem](t, "context_packet_item.v1.json")
			i.Category = PacketCategory(addedVocabularyValue)
			return i.Validate()
		}},
		{"agent_episode.redaction_state", func() error {
			e := loadFixture[AgentEpisode](t, "agent_episode.v1.json")
			e.RedactionState = addedVocabularyValue
			return e.Validate()
		}},
		{"record_episode.redaction_state", func() error {
			r := loadFixture[MCPRecordEpisodeResponse](t, "mcp_record_episode_response.v1.json")
			r.RedactionState = addedVocabularyValue
			return r.Validate()
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.validate() == nil {
				t.Fatal("the producer-side Validate accepts a value outside the served vocabulary")
			}
		})
	}
}

func TestOutcomeRowReadRejectsAnEqualCountNarrowingWithAnAddedCause(t *testing.T) {
	for _, mutate := range []func(*ContextFabricPlanRequirementOutcomeRow){
		func(row *ContextFabricPlanRequirementOutcomeRow) {
			row.CauseCoverage = ContextFabricCoverageDetailCode(addedVocabularyValue)
		},
		func(row *ContextFabricPlanRequirementOutcomeRow) {
			row.CauseCoverage = ContextFabricCoverageDetailFactUnconfigured
			row.Obligation = addedVocabularyValue
			row.Requirement = addedVocabularyValue + "/subject/project"
		},
	} {
		r := loadFixture[ContextFabricInvestigationResult](t, resultStatusCorrect)
		row := r.Completeness.Outcomes[0]
		row.Outcome = ContextFabricRequirementNarrowed
		row.Impact = ContextFabricAnswerImpactScope
		row.Stage = ContextFabricOutcomeStageAssembledResult
		row.CauseObserved = true
		row.Served, row.Declared = 5, 5
		mutate(&row)
		if err := validatePlanRequirementOutcomeRow(row, contextFabricLegacyBounds); err == nil {
			t.Fatalf("a narrowed row that served everything it declared is accepted on a read because a token is added: %+v", row)
		}
	}
}

func TestConfirmedStructureReadBoundsReceiptIdentityForAnAddedSource(t *testing.T) {
	r := loadFixture[ContextFabricInvestigationResult](t, resultV1)
	entry := r.ConfirmedStructure[0]
	entry.Source = ContextFabricStructureSource(addedVocabularyValue)
	if err := entry.validate(contextFabricLegacyBounds); err != nil {
		t.Fatalf("the base entry with an added source must pass: %v", err)
	}
	for _, mutate := range []func(*ContextFabricConfirmedStructureEntry){
		func(e *ContextFabricConfirmedStructureEntry) { e.ReceiptID = "short" },
		func(e *ContextFabricConfirmedStructureEntry) { e.PriorResultID = "short" },
	} {
		bad := entry
		mutate(&bad)
		if err := bad.validate(contextFabricLegacyBounds); err == nil {
			t.Fatalf("an added source with an out-of-bounds receipt identity is accepted on a read: %+v", bad)
		}
	}
}

// grownFactKinds is every fact kind this build knows plus one the server added.
func grownFactKinds() []ContextFabricFactKind {
	kinds := ContextFabricFactKindVocabulary()
	return append(append([]ContextFabricFactKind{}, kinds[:]...), ContextFabricFactKind(addedVocabularyValue))
}

func TestFactKindListsReadAcceptAGrownVocabulary(t *testing.T) {
	plan := addedVocabularyPlan()
	plan.FactKinds = grownFactKinds()
	if err := plan.validate(contextFabricLegacyBounds); err != nil {
		t.Errorf("answer_plan.fact_kinds: a read rejects a list one longer than this build's vocabulary: %v", err)
	}
	read := maximalPlanRequirementAt("evidence", "subject", ContextFabricSubjectProject, nil)
	read.Step, read.StepExecution, read.InputClass, read.InputFactKinds = "", "", "", nil
	read.Kind = contextFabricObligationKindRead
	read.FactKinds = grownFactKinds()
	compute := maximalPlanRequirementAt("evidence", "subject", ContextFabricSubjectProject, grownFactKinds())
	for name, row := range map[string]ContextFabricPlanRequirement{"fact_kinds": read, "input_fact_kinds": compute} {
		if err := validatePlanRequirements([]ContextFabricPlanRequirement{row}, contextFabricLegacyBounds); err != nil {
			t.Errorf("plan_requirement.%s: a read rejects a list one longer than this build's vocabulary: %v", name, err)
		}
	}
	q := minimalInterpretation()
	for _, kind := range grownFactKinds() {
		q.FactRequirements = append(q.FactRequirements, ContextFabricFactRequirement{Kind: kind})
	}
	if err := q.validate(contextFabricLegacyBounds); err != nil {
		t.Errorf("interpretation.fact_requirements: a read rejects one requirement per kind of a grown vocabulary: %v", err)
	}
	if err := q.Validate(); err == nil {
		t.Error("the write path accepts an interpretation naming a fact kind outside the vocabulary")
	}
}
