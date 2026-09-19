package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// plantSubjects builds a value of type t with every slice of length one, every
// pointer set, every map holding one entry, and a distinct subject in every
// subject-shaped struct position the type can hold. It returns how many it
// planted. The positions come from the TYPE, so a subject-bearing field added
// to the contract is planted here without a change to this test.
func plantSubjects(t reflect.Type, next *int, onPath map[reflect.Type]bool) reflect.Value {
	value := reflect.New(t).Elem()
	if onPath[t] {
		return value
	}
	switch t.Kind() {
	case reflect.Pointer:
		onPath[t] = true
		value.Set(plantSubjects(t.Elem(), next, onPath).Addr())
		delete(onPath, t)
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return value
		}
		onPath[t] = true
		value = reflect.Append(reflect.MakeSlice(t, 0, 1), plantSubjects(t.Elem(), next, onPath))
		delete(onPath, t)
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return value
		}
		onPath[t] = true
		value = reflect.MakeMap(t)
		value.SetMapIndex(reflect.ValueOf(fmt.Sprintf("k%d", *next)).Convert(t.Key()), plantSubjects(t.Elem(), next, onPath))
		delete(onPath, t)
	case reflect.Struct:
		onPath[t] = true
		for index := 0; index < t.NumField(); index++ {
			if !t.Field(index).IsExported() {
				continue
			}
			value.Field(index).Set(plantSubjects(t.Field(index).Type, next, onPath))
		}
		delete(onPath, t)
		if kind, id := value.FieldByName("Kind"), value.FieldByName("CanonicalID"); kind.IsValid() && id.IsValid() && kind.Type() == subjectKindType && id.Type() == stringType {
			*next++
			kind.SetString(string(SubjectProject))
			id.SetString(fmt.Sprintf("planted-%d", *next))
		}
	}
	return value
}

// Every position a stored result can name a subject in is decided: the
// collector finds exactly the subjects planted across the whole result type.
func TestStoredResultSubjectsCoverEveryPositionTheResultTypeHas(t *testing.T) {
	planted := 0
	result := plantSubjects(reflect.TypeOf(InvestigationResult{}), &planted, map[reflect.Type]bool{}).Interface().(InvestigationResult)
	if planted < 14 {
		t.Fatalf("planted %d subject positions; the result type holds at least the fourteen stored positions measured on the trial store", planted)
	}
	got := StoredResultSubjects(result)
	if len(got) != planted {
		t.Fatalf("collected %d of %d planted subjects", len(got), planted)
	}
	// Committed, candidates, cohort members and group subjects are among them.
	for _, want := range []SubjectRef{result.SubjectResolution.Committed[0], result.SubjectResolution.Candidates[0].Subject, result.Cohort.Members[0].Subject, result.Cohort.Groups[0].Subject, result.Paths[0].Nodes[0], result.ClaimedFacts[0].Subject} {
		found := false
		for _, subject := range got {
			if subject.Kind == want.Kind && subject.CanonicalID == want.CanonicalID {
				found = true
			}
		}
		if !found {
			t.Errorf("subject %s not collected", want.CanonicalID)
		}
	}
}

// gateGraph answers the gate's two graph calls as the test states.
type gateGraph struct {
	*capturingGraphReader
	outcomes map[string]StoredSubjectOutcome
	bindErr  error
	readErr  error
	short    bool
	calls    int
}

func (g *gateGraph) ResolveInvestigationBinding(ctx context.Context, principal storage.Principal) (ResolvedGraphBinding, error) {
	if g.bindErr != nil {
		return ResolvedGraphBinding{}, g.bindErr
	}
	return g.capturingGraphReader.ResolveInvestigationBinding(ctx, principal)
}

func (g *gateGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	g.calls++
	if g.readErr != nil {
		return nil, g.readErr
	}
	out := make([]StoredSubjectOutcome, 0, len(subjects))
	for _, subject := range subjects {
		outcome, ok := g.outcomes[SubjectMapKey(subject)]
		if !ok {
			outcome = StoredSubjectAbsent
		}
		out = append(out, outcome)
	}
	if g.short && len(out) > 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}

func gateResult(subjects ...SubjectRef) InvestigationResult {
	result := validInvestigationResult()
	result.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: subjects}
	return result
}

// TestStoredResultGateDecisionTable enumerates the gate's own inputs: the
// principal's scope class, the subject classes (graph, organization, group),
// every per-subject outcome, and every plane failure.
func TestStoredResultGateDecisionTable(t *testing.T) {
	restricted := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools"}}
	universal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}
	unrestricted := storage.Principal{OrgID: "org_1"}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:a", Label: "A"}
	member := SubjectRef{Kind: SubjectProject, CanonicalID: "project:m", Label: "M"}
	group := SubjectRef{Kind: SubjectTeam, CanonicalID: "AUTH", Label: "AUTH"}
	ownOrg := SubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "org_1", Label: "org"}
	ownOrgKeyed := SubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "organization:org_1", Label: "org"}
	otherOrg := SubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "organization:org_2", Label: "org2"}
	grouped := func(memberIDs ...string) InvestigationResult {
		result := gateResult(project)
		result.Cohort = &contractsv1.ContextFabricCohort{
			Kind:    SubjectProject,
			Members: []contractsv1.ContextFabricCohortMember{{Subject: member}},
			Groups:  []contractsv1.ContextFabricCohortGroup{{Subject: group, MemberCanonicalIDs: memberIDs}},
		}
		// A driver naming the group key is the group, not a graph subject.
		result.Drivers = []DriverJudgment{{AffectedSubjects: []SubjectRef{group}}}
		return result
	}

	cases := []struct {
		name       string
		principal  storage.Principal
		result     InvestigationResult
		graph      *gateGraph
		noGraph    bool
		decision   StoredResultDecision
		reason     StoredResultAuthorizationReason
		graphCalls int
	}{
		{"no_subjects/restricted", restricted, gateResult(), &gateGraph{}, false, StoredResultAdmitted, StoredResultReasonNoSubjects, 0},
		{"unrestricted/no_graph_read", unrestricted, gateResult(project), &gateGraph{readErr: errors.New("never called")}, false, StoredResultAdmitted, StoredResultReasonUnrestrictedPrincipal, 0},
		{"restricted/admitted", restricted, gateResult(project), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}, false, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted, 1},
		{"universal/admitted", universal, gateResult(project), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}, false, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted, 1},
		{"restricted/denied", restricted, gateResult(project), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectDenied}}, false, StoredResultDenied, StoredResultReasonSubjectDenied, 1},
		{"restricted/absent", restricted, gateResult(project), &gateGraph{}, false, StoredResultDenied, StoredResultReasonSubjectAbsent, 1},
		{"restricted/denied_beats_absent", restricted, gateResult(project, member), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(member): StoredSubjectDenied}}, false, StoredResultDenied, StoredResultReasonSubjectDenied, 1},
		{"org/own_bare", restricted, gateResult(ownOrg), &gateGraph{readErr: errors.New("never called")}, false, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted, 0},
		{"org/own_keyed", restricted, gateResult(ownOrgKeyed), &gateGraph{readErr: errors.New("never called")}, false, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted, 0},
		{"org/other", unrestricted, gateResult(otherOrg), &gateGraph{}, false, StoredResultDenied, StoredResultReasonOrganizationMismatch, 0},
		{"group/proven", restricted, grouped(member.CanonicalID), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted, SubjectMapKey(member): StoredSubjectAdmitted}}, false, StoredResultAdmitted, StoredResultReasonSubjectsAdmitted, 1},
		{"group/member_refused", restricted, grouped(member.CanonicalID), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted, SubjectMapKey(member): StoredSubjectDenied}}, false, StoredResultDenied, StoredResultReasonSubjectDenied, 1},
		{"group/no_members", restricted, grouped(), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted, SubjectMapKey(member): StoredSubjectAdmitted}}, false, StoredResultDenied, StoredResultReasonGroupUnproven, 1},
		{"group/unlisted_member", restricted, grouped("project:elsewhere"), &gateGraph{outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted, SubjectMapKey(member): StoredSubjectAdmitted}}, false, StoredResultDenied, StoredResultReasonGroupUnproven, 1},
		{"group/unrestricted_proven", unrestricted, grouped(member.CanonicalID), &gateGraph{}, false, StoredResultAdmitted, StoredResultReasonUnrestrictedPrincipal, 0},
		{"plane/no_authorizer", restricted, gateResult(project), nil, true, StoredResultUnavailable, StoredResultReasonAuthorizerMissing, 0},
		{"plane/bind_not_projected", restricted, gateResult(project), &gateGraph{bindErr: fmt.Errorf("wrapped: %w", ErrGraphNotProjected)}, false, StoredResultDenied, StoredResultReasonGraphNotProjected, 0},
		{"plane/bind_failed", restricted, gateResult(project), &gateGraph{bindErr: errors.New("epoch store down")}, false, StoredResultUnavailable, StoredResultReasonGraphReadFailed, 0},
		{"plane/read_not_projected", restricted, gateResult(project), &gateGraph{readErr: ErrGraphNotProjected}, false, StoredResultDenied, StoredResultReasonGraphNotProjected, 1},
		{"plane/read_failed", restricted, gateResult(project), &gateGraph{readErr: context.DeadlineExceeded}, false, StoredResultUnavailable, StoredResultReasonGraphReadFailed, 1},
		{"plane/short_answer", restricted, gateResult(project), &gateGraph{short: true, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectAdmitted}}, false, StoredResultUnavailable, StoredResultReasonGraphReadFailed, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var graph GraphReader = &capturingGraphReader{}
			if !c.noGraph {
				c.graph.capturingGraphReader = &capturingGraphReader{}
				graph = c.graph
			}
			decision := NewStoredResultGate(graph).Authorize(context.Background(), c.principal, StoredInvestigationResult{Result: c.result}, StoredResultSurfaceResultByID)
			if decision.Decision != c.decision || decision.Reason != c.reason {
				t.Fatalf("decision = %s/%s, want %s/%s (%+v)", decision.Decision, decision.Reason, c.decision, c.reason, decision)
			}
			if !c.noGraph && c.graph.calls != c.graphCalls {
				t.Fatalf("graph lookups = %d, want %d", c.graph.calls, c.graphCalls)
			}
			err := decision.ServingError()
			switch c.decision {
			case StoredResultAdmitted:
				if err != nil {
					t.Fatalf("ServingError() = %v", err)
				}
			case StoredResultDenied:
				if !errors.Is(err, ErrInvestigationResultNotFound) {
					t.Fatalf("ServingError() = %v, want not found", err)
				}
			default:
				if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvestigationResultNotFound) {
					t.Fatalf("ServingError() = %v, want unavailable", err)
				}
			}
			wantScope := StoredResultScopeRestricted
			switch {
			case len(c.principal.RepositoryScopes) == 0:
				wantScope = StoredResultScopeUnrestricted
			case c.principal.RepositoryScopes[0] == "*":
				wantScope = StoredResultScopeUniversal
			}
			if decision.Surface != StoredResultSurfaceResultByID || decision.RepositoryScopeCount != len(c.principal.RepositoryScopes) || decision.PrincipalScope != wantScope {
				t.Fatalf("decision identity = %+v, want scope %s", decision, wantScope)
			}
		})
	}
}

// The trace carries the refused kinds and counts, never an id or a label.
func TestStoredResultAuthorizationLogArgsCarryNoIdentity(t *testing.T) {
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project:secret-id", Label: "Secret Label"}
	graph := &gateGraph{capturingGraphReader: &capturingGraphReader{}, outcomes: map[string]StoredSubjectOutcome{SubjectMapKey(project): StoredSubjectDenied}}
	principal := storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"acme/tools", "acme/web"}}
	decision := NewStoredResultGate(graph).Authorize(context.Background(), principal, StoredInvestigationResult{Result: gateResult(project)}, StoredResultSurfacePriorResult)
	args := StoredResultAuthorizationLogArgs(principal, decision)
	rendered := fmt.Sprint(args...)
	for _, secret := range []string{project.CanonicalID, project.Label} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("trace carries %q: %s", secret, rendered)
		}
	}
	fields := map[string]any{}
	for index := 0; index+1 < len(args); index += 2 {
		fields[args[index].(string)] = args[index+1]
	}
	want := map[string]any{
		"org_id": "org_1", "surface": "prior_result", "principal_scope": "restricted", "repository_scope_count": 2,
		"decision": "denied", "reason": "subject_denied", "subject_count": 1, "graph_subject_count": 1,
		"admitted_count": 0, "denied_count": 1, "absent_count": 0, "organization_subject_count": 0,
		"organization_mismatch_count": 0, "group_count": 0, "group_unproven_count": 0,
	}
	for key, value := range want {
		if !reflect.DeepEqual(fields[key], value) {
			t.Errorf("%s = %#v, want %#v", key, fields[key], value)
		}
	}
	if kinds, _ := fields["refused_kinds"].([]string); !reflect.DeepEqual(kinds, []string{"project"}) {
		t.Errorf("refused_kinds = %#v", fields["refused_kinds"])
	}
	if _, ok := fields["error_class"]; ok {
		t.Error("error_class written for a decision with no plane failure")
	}
	graph.readErr = context.DeadlineExceeded
	failed := NewStoredResultGate(graph).Authorize(context.Background(), principal, StoredInvestigationResult{Result: gateResult(project)}, StoredResultSurfacePriorResult)
	failedArgs := StoredResultAuthorizationLogArgs(principal, failed)
	if got := failedArgs[len(failedArgs)-1]; got != "deadline_exceeded" {
		t.Errorf("error_class = %v", got)
	}
}

// The collector's own input domain, over local shapes the contract does not
// carry today: nil and set pointers, interfaces, arrays, maps, byte slices,
// unexported fields, look-alike structs whose Kind is a plain string, empty
// and blank identities, and duplicates.
func TestStoredResultSubjectCollectorDomain(t *testing.T) {
	type plainKind struct {
		Kind        string
		CanonicalID string
	}
	type shape struct {
		Nil        *SubjectRef
		Set        *SubjectRef
		Iface      any
		NilIface   any
		Array      [1]SubjectRef
		Map        map[string]SubjectRef
		Bytes      []byte
		Plain      plainKind
		EmptyKind  SubjectRef
		EmptyID    SubjectRef
		BlankID    SubjectRef
		Duplicate  SubjectRef
		unexported SubjectRef
	}
	subject := func(id string) SubjectRef { return SubjectRef{Kind: SubjectProject, CanonicalID: id} }
	set := subject("set")
	value := shape{
		Set: &set, Iface: subject("iface"), Array: [1]SubjectRef{subject("array")},
		Map: map[string]SubjectRef{"k": subject("map")}, Bytes: []byte("project"),
		Plain: plainKind{Kind: "project", CanonicalID: "plain"}, EmptyKind: SubjectRef{CanonicalID: "empty-kind"},
		EmptyID: SubjectRef{Kind: SubjectProject}, BlankID: SubjectRef{Kind: SubjectProject, CanonicalID: "  "},
		Duplicate: subject("set"), unexported: subject("unexported"),
	}
	var got []string
	seen := map[string]bool{}
	collectStoredResultSubjects(reflect.ValueOf(value), func(s SubjectRef) {
		if s.Kind == "" || strings.TrimSpace(s.CanonicalID) == "" || seen[SubjectMapKey(s)] {
			return
		}
		seen[SubjectMapKey(s)] = true
		got = append(got, s.CanonicalID)
	})
	want := []string{"set", "iface", "array", "map"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collected %v, want %v", got, want)
	}
	// The exported collector applies the identity filter and deduplication.
	result := validInvestigationResult()
	result.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{set, set, {Kind: SubjectProject, CanonicalID: " "}, {CanonicalID: "no-kind"}}}
	result.ClaimedFacts = []ClaimedFact{{Subject: SubjectRef{}}}
	if subjects := StoredResultSubjects(result); len(subjects) != 1 || subjects[0] != set {
		t.Fatalf("StoredResultSubjects = %#v, want the one real subject once", subjects)
	}
}

// Guards restating an invariant held elsewhere, killed at the predicate.
func TestStoredResultPredicateGuards(t *testing.T) {
	if organizationSubjectIsCallers(storage.Principal{}, SubjectRef{Kind: contractsv1.ContextFabricSubjectOrganization, CanonicalID: "organization:"}) {
		t.Fatal("a principal with no organization matched an organization subject")
	}
	// A member id the cohort does not list is unproven even if some admitted
	// key happens to match it with no kind.
	cohort := &contractsv1.ContextFabricCohort{Members: []contractsv1.ContextFabricCohortMember{{Subject: SubjectRef{Kind: SubjectProject, CanonicalID: "listed"}}}}
	group := contractsv1.ContextFabricCohortGroup{Subject: SubjectRef{Kind: SubjectTeam, CanonicalID: "T"}, MemberCanonicalIDs: []string{"unlisted"}}
	if groupProvenByMembers(group, cohort, map[string]struct{}{SubjectMapKey(SubjectRef{CanonicalID: "unlisted"}): {}}) {
		t.Fatal("a group listing a member the cohort does not carry was proven")
	}
	if groupProvenByMembers(group, nil, map[string]struct{}{}) {
		t.Fatal("a group without a cohort was proven")
	}
	for err, want := range map[error]string{
		context.DeadlineExceeded: "deadline_exceeded", context.Canceled: "canceled",
		fmt.Errorf("x: %w", ErrUnavailable): "dependency_unavailable", errors.New("other"): "graph_error",
	} {
		if got := storedResultErrorClass(err); got != want {
			t.Errorf("storedResultErrorClass(%v) = %s, want %s", err, got, want)
		}
	}
	unavailable := StoredResultAuthorization{Decision: StoredResultUnavailable, Reason: StoredResultReasonGraphReadFailed, Err: context.DeadlineExceeded}
	if err := unavailable.ServingError(); !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ServingError() = %v, want unavailable wrapping the plane failure", err)
	}
	if err := (StoredResultAuthorization{Decision: StoredResultUnavailable, Reason: StoredResultReasonAuthorizerMissing}).ServingError(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ServingError() = %v, want unavailable", err)
	}
}
