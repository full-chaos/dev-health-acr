package contextpacket

import (
	"context"
	"errors"
	"fmt"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// ClickHouseRowScanner is the narrow query boundary needed by production
// adapters and supports real driver implementations without exposing a driver.
type ClickHouseRowScanner interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close() error
}

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
	statement := "SELECT * FROM (" + query.Statement + ") ORDER BY observed_at DESC, evidence_ref_id ASC LIMIT {source_row_limit:UInt32}"
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
