package hosted

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// dataReads is the S1a direct data composition (CHAOS-7072): the loaded
// operation policy, the run_operation runner and the find_subjects lookup.
// Each part is optional and nil when it cannot serve.
type dataReads struct {
	catalogue  *directread.Catalogue
	operations api.DataOperationRunner
	subjects   api.DataSubjectFinder
}

// dataReadsCatalogue is the policy loader; a variable so a test can plant a
// load failure. Production always loads the embedded artifact.
var dataReadsCatalogue = directread.DefaultCatalogue

// buildDataReads composes the S1a data tools (design E.5, E.8).
//
//   - The operation policy is loaded always (data_catalog lists it). A load
//     failure FAILS STARTUP only when the internal query service URL is
//     configured -- an operator asked for run_operation and it cannot
//     serve. Without the URL the failure is logged loudly and the catalogue
//     is absent.
//   - find_subjects needs the graph: the engine's graph adapter, which must
//     be a directread.SubjectGraph, and the S0 subject gate. Without them
//     the subjects route answers 503.
//   - run_operation needs the URL AND the loaded policy; otherwise the
//     operations route answers feature_not_enabled (data_query_not_configured).
//     Its gate is the S0 gate; when no graph is composed the runner gets a
//     gate over no graph, which fails every decision closed, and the API
//     answers 503 because DirectReadGate is nil. A restricted caller that
//     names no repository is scoped to its grant through the lookup
//     (GrantedRepositories) when the graph exists.
//
// No partial composition is silent: every absent part is logged once.
func buildDataReads(queryURL string, queryTimeout time.Duration, investigator contextfabric.Investigator, gate *directread.SubjectGate, logger *slog.Logger) (dataReads, error) {
	if logger == nil {
		logger = slog.Default()
	}
	var out dataReads
	configured := strings.TrimSpace(queryURL) != ""

	catalogue, err := dataReadsCatalogue()
	switch {
	case err != nil && configured:
		return dataReads{}, fmt.Errorf("initialize data operation catalogue: %w", err)
	case err != nil:
		logger.Error("context fabric direct data composition", "decision", "not_loaded", "reason", "catalogue_invalid")
	default:
		out.catalogue = catalogue
	}

	var graph directread.SubjectGraph
	if engine, ok := investigator.(directReadSourcer); ok && !storage.IsNil(investigator) {
		source, _ := engine.DirectReadSources()
		if subjectGraph, ok := source.(directread.SubjectGraph); ok && !storage.IsNil(source) {
			graph = subjectGraph
		}
	}
	var grants directread.GrantedRepositories
	if graph != nil && gate != nil {
		out.subjects = directread.NewSubjectLookup(graph, gate, directread.NewSlogFindRecorder(logger))
		// The grant listing runs its own lookup with no find_subjects
		// telemetry: it is not a find_subjects call. The gate still records
		// every decision it takes.
		grants = directread.NewGrantedRepositories(directread.NewSubjectLookup(graph, gate, nil))
	} else {
		logger.Warn("context fabric direct data subjects not composed", "reason", "graph_or_gate_absent")
	}

	if !configured || out.catalogue == nil {
		logger.Info("context fabric direct data composition", "decision", "operations_off", "reason", "data_query_not_configured")
		return out, nil
	}
	client, err := directread.NewHTTPQueryClient(queryURL, queryTimeout)
	if err != nil {
		return dataReads{}, fmt.Errorf("initialize data query client: %w", err)
	}
	runnerGate := gate
	if runnerGate == nil {
		runnerGate = directread.NewSubjectGate(nil, directread.NewSlogRecorder(logger))
	}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
		Catalogue: out.catalogue, Gate: runnerGate, Client: client, Grants: grants, Logger: logger,
	})
	if err != nil {
		return dataReads{}, fmt.Errorf("initialize data operation runner: %w", err)
	}
	out.operations = runner
	logger.Info("context fabric direct data composition", "decision", "loaded", "schema_digest", out.catalogue.SchemaDigest())
	return out, nil
}
