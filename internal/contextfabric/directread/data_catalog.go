package directread

// data_catalog, slice S1a (CHAOS-7036 design C.3, E.5, H). The catalogue a
// client agent reads BEFORE it calls find_subjects or run_operation: what it
// can ask, in which shape, with which limits, FOR THIS CALLER.
//
// Rules this builder keeps, each pinned by a test:
//
//   - Operations come from the loaded policy artifact only (the same
//     *Catalogue the runner refuses with), so the catalogue can never list an
//     operation the runner would refuse as unknown, nor hide one it serves.
//   - The operation list is the one for the caller's class: 19 operations for
//     an unrestricted caller, 2 for a repository-restricted one. Every other
//     registered document is listed under not_served with its code and
//     reason, and every refused variable value (the K14-A
//     basis_dependent_shape and the person-scoped values) under
//     refused_shapes.
//   - A caller without data:read still SEES the operation list (the list is
//     policy, not data), each entry marked available:false with the reason
//     scope_missing_data_read. This closes design "found while merging" item
//     8 (r5 did not say whether a context:read-only caller sees the list).
//   - The caller section carries the scopes present and the grant class. It
//     never carries a repository name, a slug or a count of grants.
//   - The facts section lists the kinds read_facts serves, read from the SAME
//     capability registry read_facts validates against (CHAOS-7148), never
//     from a second list. Kinds are not grant-filtered by read_facts (the
//     subject gate filters subjects), so the list reveals nothing a
//     restricted caller could not learn from a read_facts refusal. When
//     read_facts cannot serve any kind the section says so in a note, never as an
//     empty list.
//
// No model is called and nothing is read from a store: the catalogue is a
// pure function of the policy artifact, the closed vocabularies and the
// caller's class and scopes.

import (
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// DataContractVersion is the contract family of the direct data tools
// (design J.1).
const DataContractVersion = "acr-data.v1"

// Catalog section names (the closed vocabulary of the sections parameter).
const (
	CatalogSectionOperations    = "operations"
	CatalogSectionFacts         = "facts"
	CatalogSectionSubjects      = "subjects"
	CatalogSectionRelationships = "relationships"
	CatalogSectionLimits        = "limits"
)

// CatalogSectionVocabulary is the closed set of catalog sections.
func CatalogSectionVocabulary() [6]string {
	return [6]string{CatalogSectionOperations, CatalogSectionFacts, CatalogSectionSubjects, CatalogSectionRelationships, CatalogSectionLimits, CatalogSectionSchema}
}

// CatalogFactsNote is the fixed note of the facts section when read_facts is
// not wired in this deployment.
const CatalogFactsNote = "facts: read_facts is not available in this deployment"

// CatalogFactsServedNote is the fixed note when read_facts serves the kinds
// listed in the facts section.
const CatalogFactsServedNote = "facts: served by read_facts; kinds listed are the ones it accepts"

// Reasons an operation entry is not available to this caller now.
const (
	CatalogUnavailableScopeMissing      = "scope_missing_data_read"
	CatalogUnavailableQueryNotConfigure = "data_query_not_configured"
	// CatalogUnavailableGateMissing: the runner exists but no subject gate
	// over a real graph is composed, so the route answers 503 and the tool
	// is not advertised (CHAOS-7075 pr2 r2).
	CatalogUnavailableGateMissing = "subject_gate_unavailable"
)

// DataCatalogUntrustedNotice is the fixed label every direct data answer
// carries (the same text as the investigation tools' label).
const DataCatalogUntrustedNotice = contractsv1.MCPUntrustedContentNotice

// operationPurposes is the short, static purpose text of each served
// operation. It is authored here, not generated: the ops registry has no
// purpose text. Every served operation must have one (a test holds the key
// set equal to the catalogue's served set), at most 200 characters.
var operationPurposes = map[string]string{
	"acrRepositoryScopes":            "The organization's repository slugs as the ops query service knows them, for mapping a slug to a repository.",
	"capacityCompletionDistribution": "A team's forecast completion distribution only: simulated completion days and item counts, without the dates or the target.",
	"capacityForecast":               "An on-demand Monte Carlo forecast of when a work scope completes; each call draws a new random seed.",
	"capacityForecasts":              "Stored capacity forecasts, as a list, with their inputs and percentile dates.",
	"catalogValues":                  "The distinct values of one catalogue dimension (repository, team, theme, subcategory or work type).",
	"cognitiveLoad":                  "A team's cognitive load series over a window.",
	"complexityTimeseries":           "Code complexity over time per repository or file, for the repositories named.",
	"compoundingRisk":                "Compounding delivery risk per repository or team per day, with the trend and the component scores.",
	"hotspots":                       "Files with high change and complexity (hotspots) in a window, per repository.",
	"investmentBreakdown":            "Investment (work themes and subcategories) as a time series or an org-level breakdown for a window.",
	"investmentFull":                 "Investment over a window: time series, org-level breakdowns and the batch coverage disclosure.",
	"home":                           "The org-wide home summary: data freshness, metric deltas, tiles, signals, the limiting factor and data confidence.",
	"recommendations":                "A team's stored recommendations over a lookback window, each with its rationale and evidence rows.",
	"workItemTeamAttributions":       "Raw team attribution facts per work item, with source, confidence, primary flag and evidence text.",
	"securityOverview":               "Security posture: open alert counts by severity, the trend and 30-day indicators.",
	"throughputForecast":             "A throughput forecast with its history sufficiency flag.",
	"workGraphArtifacts":             "Work graph artifacts (issues, pull requests and more) in a window, with a degraded-reason disclosure.",
	"workGraphEdges":                 "Work graph edges between artifacts in a window, with a degraded-reason disclosure.",
	"workGraphFlow":                  "Work graph flow between stages in a window, with a degraded-reason disclosure.",
}

// OperationPurpose returns the authored purpose of an operation, or "".
func OperationPurpose(name string) string { return operationPurposes[name] }

// CatalogCaller is what the builder may know about the caller: its grant
// class, the scopes present on its credential, and whether run_operation
// can serve in this deployment. It carries no repository name and no grant.
type CatalogCaller struct {
	PrincipalClass PrincipalClass
	// Scopes are the credential's scopes (any order, duplicates allowed).
	Scopes []string
	// DataRead reports the data:read scope.
	DataRead bool
	// OperationsServable reports that the query service URL is configured
	// and the policy artifact loaded (design E.5).
	OperationsServable bool
	// FactCapabilities is the registry read_facts serves, or nil when
	// read_facts is not wired. Only DirectServable kinds are listed.
	FactCapabilities []contextfabric.FactCapability
	// GraphQL is the graphql_query root policy (nil when it did not derive)
	// and GraphQLServable reports its runner is composed (CHAOS-7075).
	GraphQL         *GraphQLPolicy
	GraphQLServable bool
	// GateComposed reports the subject gate over a real graph; without it
	// run_operation and graphql_query cannot authorize a subject.
	GateComposed bool
	// FactsServable is true when a read_facts reader object is composed. The
	// section still says "not available" unless the registry it reads lists
	// at least one direct-servable kind.
	FactsServable bool
}

// DataCatalogRequest selects sections. Empty means all served sections.
type DataCatalogRequest struct {
	Sections []string
}

// DataCatalog is the data_catalog answer.
type DataCatalog struct {
	ContractVersion  string                `json:"contract_version"`
	Sections         []string              `json:"sections"`
	Operations       *CatalogOperations    `json:"operations,omitempty"`
	Facts            *CatalogFacts         `json:"facts,omitempty"`
	Subjects         *CatalogSubjects      `json:"subjects,omitempty"`
	Relationships    *CatalogRelationships `json:"relationships,omitempty"`
	Limits           *CatalogLimits        `json:"limits,omitempty"`
	Schema           *CatalogSchema        `json:"schema,omitempty"`
	Versions         CatalogVersions       `json:"versions"`
	Caller           CatalogCallerView     `json:"caller"`
	Consistency      string                `json:"consistency"`
	UntrustedContent CatalogUntrustedLabel `json:"untrusted_content"`
	Notes            []string              `json:"notes,omitempty"`
}

// CatalogUntrustedLabel is the untrusted-content label.
type CatalogUntrustedLabel struct {
	Untrusted bool     `json:"untrusted"`
	Notice    string   `json:"notice"`
	Fields    []string `json:"fields"`
}

// CatalogOperations is the operations section.
type CatalogOperations struct {
	CallerClass   CallerClass           `json:"caller_class"`
	Available     bool                  `json:"available"`
	Reason        string                `json:"reason,omitempty"`
	Operations    []CatalogOperation    `json:"operations"`
	NotServed     []CatalogNotServed    `json:"not_served"`
	RefusedShapes []CatalogRefusedShape `json:"refused_shapes"`
}

// CatalogOperation is one operation served to this caller class.
type CatalogOperation struct {
	Name              string            `json:"name"`
	Purpose           string            `json:"purpose"`
	Available         bool              `json:"available"`
	Reason            string            `json:"reason,omitempty"`
	ScopeClass        ScopeClass        `json:"scope_class"`
	ForcedVariable    string            `json:"forced_variable,omitempty"`
	ScopeVariables    []string          `json:"scope_variables"`
	CostClass         CostClass         `json:"cost_class"`
	ResponseRoot      string            `json:"response_root"`
	DisclosureFields  []string          `json:"disclosure_fields"`
	DeadlineSeconds   int               `json:"deadline_seconds"`
	MaxInFlightPerOrg int               `json:"max_in_flight_per_org,omitempty"`
	DocumentDigest    string            `json:"document_digest"`
	Variables         []CatalogVariable `json:"variables"`
	Notes             []string          `json:"notes,omitempty"`
}

// CatalogVariable is one client-settable variable path.
type CatalogVariable struct {
	Path        string   `json:"path"`
	Type        string   `json:"type"`
	EnumValues  []string `json:"enum_values,omitempty"`
	Default     string   `json:"default,omitempty"`
	Min         *int64   `json:"min,omitempty"`
	Max         *int64   `json:"max,omitempty"`
	MaxItems    int      `json:"max_items,omitempty"`
	MaxLength   int      `json:"max_length,omitempty"`
	SubjectKind string   `json:"subject_kind,omitempty"`
}

// CatalogNotServed is one registered operation this caller cannot run.
type CatalogNotServed struct {
	Name   string      `json:"name"`
	Code   RefusalCode `json:"code"`
	Reason string      `json:"reason"`
}

// CatalogRefusedShape is one refused variable value of a served operation.
type CatalogRefusedShape struct {
	Operation string      `json:"operation"`
	Path      string      `json:"path"`
	Value     string      `json:"value"`
	Code      RefusalCode `json:"code"`
	Reason    string      `json:"reason"`
}

// CatalogFacts is the facts section: the kinds read_facts serves.
type CatalogFacts struct {
	Served bool              `json:"served"`
	Note   string            `json:"note"`
	Kinds  []CatalogFactKind `json:"kinds,omitempty"`
}

// CatalogFactKind is one fact kind read_facts accepts.
type CatalogFactKind struct {
	Kind         string   `json:"kind"`
	SubjectKinds []string `json:"subject_kinds"`
	Fields       []string `json:"fields"`
}

// buildCatalogFacts derives the facts section from the registry read_facts
// serves. A kind read_facts refuses (no declared fields) is not listed.
func buildCatalogFacts(caller CatalogCaller) *CatalogFacts {
	if !caller.FactsServable {
		return &CatalogFacts{Served: false, Note: CatalogFactsNote}
	}
	kinds := []CatalogFactKind{}
	for _, capability := range caller.FactCapabilities {
		if !capability.DirectServable() {
			continue
		}
		entry := CatalogFactKind{Kind: string(capability.Kind), SubjectKinds: []string{}, Fields: []string{}}
		for _, subject := range capability.SupportedSubjectKinds {
			entry.SubjectKinds = append(entry.SubjectKinds, string(subject))
		}
		for _, field := range capability.Fields {
			entry.Fields = append(entry.Fields, field.Name)
		}
		sort.Strings(entry.SubjectKinds)
		kinds = append(kinds, entry)
	}
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].Kind < kinds[j].Kind })
	// A reader object with no source, or a source that lists nothing
	// servable, answers every read_facts call as unavailable or refused:
	// that is "not available", never "served" with an empty list.
	if len(kinds) == 0 {
		return &CatalogFacts{Served: false, Note: CatalogFactsNote}
	}
	return &CatalogFacts{Served: true, Note: CatalogFactsServedNote, Kinds: kinds}
}

// CatalogSubjects is the subjects section.
type CatalogSubjects struct {
	Kinds []CatalogSubjectKind `json:"kinds"`
}

// CatalogSubjectKind is one subject kind of the closed vocabulary.
type CatalogSubjectKind struct {
	Kind            string `json:"kind"`
	Listable        bool   `json:"listable"`
	DefaultNameKind bool   `json:"default_name_kind"`
}

// CatalogRelationships is the relationships section.
type CatalogRelationships struct {
	Note  string                `json:"note"`
	Types []CatalogRelationship `json:"types"`
}

// CatalogRelationship is one relationship type with its end kinds.
type CatalogRelationship struct {
	Type        string   `json:"type"`
	SourceKinds []string `json:"source_kinds"`
	TargetKinds []string `json:"target_kinds"`
	// Produced is false for a type in the closed vocabulary that no
	// projection source of this build writes.
	Produced bool `json:"produced"`
}

// CatalogLimits is the limits section.
type CatalogLimits struct {
	MaxBytesDefault        int `json:"max_bytes_default"`
	MaxBytesCap            int `json:"max_bytes_cap"`
	OperationRequestBytes  int `json:"operation_request_bytes"`
	DeadlineSeconds        int `json:"deadline_seconds"`
	FindPageDefault        int `json:"find_page_default"`
	FindPageMax            int `json:"find_page_max"`
	FindNameKindsMax       int `json:"find_name_kinds_max"`
	FindQueryMaxRunes      int `json:"find_query_max_runes"`
	FindListScanMax        int `json:"find_list_scan_max"`
	GrantedRepositoriesMax int `json:"granted_repositories_max"`
}

// CatalogVersions pins what the catalogue was built from.
type CatalogVersions struct {
	Contract          string `json:"contract"`
	CatalogueContract string `json:"catalogue_contract"`
	OpsRepository     string `json:"ops_repository"`
	OpsSourceSHA      string `json:"ops_source_sha"`
	SchemaDigest      string `json:"schema_digest"`
}

// CatalogCallerView is the caller section: scopes and class only.
type CatalogCallerView struct {
	Scopes     []string    `json:"scopes"`
	GrantClass CallerClass `json:"grant_class"`
}

// catalogRelationshipEnds declares the end kinds of the 13 relationship
// types. Sources: devhealthsource/clickhouse.go:119-135 (BELONGS_TO_*,
// CORRELATED_WITH_INCIDENT, PART_OF and the work_item_dependencies types),
// devhealthsource/tables.go:841 (PART_OF), devhealthsource/
// teams_projects_edges.go:47,540,616,1494,1707 (BELONGS_TO_PROJECT,
// OWNED_BY_TEAM). DOCUMENTED_BY and HAS_EPISODE are in the closed vocabulary
// but no projection source of this build writes them (produced=false).
var catalogRelationshipEnds = map[contractsv1.ContextFabricRelationshipType]CatalogRelationship{
	contractsv1.ContextFabricRelationshipBelongsToRepository:    {SourceKinds: []string{"ci_pipeline_run", "deployment", "incident", "pull_request", "work_item"}, TargetKinds: []string{"repository"}, Produced: true},
	contractsv1.ContextFabricRelationshipBelongsToPullRequest:   {SourceKinds: []string{"pull_request_review"}, TargetKinds: []string{"pull_request"}, Produced: true},
	contractsv1.ContextFabricRelationshipCorrelatedWithIncident: {SourceKinds: []string{"deployment"}, TargetKinds: []string{"incident"}, Produced: true},
	contractsv1.ContextFabricRelationshipRelatedTo:              {SourceKinds: []string{"work_item"}, TargetKinds: []string{"work_item"}, Produced: true},
	contractsv1.ContextFabricRelationshipDocumentedBy:           {SourceKinds: []string{}, TargetKinds: []string{"document"}, Produced: false},
	contractsv1.ContextFabricRelationshipHasEpisode:             {SourceKinds: []string{}, TargetKinds: []string{"episode"}, Produced: false},
	contractsv1.ContextFabricRelationshipBlocks:                 {SourceKinds: []string{"work_item"}, TargetKinds: []string{"work_item"}, Produced: true},
	contractsv1.ContextFabricRelationshipPartOf:                 {SourceKinds: []string{"work_item"}, TargetKinds: []string{"work_item"}, Produced: true},
	contractsv1.ContextFabricRelationshipRelatesTo:              {SourceKinds: []string{"work_item"}, TargetKinds: []string{"work_item"}, Produced: true},
	contractsv1.ContextFabricRelationshipDuplicates:             {SourceKinds: []string{"work_item"}, TargetKinds: []string{"work_item"}, Produced: true},
	contractsv1.ContextFabricRelationshipBelongsToProject:       {SourceKinds: []string{"pull_request", "work_item"}, TargetKinds: []string{"project"}, Produced: true},
	contractsv1.ContextFabricRelationshipOwnedByTeam:            {SourceKinds: []string{"project", "repository", "work_item"}, TargetKinds: []string{"team"}, Produced: true},
	contractsv1.ContextFabricRelationshipLinksPullRequest:       {SourceKinds: []string{"work_item"}, TargetKinds: []string{"pull_request"}, Produced: true},
}

// catalogRelationshipOrder is the wire order of the relationship types.
var catalogRelationshipOrder = []contractsv1.ContextFabricRelationshipType{
	contractsv1.ContextFabricRelationshipBelongsToRepository,
	contractsv1.ContextFabricRelationshipBelongsToPullRequest,
	contractsv1.ContextFabricRelationshipCorrelatedWithIncident,
	contractsv1.ContextFabricRelationshipRelatedTo,
	contractsv1.ContextFabricRelationshipDocumentedBy,
	contractsv1.ContextFabricRelationshipHasEpisode,
	contractsv1.ContextFabricRelationshipBlocks,
	contractsv1.ContextFabricRelationshipPartOf,
	contractsv1.ContextFabricRelationshipRelatesTo,
	contractsv1.ContextFabricRelationshipDuplicates,
	contractsv1.ContextFabricRelationshipBelongsToProject,
	contractsv1.ContextFabricRelationshipOwnedByTeam,
	contractsv1.ContextFabricRelationshipLinksPullRequest,
}

// CatalogRelationshipNote is the fixed meaning line of the relationships
// section (design C.6).
const CatalogRelationshipNote = "An edge shows a relation. It does not show a cause."

// ErrCatalogSection: the sections parameter names a section that does not
// exist. The route answers invalid_request.
type ErrCatalogSection struct{}

func (ErrCatalogSection) Error() string { return "data_catalog: unknown section" }

// ParseCatalogSections reads the comma list of the sections parameter. An
// empty list is every section. An unknown name is an error; duplicates are
// dropped; the order is the vocabulary order.
func ParseCatalogSections(raw string) ([]string, error) {
	vocab := CatalogSectionVocabulary()
	if strings.TrimSpace(raw) == "" {
		return vocab[:], nil
	}
	want := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		known := false
		for _, v := range vocab {
			if v == name {
				known = true
			}
		}
		if !known {
			return nil, ErrCatalogSection{}
		}
		want[name] = true
	}
	out := []string{}
	for _, v := range vocab {
		if want[v] {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, ErrCatalogSection{}
	}
	return out, nil
}

// BuildDataCatalog builds the catalogue for one caller. catalogue may be nil
// (no policy loaded): the operations section then lists nothing and says
// data_query_not_configured. sections must come from ParseCatalogSections.
func BuildDataCatalog(catalogue *Catalogue, caller CatalogCaller, sections []string) DataCatalog {
	class := CallerClassFor(caller.PrincipalClass)
	out := DataCatalog{
		ContractVersion: DataContractVersion,
		Sections:        append([]string{}, sections...),
		Versions:        CatalogVersions{Contract: DataContractVersion, CatalogueContract: OperationCatalogueContract},
		Caller:          CatalogCallerView{Scopes: catalogScopes(caller.Scopes), GrantClass: class},
		Consistency:     ConsistencyBestEffort,
		UntrustedContent: CatalogUntrustedLabel{
			Untrusted: true, Notice: DataCatalogUntrustedNotice,
			Fields: []string{"operations.operations[].notes", "operations.not_served[].reason", "operations.refused_shapes[].reason"},
		},
	}
	if catalogue != nil {
		source := catalogue.Source()
		out.Versions.OpsRepository = source.Repository
		out.Versions.OpsSourceSHA = source.Commit
		out.Versions.SchemaDigest = catalogue.StampedSchemaDigest()
	}
	for _, section := range sections {
		switch section {
		case CatalogSectionOperations:
			out.Operations = buildCatalogOperations(catalogue, caller, class)
		case CatalogSectionFacts:
			out.Facts = buildCatalogFacts(caller)
			out.Notes = append(out.Notes, out.Facts.Note)
		case CatalogSectionSubjects:
			out.Subjects = buildCatalogSubjects()
		case CatalogSectionRelationships:
			out.Relationships = buildCatalogRelationships()
		case CatalogSectionSchema:
			out.Schema = BuildCatalogSchema(caller.GraphQL, class, caller.GraphQLServable, caller.GateComposed, caller.DataRead)
		case CatalogSectionLimits:
			out.Limits = &CatalogLimits{
				MaxBytesDefault: DefaultOperationMaxBytes, MaxBytesCap: MaxOperationMaxBytes,
				OperationRequestBytes: MaxQueryRequestBytes, DeadlineSeconds: catalogDeadlineSeconds(catalogue),
				FindPageDefault: DefaultFindLimit, FindPageMax: MaxFindLimit, FindNameKindsMax: MaxFindKinds,
				FindQueryMaxRunes: MaxFindQueryRunes, FindListScanMax: MaxFindScanNodes,
				GrantedRepositoriesMax: MaxGrantedRepositories,
			}
		}
	}
	return out
}

// catalogScopes keeps the known scope tokens only, sorted and unique. A
// scope is a short fixed token; anything else is dropped, never echoed.
func catalogScopes(scopes []string) []string {
	set := map[string]bool{}
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" || len(scope) > 32 || strings.ContainsAny(scope, " /\t\n") {
			continue
		}
		set[scope] = true
	}
	out := make([]string, 0, len(set))
	for scope := range set {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

func catalogDeadlineSeconds(catalogue *Catalogue) int {
	deadline := 30
	if catalogue == nil {
		return deadline
	}
	for _, op := range catalogue.Operations(CallerUnrestricted) {
		if op.DeadlineSeconds > deadline {
			deadline = op.DeadlineSeconds
		}
	}
	return deadline
}

func buildCatalogOperations(catalogue *Catalogue, caller CatalogCaller, class CallerClass) *CatalogOperations {
	section := &CatalogOperations{CallerClass: class, Available: true, Operations: []CatalogOperation{}, NotServed: []CatalogNotServed{}, RefusedShapes: []CatalogRefusedShape{}}
	reason := ""
	switch {
	case catalogue == nil || !caller.OperationsServable:
		reason = CatalogUnavailableQueryNotConfigure
	case !caller.GateComposed:
		reason = CatalogUnavailableGateMissing
	case !caller.DataRead:
		reason = CatalogUnavailableScopeMissing
	}
	if reason != "" {
		section.Available, section.Reason = false, reason
	}
	// No detail for a caller run_operation cannot serve (CHAOS-7075 class
	// sweep of the pr2 r1 P1): without data:read, or without a composed
	// runner, the section says why and lists no operation, variable,
	// not-served entry or refused shape.
	if catalogue == nil || reason != "" {
		return section
	}
	served := map[string]bool{}
	for _, op := range catalogue.Operations(class) {
		served[op.Name] = true
		section.Operations = append(section.Operations, catalogOperation(op, class, reason))
	}
	// Operations served to another class only: not served to this caller.
	for _, op := range catalogue.Operations(CallerUnrestricted) {
		if served[op.Name] {
			continue
		}
		scope := op.Scope(class)
		entry := CatalogNotServed{Name: op.Name, Code: RefusalOperationNotServedForCaller, Reason: "operation is not served to this caller class"}
		if scope.Refusal != nil {
			entry.Code, entry.Reason = scope.Refusal.Code, scope.Refusal.Reason
		}
		section.NotServed = append(section.NotServed, entry)
	}
	for _, ns := range catalogue.NotServed() {
		section.NotServed = append(section.NotServed, CatalogNotServed{Name: ns.Name, Code: ns.Code, Reason: ns.Reason})
	}
	sort.Slice(section.NotServed, func(i, j int) bool { return section.NotServed[i].Name < section.NotServed[j].Name })
	// Refused variable values of every served operation (any class): the
	// K14-A basis_dependent_shape and the person-scoped values.
	for _, op := range catalogue.Operations(CallerUnrestricted) {
		for _, rule := range op.Variables {
			for _, refused := range rule.RefusedValues {
				section.RefusedShapes = append(section.RefusedShapes, CatalogRefusedShape{
					Operation: op.Name, Path: rule.Path, Value: refused.Value, Code: refused.Code, Reason: refused.Reason,
				})
			}
		}
	}
	return section
}

func catalogOperation(op *OperationPolicy, class CallerClass, reason string) CatalogOperation {
	scope := op.Scope(class)
	entry := CatalogOperation{
		Name: op.Name, Purpose: OperationPurpose(op.Name), Available: reason == "", Reason: reason,
		ScopeClass: ScopeOrgWide, ScopeVariables: []string{}, CostClass: op.CostClass,
		DisclosureFields: []string{}, DeadlineSeconds: op.DeadlineSeconds, MaxInFlightPerOrg: op.MaxInFlightPerOrg,
		DocumentDigest: op.Digest, Variables: []CatalogVariable{}, Notes: append([]string(nil), op.Notes...),
	}
	if class == CallerRestricted {
		entry.ScopeClass = ScopeForcedGrant
		entry.ForcedVariable = scope.ForcedVariablePath
	}
	if len(op.Outputs) > 0 {
		root := op.Outputs[0].Path
		if i := strings.IndexAny(root, ".["); i >= 0 {
			root = root[:i]
		}
		entry.ResponseRoot = root
	}
	for _, d := range op.Disclosure {
		entry.DisclosureFields = append(entry.DisclosureFields, d.Path)
	}
	for _, rule := range op.Variables {
		if !rule.Allowed || rule.Source != SourceClient || rule.Kind == VariableKindObject {
			continue
		}
		// A restricted caller's forced path stays listed: the caller may
		// name a subset of its grant there; scope_class says acr bounds it.
		v := CatalogVariable{Path: rule.Path, Type: rule.Type, Default: rule.EffectiveDefault(), Min: rule.Min, Max: rule.Max, MaxItems: rule.MaxItems, MaxLength: rule.MaxLength}
		if rule.Kind == VariableKindEnum {
			v.EnumValues = append([]string{}, rule.AllowedValues...)
		}
		if rule.Subject != nil {
			v.SubjectKind = rule.Subject.Kind
			entry.ScopeVariables = append(entry.ScopeVariables, rule.Path)
		}
		entry.Variables = append(entry.Variables, v)
	}
	return entry
}

func buildCatalogSubjects() *CatalogSubjects {
	defaults := map[string]bool{}
	for _, kind := range DefaultLookupNameKinds() {
		defaults[kind] = true
	}
	section := &CatalogSubjects{}
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		// find_subjects list mode serves every kind of the closed
		// vocabulary (planFind accepts any valid kind).
		section.Kinds = append(section.Kinds, CatalogSubjectKind{Kind: string(kind), Listable: true, DefaultNameKind: defaults[string(kind)]})
	}
	return section
}

func buildCatalogRelationships() *CatalogRelationships {
	section := &CatalogRelationships{Note: CatalogRelationshipNote}
	for _, t := range catalogRelationshipOrder {
		ends := catalogRelationshipEnds[t]
		section.Types = append(section.Types, CatalogRelationship{
			Type: string(t), SourceKinds: append([]string{}, ends.SourceKinds...), TargetKinds: append([]string{}, ends.TargetKinds...), Produced: ends.Produced,
		})
	}
	return section
}
