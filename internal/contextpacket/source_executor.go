package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	dhgoclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// ClickHouseRowScanner is a type alias for the generic row-scanning
// primitive now owned by the shared dev-health-go library (CHAOS-4377). It
// is the exact same type, so every existing call site keeps compiling
// unchanged.
type ClickHouseRowScanner = dhgoclickhouse.RowScanner

type ClickHouseQueryClient interface {
	Query(context.Context, string, []ClickHouseBinding) (ClickHouseRowScanner, error)
}

type ClickHouseSourceExecutor struct{ client ClickHouseQueryClient }

const sourceEvidenceRowLimit = 100

type SourceQueryPhase string

const (
	SourceQueryPhaseUnknown   SourceQueryPhase = "unknown"
	SourceQueryPhaseQuery     SourceQueryPhase = "query"
	SourceQueryPhaseScan      SourceQueryPhase = "scan"
	SourceQueryPhaseIteration SourceQueryPhase = "iteration"
	SourceQueryPhaseClose     SourceQueryPhase = "close"
)

type sourceExecutionError struct {
	phase    SourceQueryPhase
	sourceID string
	cause    error
}

func (e *sourceExecutionError) Error() string {
	return string(e.phase) + " source " + e.sourceID
}

func (e *sourceExecutionError) Unwrap() error { return e.cause }

func NewClickHouseSourceExecutor(client ClickHouseQueryClient) *ClickHouseSourceExecutor {
	return &ClickHouseSourceExecutor{client: client}
}

func (e *ClickHouseSourceExecutor) QueryEvidence(ctx context.Context, query SourceQuery, bindings []ClickHouseBinding) (_ []contractsv1.EvidenceRef, err error) {
	if e == nil || e.client == nil {
		return nil, fmt.Errorf("contextpacket: clickhouse query client is required")
	}
	// Preserve the leading evidenceBefore order before canonical deduplication;
	// goal and category ranking run after all source queries are merged.
	statement := fmt.Sprintf(
		"SELECT * FROM (%s) WHERE ({include_low_confidence:UInt8} = 1 OR confidence >= %g) "+
			"ORDER BY multiIf(provenance = 'native', 0, provenance = 'explicit_text', 1, provenance = 'derived', 2, provenance = 'heuristic', 3, 4) ASC, observed_at DESC, evidence_ref_id ASC "+
			"LIMIT 1 BY system, entity_type, entity_id LIMIT {source_row_limit:UInt32}",
		query.Statement,
		minimumEvidenceConfidence,
	)
	queryBindings := make([]ClickHouseBinding, len(bindings), len(bindings)+1)
	copy(queryBindings, bindings)
	queryBindings = append(queryBindings, ClickHouseBinding{Name: "source_row_limit", Value: uint32(sourceEvidenceRowLimit)})
	rows, err := e.client.Query(ctx, statement, queryBindings)
	if err != nil {
		return nil, &sourceExecutionError{phase: SourceQueryPhaseQuery, sourceID: query.ID, cause: err}
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = &sourceExecutionError{phase: SourceQueryPhaseClose, sourceID: query.ID, cause: closeErr}
		}
	}()
	evidence := []contractsv1.EvidenceRef{}
	for len(evidence) < sourceEvidenceRowLimit && rows.Next() {
		ref, scanErr := scanEvidenceRow(rows)
		if scanErr != nil {
			return nil, &sourceExecutionError{phase: SourceQueryPhaseScan, sourceID: query.ID, cause: scanErr}
		}
		evidence = append(evidence, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, &sourceExecutionError{phase: SourceQueryPhaseIteration, sourceID: query.ID, cause: err}
	}
	return evidence, nil
}

func sourceQueryFailurePhase(err error) SourceQueryPhase {
	var executionErr *sourceExecutionError
	if !errors.As(err, &executionErr) {
		return SourceQueryPhaseUnknown
	}
	switch executionErr.phase {
	case SourceQueryPhaseQuery, SourceQueryPhaseScan, SourceQueryPhaseIteration, SourceQueryPhaseClose:
		return executionErr.phase
	default:
		return SourceQueryPhaseUnknown
	}
}

func scanEvidenceRow(rows ClickHouseRowScanner) (contractsv1.EvidenceRef, error) {
	var id, system, entityType, entityID, label, safeURI, provenance, citation string
	var confidence float64
	var observedAt time.Time
	if err := rows.Scan(&id, &system, &entityType, &entityID, &label, &safeURI, &provenance, &confidence, &citation, &observedAt); err != nil {
		return contractsv1.EvidenceRef{}, err
	}
	return contractsv1.EvidenceRef{
		SchemaVersion: contractsv1.EvidenceRefSchema,
		EvidenceRefID: id,
		Source:        contractsv1.EvidenceSource{System: system, EntityType: entityType, EntityID: entityID, DisplayLabel: label, SafeURI: safeURI},
		Provenance:    provenance,
		Confidence:    confidence,
		Citation:      citation,
		ObservedAt:    observedAt.UTC(),
		Availability:  contractsv1.EvidenceAvailable,
	}, nil
}
