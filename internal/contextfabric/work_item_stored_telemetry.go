package contextfabric

import (
	"context"
	"fmt"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// WorkItemStoredServingEvent separates acceptance of a stored candidate from
// successful delivery. It carries only closed decisions and bounded counts.
type WorkItemStoredServingEvent struct {
	Surface                                                  StoredAnswerabilitySurface
	Basis                                                    string
	SemanticRead                                             SemanticStateReadStatus
	CensusRead                                               WorkItemTupleCensusReadStatus
	DetailsBefore, ReasonsBefore, DetailsAfter, ReasonsAfter int
}

func newWorkItemStoredServingEvent(surface StoredAnswerabilitySurface, read SemanticStateReadStatus, result InvestigationResult) WorkItemStoredServingEvent {
	return WorkItemStoredServingEvent{Surface: surface, SemanticRead: read, CensusRead: WorkItemTupleCensusReadAbsent, DetailsBefore: len(result.Coverage.Details), ReasonsBefore: len(result.Coverage.DegradedReasons), DetailsAfter: len(result.Coverage.Details), ReasonsAfter: len(result.Coverage.DegradedReasons)}
}

// Validate only the coverage changed during tuple serving. Other historical
// result fields keep their existing read-time rules. Mandatory degradation
// pairs never yield to make room for D47; an unrepresentable answer is an error.
func validateWorkItemStoredCoverage(result InvestigationResult, event *WorkItemStoredServingEvent) error {
	event.DetailsAfter = len(result.Coverage.Details)
	event.ReasonsAfter = len(result.Coverage.DegradedReasons)
	if result.Coverage.Validate() != nil {
		event.Basis = "coverage_invalid"
		return fmt.Errorf("%w: stored work item coverage invalid", ErrInvalidResult)
	}
	return nil
}

const WorkItemStoredServingLogMessage = "context fabric work item stored serving"

func WorkItemStoredServingLogArgs(event WorkItemStoredServingEvent) []any {
	return []any{"surface", string(event.Surface), "basis", SanitizeLogAttr(event.Basis), "semantic_read", string(event.SemanticRead), "census_read", string(event.CensusRead), "coverage_details_before", event.DetailsBefore, "coverage_reasons_before", event.ReasonsBefore, "coverage_details_after", event.DetailsAfter, "coverage_reasons_after", event.ReasonsAfter, "coverage_bound", contractsv1.ContextFabricCoverageEntriesMaxCount}
}

func (t SlogEngineTelemetry) RecordWorkItemStoredServing(ctx context.Context, principal storage.Principal, event WorkItemStoredServingEvent) {
	args := WorkItemStoredServingLogArgs(event)
	args = append(args, "org_id", SanitizeLogAttr(principal.OrgID))
	args = append(args, requestIDLogAttrs(ctx)...)
	t.logger.InfoContext(ctx, WorkItemStoredServingLogMessage, args...)
}
